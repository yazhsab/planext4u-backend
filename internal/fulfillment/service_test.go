package fulfillment

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/order"
)

func TestBEP4008RiderOnboardingDutyAndAtomicConcurrentOffer(t *testing.T) {
	service, _ := fulfillmentFixture(t)
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	task, err := service.SeedTask(admin, fulfillmentTaskSeed("delivery-concurrent-001", "food-order-001"))
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = service.OfferTask(admin, "dispatch-offer-concurrent1", task.ID, task.Revision)
	if err != nil || task.Status != "OFFERED" {
		t.Fatalf("offered=%#v err=%v", task, err)
	}
	const riders = 16
	actors := make([]Actor, riders)
	for index := range actors {
		actors[index] = approvedRiderFixture(t, service, fmt.Sprintf("rider-concurrent-%03d", index))
		if _, _, err := service.StartDuty(actors[index], fmt.Sprintf("duty-start-concurrent-%03d", index), "600001"); err != nil {
			t.Fatal(err)
		}
	}
	var successes atomic.Int64
	var wait sync.WaitGroup
	for index, actor := range actors {
		wait.Add(1)
		go func(index int, actor Actor) {
			defer wait.Done()
			_, _, err := service.AcceptOffer(actor, fmt.Sprintf("offer-accept-concurrent-%03d", index), task.ID, task.Revision)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, ErrConflict) {
				t.Errorf("accept %d error=%v", index, err)
			}
		}(index, actor)
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("offer winners=%d, want 1", successes.Load())
	}
	values, err := service.Tasks(admin)
	if err != nil || len(values) != 1 || values[0].Status != "ASSIGNED" || values[0].AssignedRiderID == "" {
		t.Fatalf("tasks=%#v err=%v", values, err)
	}
}

func TestBE005RiderDeclineIsScopedIdempotentAndPreservesOtherRiderAcceptance(t *testing.T) {
	service, _ := fulfillmentFixture(t)
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	decliningRider := approvedRiderFixture(t, service, "rider-decline-001")
	acceptingRider := approvedRiderFixture(t, service, "rider-decline-002")
	for index, rider := range []Actor{decliningRider, acceptingRider} {
		if _, _, err := service.StartDuty(rider, fmt.Sprintf("duty-start-decline-%03d", index), "600001"); err != nil {
			t.Fatal(err)
		}
	}
	task, err := service.SeedTask(admin, fulfillmentTaskSeed("delivery-decline-001", "food-order-decline-001"))
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = service.OfferTask(admin, "dispatch-offer-decline-001", task.ID, task.Revision)
	if err != nil || !contains(task.AllowedActions, "DECLINE") {
		t.Fatalf("offered=%#v err=%v", task, err)
	}
	request := OfferDeclineRequest{ReasonCode: "too_far"}
	decline, replay, err := service.DeclineOffer(decliningRider, "rider-decline-command-001", task.ID, task.Revision, request)
	if err != nil || replay || decline.ReasonCode != "TOO_FAR" || decline.TaskRevision != task.Revision {
		t.Fatalf("decline=%#v replay=%v err=%v", decline, replay, err)
	}
	again, replay, err := service.DeclineOffer(decliningRider, "rider-decline-command-001", task.ID, task.Revision, request)
	if err != nil || !replay || again != decline {
		t.Fatalf("decline replay=%#v replay=%v err=%v", again, replay, err)
	}
	if _, _, err := service.DeclineOffer(decliningRider, "rider-decline-command-001", task.ID, task.Revision, OfferDeclineRequest{ReasonCode: "ENDING_DUTY"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("decline idempotency conflict=%v", err)
	}
	if offers, err := service.Offers(decliningRider); err != nil || len(offers) != 0 {
		t.Fatalf("declining rider offers=%#v err=%v", offers, err)
	}
	if _, _, err := service.AcceptOffer(decliningRider, "rider-accept-after-decline", task.ID, task.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("accept after decline error=%v", err)
	}
	if offers, err := service.Offers(acceptingRider); err != nil || len(offers) != 1 {
		t.Fatalf("other rider offers=%#v err=%v", offers, err)
	}
	accepted, _, err := service.AcceptOffer(acceptingRider, "rider-accept-after-other-decline", task.ID, task.Revision)
	if err != nil || accepted.AssignedRiderID != acceptingRider.Subject {
		t.Fatalf("other rider accepted=%#v err=%v", accepted, err)
	}
}

func TestBE005RiderDeclineHonorsExpiryAndAcceptRace(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		service, now := fulfillmentFixture(t)
		admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
		rider := approvedRiderFixture(t, service, "rider-expired-decline")
		_, _, _ = service.StartDuty(rider, "duty-start-expired-decline", "600001")
		task, _ := service.SeedTask(admin, fulfillmentTaskSeed("delivery-expired-decline", "food-order-expired-decline"))
		task, _, _ = service.OfferTask(admin, "dispatch-expired-decline", task.ID, task.Revision)
		*now = now.Add(46 * time.Second)
		if _, _, err := service.DeclineOffer(rider, "rider-expired-decline-command", task.ID, task.Revision, OfferDeclineRequest{ReasonCode: "ENDING_DUTY"}); !errors.Is(err, ErrOfferExpired) {
			t.Fatalf("expired decline error=%v", err)
		}
	})

	t.Run("accept versus decline", func(t *testing.T) {
		service, _ := fulfillmentFixture(t)
		admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
		rider := approvedRiderFixture(t, service, "rider-decision-race")
		_, _, _ = service.StartDuty(rider, "duty-start-decision-race", "600001")
		task, _ := service.SeedTask(admin, fulfillmentTaskSeed("delivery-decision-race", "food-order-decision-race"))
		task, _, _ = service.OfferTask(admin, "dispatch-decision-race", task.ID, task.Revision)
		results := make(chan error, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			_, _, err := service.AcceptOffer(rider, "rider-race-accept-command", task.ID, task.Revision)
			results <- err
		}()
		go func() {
			defer wait.Done()
			_, _, err := service.DeclineOffer(rider, "rider-race-decline-command", task.ID, task.Revision, OfferDeclineRequest{ReasonCode: "TOO_FAR"})
			results <- err
		}()
		wait.Wait()
		close(results)
		succeeded, conflicted := 0, 0
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, ErrConflict):
				conflicted++
			default:
				t.Fatalf("decision race error=%v", err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("decision race succeeded=%d conflicted=%d", succeeded, conflicted)
		}
	})
}

func TestBEP4008LocationOfflineRecoveryPODAndReassignment(t *testing.T) {
	service, now := fulfillmentFixture(t)
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	rider := approvedRiderFixture(t, service, "rider-lifecycle-001")
	if _, _, err := service.StartDuty(rider, "duty-start-lifecycle-01", "600001"); err != nil {
		t.Fatal(err)
	}
	task, _ := service.SeedTask(admin, fulfillmentTaskSeed("delivery-lifecycle-001", "food-order-002"))
	task, _, _ = service.OfferTask(admin, "dispatch-offer-lifecycle1", task.ID, task.Revision)
	task, _, err := service.AcceptOffer(rider, "rider-accept-lifecycle-1", task.ID, task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	location, replay, err := service.UpdateLocation(rider, "rider-location-command1", LocationUpdate{Sequence: 1, Point: Point{Latitude: 13.0827, Longitude: 80.2707}, AccuracyM: 8, CapturedAt: *now})
	if err != nil || replay || location.Sequence != 1 {
		t.Fatalf("location=%#v replay=%v err=%v", location, replay, err)
	}
	if _, _, err := service.UpdateLocation(rider, "rider-location-stale-01", LocationUpdate{Sequence: 1, Point: Point{Latitude: 13.0828, Longitude: 80.2708}, AccuracyM: 9, CapturedAt: *now}); !errors.Is(err, ErrLocationStale) {
		t.Fatalf("stale location error=%v", err)
	}
	results, err := service.RecoverOffline(rider, []OfflineCommand{
		{DeviceSequence: 2, CommandID: "offline-pickup-command2", Kind: "PICKUP", TaskID: task.ID, Revision: task.Revision},
		{DeviceSequence: 1, CommandID: "offline-location-command1", Kind: "LOCATION", Payload: map[string]any{"latitude": 13.083, "longitude": 80.271, "accuracy_m": 10.0, "captured_at": now.Format(time.RFC3339)}},
	})
	if err != nil || len(results) != 2 || results[0].DeviceSequence != 1 || results[1].ResourceStatus != "PICKED_UP" {
		t.Fatalf("offline=%#v err=%v", results, err)
	}
	tasks, _ := service.Tasks(rider)
	task = tasks[0]
	if _, _, err := service.CompleteDelivery(rider, "rider-pod-raw-photo001", task.ID, task.Revision, CompletionRequest{OTP: "135790", BlurredPhotoAssetID: "asset-raw-customer-photo"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unblurred POD error=%v", err)
	}
	if _, _, err := service.CompleteDelivery(rider, "rider-pod-wrong-otp001", task.ID, task.Revision, CompletionRequest{OTP: "000000", BlurredPhotoAssetID: "asset-blurred-proof-001"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong OTP error=%v", err)
	}
	task, replay, err = service.CompleteDelivery(rider, "rider-pod-complete-001", task.ID, task.Revision, CompletionRequest{OTP: "135790", BlurredPhotoAssetID: "asset-blurred-proof-001"})
	if err != nil || replay || task.Status != "DELIVERED" || task.PODBlurredAssetID == "" {
		t.Fatalf("delivered=%#v replay=%v err=%v", task, replay, err)
	}
	entries, err := service.Ledger(rider, rider.Subject)
	if err != nil || len(entries) != 1 || entries[0].CalculationVersion != "rider-commission-v1" {
		t.Fatalf("earnings=%#v err=%v", entries, err)
	}

	second := fulfillmentTaskSeed("delivery-reassign-001", "food-order-003")
	task, _ = service.SeedTask(admin, second)
	task, _, _ = service.OfferTask(admin, "dispatch-offer-reassign01", task.ID, task.Revision)
	task, _, _ = service.AcceptOffer(rider, "rider-accept-reassign-01", task.ID, task.Revision)
	*now = now.Add(3 * time.Minute)
	count, err := service.SweepStaleAssignments(admin, "region-chennai", "Rider location heartbeat expired")
	if err != nil || count != 1 {
		t.Fatalf("sweep count=%d err=%v", count, err)
	}
	tasks, _ = service.Tasks(admin)
	for _, candidate := range tasks {
		if candidate.ID == task.ID {
			task = candidate
		}
	}
	if task.Status != "REASSIGNMENT_REQUIRED" {
		t.Fatalf("reassignment status=%s", task.Status)
	}
	task, _, err = service.Reassign(admin, "dispatch-reassign-command1", task.ID, task.Revision, "Rider went offline beyond TTL")
	if err != nil || task.Status != "OFFERED" || task.AssignedRiderID != "" || task.ReassignmentCount != 1 {
		t.Fatalf("reassigned=%#v err=%v", task, err)
	}
}

func TestBEP4009BoundedChatScopeRedactionRetryBlockAndExpiry(t *testing.T) {
	service, now := fulfillmentFixture(t)
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	rider := approvedRiderFixture(t, service, "rider-chat-001")
	_, _, _ = service.StartDuty(rider, "duty-start-chat-0001", "600001")
	seed := fulfillmentTaskSeed("delivery-chat-001", "food-order-chat-001")
	task, _ := service.SeedTask(admin, seed)
	task, _, _ = service.OfferTask(admin, "dispatch-offer-chat-001", task.ID, task.Revision)
	task, _, _ = service.AcceptOffer(rider, "rider-accept-chat-001", task.ID, task.Revision)
	customer := fulfillmentActor(seed.CustomerID, "CUSTOMER", false)
	conversation, err := service.Conversation(customer, seed.OrderID)
	if err != nil || !contains(conversation.ParticipantIDs, rider.Subject) {
		t.Fatalf("conversation=%#v err=%v", conversation, err)
	}
	message, replay, err := service.SendMessage(customer, "chat-send-command-0001", conversation.ID, "Call me on +91 98765 43210 or customer@example.com")
	if err != nil || replay || !message.Redacted || stringsContainsAny(message.Body, "98765", "customer@example.com") {
		t.Fatalf("message=%#v replay=%v err=%v", message, replay, err)
	}
	again, replay, err := service.SendMessage(customer, "chat-send-command-0001", conversation.ID, "Call me on +91 98765 43210 or customer@example.com")
	if err != nil || !replay || again.ID != message.ID {
		t.Fatalf("message replay=%#v replay=%v err=%v", again, replay, err)
	}
	read, _, err := service.MessageReceipt(rider, "chat-read-command-0001", conversation.ID, message.ID, "READ")
	if err != nil || len(read.Receipts) != 2 {
		t.Fatalf("receipt=%#v err=%v", read, err)
	}
	attacker := fulfillmentActor("rider-chat-attacker", "RIDER", false)
	if _, _, err := service.SendMessage(attacker, "chat-attacker-command01", conversation.ID, "Unauthorized"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("chat scope error=%v", err)
	}
	task, _, _ = service.MarkPickedUp(rider, "rider-chat-pickup-001", task.ID, task.Revision)
	task, _, _ = service.CompleteDelivery(rider, "rider-chat-delivery-01", task.ID, task.Revision, CompletionRequest{OTP: "135790", BlurredPhotoAssetID: "asset-blurred-chat-001"})
	*now = now.Add(2*time.Hour + time.Second)
	if _, _, err := service.SendMessage(customer, "chat-expired-command-01", conversation.ID, "Are you there?"); !errors.Is(err, ErrChatExpired) {
		t.Fatalf("chat expiry error=%v", err)
	}

	seed = fulfillmentTaskSeed("delivery-chat-block-01", "food-order-chat-block")
	_, _ = service.SeedTask(admin, seed)
	conversation, _ = service.Conversation(fulfillmentActor(seed.CustomerID, "CUSTOMER", false), seed.OrderID)
	conversation, _, err = service.BlockConversation(fulfillmentActor(seed.CustomerID, "CUSTOMER", false), "chat-block-command-001", conversation.ID, "User reported unsafe contact")
	if err != nil || !conversation.Blocked {
		t.Fatalf("block=%#v err=%v", conversation, err)
	}
	if _, _, err := service.SendMessage(fulfillmentActor(seed.CustomerID, "CUSTOMER", false), "chat-blocked-send-0001", conversation.ID, "Blocked message"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("blocked send error=%v", err)
	}
}

func TestBEP4010ImmutableSettlementFourEyesRetryAndZeroVariance(t *testing.T) {
	service, now := fulfillmentFixture(t)
	worker := fulfillmentActor("settlement-worker-001", "SETTLEMENT_WORKER", false)
	entry, err := service.SeedSettlement(worker, "vendor-settlement-001", "service-booking-settled-001", "VENDOR_SERVICE_SETTLEMENT", Money{AmountMinor: 100000, Currency: "INR"})
	if err != nil || entry.Net.AmountMinor != 88200 || entry.Commission.AmountMinor != 10000 || entry.Tax.AmountMinor != 1800 || entry.CalculationVersion != "settlement-v1" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	vendor := fulfillmentActor("vendor-settlement-001", "VENDOR", false)
	if _, _, err := service.RequestPayout(vendor, "payout-before-cooling-01", []string{entry.ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cooling error=%v", err)
	}
	*now = now.Add(49 * time.Hour)
	payout, replay, err := service.RequestPayout(vendor, "payout-request-command1", []string{entry.ID})
	if err != nil || replay || payout.Amount.AmountMinor != entry.Net.AmountMinor || payout.Status != "PENDING_REVIEW" {
		t.Fatalf("payout=%#v replay=%v err=%v", payout, replay, err)
	}
	financeOne := fulfillmentActor("finance-approver-001", "FINANCE", true)
	financeTwo := fulfillmentActor("finance-approver-002", "FINANCE", true)
	payout, _, err = service.ApprovePayout(financeOne, "payout-first-approval1", payout.ID, payout.Revision, "First payout control approved")
	if err != nil || payout.Status != "FIRST_APPROVED" {
		t.Fatalf("first approval=%#v err=%v", payout, err)
	}
	if _, _, err := service.ApprovePayout(financeOne, "payout-same-approval-01", payout.ID, payout.Revision, "Attempt same user approval"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same approver error=%v", err)
	}
	payout, _, err = service.ApprovePayout(financeTwo, "payout-second-approve1", payout.ID, payout.Revision, "Second payout control approved")
	if err != nil || payout.Status != "APPROVED" || payout.FirstApproverID == payout.SecondApproverID {
		t.Fatalf("second approval=%#v err=%v", payout, err)
	}
	payout, _, err = service.ExecutePayout(financeTwo, "payout-provider-fail-01", payout.ID, payout.Revision, "provider-payout-ref-001", false)
	if err != nil || payout.Status != "FAILED" || payout.AttemptCount != 1 {
		t.Fatalf("failed payout=%#v err=%v", payout, err)
	}
	payout, _, err = service.ExecutePayout(financeTwo, "payout-provider-retry01", payout.ID, payout.Revision, "provider-payout-ref-001", true)
	if err != nil || payout.Status != "PAID" || payout.AttemptCount != 2 {
		t.Fatalf("paid retry=%#v err=%v", payout, err)
	}
	reconciliation, err := service.Reconcile(financeOne, vendor.Subject)
	if err != nil || reconciliation.VarianceMinor != 0 || reconciliation.PaidMinor != entry.Net.AmountMinor {
		t.Fatalf("reconciliation=%#v err=%v", reconciliation, err)
	}
}

func TestOrderSettlementUsesCapturedCommercialTerms(t *testing.T) {
	t.Parallel()
	service, _ := fulfillmentFixture(t)
	worker := fulfillmentActor("settlement-worker-001", "SETTLEMENT_WORKER", false)
	snapshot := order.CheckoutSnapshot{
		PricingPolicyVersion: "pricing-2026-08", CommercialPolicyVersion: "commercial-2026-08", Total: order.Money{AmountMinor: 90000, Currency: "INR"},
		Lines: []order.LineSnapshot{{VendorID: "vendor-settlement-001", LineTotal: order.Money{AmountMinor: 100000, Currency: "INR"}, DiscountMinor: 10000, CommissionMinor: 12000}},
	}
	entry, err := service.SeedOrderSettlement(worker, "vendor-settlement-001", "order-settled-001", "VENDOR_ORDER_SETTLEMENT", "vendor-settlement-001", snapshot)
	if err != nil || entry.Gross.AmountMinor != 90000 || entry.Commission.AmountMinor != 12000 || entry.Tax.AmountMinor != 2160 || entry.Net.AmountMinor != 75840 || entry.CalculationVersion != "order:pricing-2026-08:commercial-2026-08" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
}

func TestBEP4011TerritoryGeoRBACMFAAuditAndDashboard(t *testing.T) {
	service, _ := fulfillmentFixture(t)
	field := fulfillmentActor("field-officer-001", "FIELD_OFFICER", false)
	if _, _, err := service.FieldCheckIn(field, "field-checkin-outside01", "territory-chennai-core", Point{Latitude: 12.9716, Longitude: 77.5946}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outside check-in error=%v", err)
	}
	checkIn, replay, err := service.FieldCheckIn(field, "field-checkin-inside-01", "territory-chennai-core", Point{Latitude: 13.083, Longitude: 80.271})
	if err != nil || replay || checkIn.DistanceM > 1000 {
		t.Fatalf("check-in=%#v replay=%v err=%v", checkIn, replay, err)
	}
	franchise := fulfillmentActor("franchise-chennai-001", "FRANCHISE_ADMIN", true)
	territories, err := service.Territories(franchise)
	if err != nil || len(territories) != 1 || territories[0].ID != "territory-chennai-core" {
		t.Fatalf("territories=%#v err=%v", territories, err)
	}
	wrongFranchise := fulfillmentActor("franchise-bengaluru-001", "FRANCHISE_ADMIN", true)
	if _, err := service.RegionalDashboard(wrongFranchise, "region-chennai"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("territory isolation error=%v", err)
	}
	noMFA := fulfillmentActor("regional-admin-001", "REGIONAL_ADMIN", false)
	if _, err := service.RegionalDashboard(noMFA, "region-chennai"); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("MFA error=%v", err)
	}
	dashboard, err := service.RegionalDashboard(franchise, "region-chennai")
	if err != nil || dashboard.Territories != 1 || len(dashboard.RecentFieldCheckIns) != 1 {
		t.Fatalf("dashboard=%#v err=%v", dashboard, err)
	}
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	audits, err := service.Audits(admin)
	if err != nil || len(audits) == 0 || audits[0].ActorID != field.Subject {
		t.Fatalf("audits=%#v err=%v", audits, err)
	}
}

func fulfillmentFixture(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	service, err := NewService(Configuration{OfferTTL: 45 * time.Second, LocationTTL: 2 * time.Minute, ChatAfterDeliveryTTL: 2 * time.Hour, SettlementCooling: 48 * time.Hour, CommissionBasisPoints: 1000, TaxBasisPoints: 1800, Territories: []Territory{
		{ID: "territory-chennai-core", RegionID: "region-chennai", FranchiseID: "franchise-chennai-001", Name: "Chennai core", Center: Point{Latitude: 13.0827, Longitude: 80.2707}, RadiusKM: 25, PostalCodes: []string{"600001", "600002"}, tenantID: "tenant-synthetic-001", country: "IN"},
		{ID: "territory-bengaluru-core", RegionID: "region-bengaluru", FranchiseID: "franchise-bengaluru-001", Name: "Bengaluru core", Center: Point{Latitude: 12.9716, Longitude: 77.5946}, RadiusKM: 25, PostalCodes: []string{"560001"}, tenantID: "tenant-synthetic-001", country: "IN"},
	}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service, &now
}

func fulfillmentActor(subject, role string, mfa bool) Actor {
	return Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: subject, Roles: []string{role}, MFAVerified: mfa}
}

func approvedRiderFixture(t *testing.T, service *Service, subject string) Actor {
	t.Helper()
	rider := fulfillmentActor(subject, "RIDER", false)
	profile, _, err := service.RegisterRider(rider, "rider-register-"+subject, RiderRegistrationRequest{FullName: "Synthetic Rider", PhoneMasked: "******1234", VehicleType: "MOTORBIKE", VehicleNumber: "TN01AB1234", Documents: []RiderDocument{{Kind: "DRIVER_LICENSE", AssetID: "asset-rider-license"}, {Kind: "IDENTITY", AssetID: "asset-rider-identity"}}, BankReference: "bankref_rider001", Zones: []string{"600001"}})
	if err != nil {
		t.Fatal(err)
	}
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	profile, _, err = service.ReviewRider(admin, "rider-review-"+subject, subject, profile.Revision, true, "Rider KYC and bank verified")
	if err != nil || profile.Status != RiderApproved {
		t.Fatalf("rider review=%#v err=%v", profile, err)
	}
	return rider
}

func fulfillmentTaskSeed(id, orderID string) TaskSeed {
	return TaskSeed{ID: id, OrderID: orderID, OrderType: "FOOD", RegionID: "region-chennai", TerritoryID: "territory-chennai-core", ZoneID: "600001", Pickup: Stop{Label: "Saravana Kitchen", AddressToken: "address-token-pickup", Point: Point{Latitude: 13.0827, Longitude: 80.2707}}, Dropoff: Stop{Label: "Customer", AddressToken: "address-token-dropoff", Point: Point{Latitude: 13.0674, Longitude: 80.2376}}, DistanceMeters: 4800, Earning: Money{AmountMinor: 8000, Currency: "INR"}, DeliveryOTP: "135790", CustomerID: "customer-" + id, CounterpartyID: "restaurant-owner-001"}
}

func stringsContainsAny(value string, values ...string) bool {
	for _, candidate := range values {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
