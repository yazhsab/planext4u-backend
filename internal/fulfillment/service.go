package fulfillment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type idempotentResult struct {
	fingerprint string
	resourceID  string
}

type taskParticipants struct {
	customerID     string
	counterpartyID string
}

type Service struct {
	clock             func() time.Time
	configuration     Configuration
	mu                sync.Mutex
	sequence          int64
	riders            map[string]*RiderProfile
	duty              map[string]*DutySession
	tasks             map[string]*DeliveryTask
	participants      map[string]taskParticipants
	locations         map[string]*RiderLocation
	offlineSequence   map[string]int64
	offlineProcessed  map[string]map[string]OfflineResult
	conversations     map[string]*Conversation
	conversationOrder map[string]string
	ledger            map[string]*LedgerEntry
	payouts           map[string]*Payout
	paidEntries       map[string]bool
	attendance        map[string]*AttendanceEntry
	territories       map[string]Territory
	fieldCheckIns     map[string]*FieldCheckIn
	audit             []*AuditEvent
	idempotency       map[string]idempotentResult
}

func NewService(configuration Configuration, clock func() time.Time) (*Service, error) {
	if clock == nil || configuration.CommissionBasisPoints < 0 || configuration.CommissionBasisPoints > 10000 || configuration.TaxBasisPoints < 0 || configuration.TaxBasisPoints > 10000 {
		return nil, ErrInvalidRequest
	}
	if configuration.OfferTTL <= 0 {
		configuration.OfferTTL = 45 * time.Second
	}
	if configuration.LocationTTL <= 0 {
		configuration.LocationTTL = 2 * time.Minute
	}
	if configuration.ChatAfterDeliveryTTL <= 0 {
		configuration.ChatAfterDeliveryTTL = 2 * time.Hour
	}
	if configuration.SettlementCooling <= 0 {
		configuration.SettlementCooling = 48 * time.Hour
	}
	service := &Service{
		clock: clock, configuration: configuration, riders: map[string]*RiderProfile{}, duty: map[string]*DutySession{}, tasks: map[string]*DeliveryTask{}, participants: map[string]taskParticipants{}, locations: map[string]*RiderLocation{}, offlineSequence: map[string]int64{}, offlineProcessed: map[string]map[string]OfflineResult{}, conversations: map[string]*Conversation{}, conversationOrder: map[string]string{}, ledger: map[string]*LedgerEntry{}, payouts: map[string]*Payout{}, paidEntries: map[string]bool{}, attendance: map[string]*AttendanceEntry{}, territories: map[string]Territory{}, fieldCheckIns: map[string]*FieldCheckIn{}, audit: []*AuditEvent{}, idempotency: map[string]idempotentResult{},
	}
	for _, territory := range configuration.Territories {
		if territory.tenantID == "" {
			territory.tenantID = configuration.TenantID
		}
		if territory.country == "" {
			territory.country = configuration.Country
		}
		if !validTerritory(territory) {
			return nil, ErrInvalidRequest
		}
		service.territories[territory.ID] = cloneTerritory(territory)
	}
	return service, nil
}

func (service *Service) RegisterRider(actor Actor, key string, request RiderRegistrationRequest) (RiderProfile, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || !validRiderRegistration(request) {
		return RiderProfile{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "rider-register", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return RiderProfile{}, false, ErrIdempotencyConflict
		}
		return cloneRider(*service.riders[actor.Subject]), true, nil
	}
	if service.riders[actor.Subject] != nil {
		return RiderProfile{}, false, ErrConflict
	}
	now := service.clock().UTC()
	service.sequence++
	value := &RiderProfile{ID: "rider-profile-" + sequenceID(service.sequence), RiderID: actor.Subject, Revision: 1, Status: RiderKYCReview, FullName: strings.TrimSpace(request.FullName), PhoneMasked: request.PhoneMasked, VehicleType: request.VehicleType, VehicleNumber: strings.ToUpper(request.VehicleNumber), Documents: cloneRiderDocuments(request.Documents), BankReference: request.BankReference, BankStatus: "PENDING_VERIFICATION", Zones: append([]string(nil), request.Zones...), MaxConcurrent: 2, AllowedActions: []string{"VIEW_REVIEW_STATUS"}, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	for index := range value.Documents {
		value.Documents[index].OCRStatus = "REVIEW_REQUIRED"
	}
	service.riders[actor.Subject] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneRider(*value), false, nil
}

func (service *Service) Rider(actor Actor) (RiderProfile, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") {
		return RiderProfile{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.riders[actor.Subject]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return RiderProfile{}, ErrNotFound
	}
	return cloneRider(*value), nil
}

func (service *Service) ReviewRider(actor Actor, key, riderID string, revision int64, approved bool, reason string) (RiderProfile, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validKey(key) || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return RiderProfile{}, false, ErrMFARequired
		}
		return RiderProfile{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.riders[riderID]
	if value == nil {
		return RiderProfile{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return RiderProfile{}, false, ErrForbidden
	}
	fingerprint := digest(struct {
		Approved bool
		Reason   string
	}{approved, reason})
	scope := idempotencyScope(actor, "rider-review:"+riderID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return RiderProfile{}, false, ErrIdempotencyConflict
		}
		return cloneRider(*value), true, nil
	}
	if value.Revision != revision || value.Status != RiderKYCReview {
		return RiderProfile{}, false, ErrConflict
	}
	for index := range value.Documents {
		value.Documents[index].OCRStatus = map[bool]string{true: "VERIFIED", false: "REJECTED"}[approved]
	}
	if approved {
		value.Status, value.BankStatus, value.AllowedActions = RiderApproved, "VERIFIED", []string{"START_DUTY", "VIEW_EARNINGS"}
	} else {
		value.Status, value.AllowedActions = RiderSuspended, nil
	}
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	service.recordAuditLocked(actor, "RIDER_REVIEW", "RIDER_PROFILE", riderID, reason)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneRider(*value), false, nil
}

func (service *Service) StartDuty(actor Actor, key, zoneID string) (DutySession, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || !safeID(zoneID) {
		return DutySession{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	profile := service.riders[actor.Subject]
	if profile == nil || profile.Status != RiderApproved || !contains(profile.Zones, zoneID) || profile.tenantID != actor.TenantID || profile.country != actor.Country {
		return DutySession{}, false, ErrForbidden
	}
	fingerprint := digest(zoneID)
	scope := idempotencyScope(actor, "duty-start", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return DutySession{}, false, ErrIdempotencyConflict
		}
		return *service.duty[actor.Subject], true, nil
	}
	if existing := service.duty[actor.Subject]; existing != nil && existing.Status == "ACTIVE" {
		return DutySession{}, false, ErrConflict
	}
	now := service.clock().UTC()
	service.sequence++
	value := &DutySession{ID: "duty-session-" + sequenceID(service.sequence), RiderID: actor.Subject, Revision: 1, Status: "ACTIVE", StartedAt: now, LastSeenAt: now, ZoneID: zoneID, tenantID: actor.TenantID, country: actor.Country}
	service.duty[actor.Subject] = value
	service.recordAttendanceLocked(actor, value.ID, "DUTY_START", Point{}, "RIDER_APP")
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return *value, false, nil
}

func (service *Service) EndDuty(actor Actor, key string, revision int64) (DutySession, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) {
		return DutySession{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.duty[actor.Subject]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return DutySession{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "duty-end", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != digest(revision) {
			return DutySession{}, false, ErrIdempotencyConflict
		}
		return *value, true, nil
	}
	if value.Revision != revision || value.Status != "ACTIVE" || service.activeTaskCountLocked(actor.Subject) > 0 {
		return DutySession{}, false, ErrConflict
	}
	now := service.clock().UTC()
	value.Status, value.Revision, value.EndedAt, value.LastSeenAt = "ENDED", value.Revision+1, &now, now
	service.recordAttendanceLocked(actor, value.ID, "DUTY_END", Point{}, "RIDER_APP")
	service.idempotency[scope] = idempotentResult{fingerprint: digest(revision), resourceID: value.ID}
	return *value, false, nil
}

func (service *Service) Duty(actor Actor) (DutySession, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") {
		return DutySession{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.duty[actor.Subject]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return DutySession{}, ErrNotFound
	}
	result := *value
	result.ActiveTasks = service.activeTaskCountLocked(actor.Subject)
	return result, nil
}

func (service *Service) SeedTask(actor Actor, seed TaskSeed) (DeliveryTask, error) {
	if !validActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validTaskSeed(seed) {
		if !actor.MFAVerified {
			return DeliveryTask{}, ErrMFARequired
		}
		return DeliveryTask{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.tasks[seed.ID] != nil {
		return DeliveryTask{}, ErrConflict
	}
	now := service.clock().UTC()
	value := &DeliveryTask{ID: seed.ID, Revision: 1, OrderID: seed.OrderID, OrderType: seed.OrderType, RegionID: seed.RegionID, TerritoryID: seed.TerritoryID, ZoneID: seed.ZoneID, Status: "READY_FOR_DISPATCH", Pickup: seed.Pickup, Dropoff: seed.Dropoff, DistanceMeters: seed.DistanceMeters, Earning: seed.Earning, AllowedActions: []string{"OFFER"}, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country, deliveryOTPHash: digest(seed.DeliveryOTP)}
	service.tasks[value.ID] = value
	service.participants[value.ID] = taskParticipants{customerID: seed.CustomerID, counterpartyID: seed.CounterpartyID}
	service.sequence++
	conversation := &Conversation{ID: "conversation-" + sequenceID(service.sequence), OrderID: value.OrderID, ParticipantIDs: []string{seed.CustomerID, seed.CounterpartyID}, ExpiresAt: now.Add(24 * time.Hour), Messages: []ChatMessage{}, tenantID: actor.TenantID, country: actor.Country}
	service.conversations[conversation.ID], service.conversationOrder[value.OrderID] = conversation, conversation.ID
	service.recordAuditLocked(actor, "TASK_CREATED", "DELIVERY_TASK", value.ID, "Dispatch task created")
	return cloneTask(*value), nil
}

func (service *Service) OfferTask(actor Actor, key, taskID string, revision int64) (DeliveryTask, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validKey(key) {
		if !actor.MFAVerified {
			return DeliveryTask{}, false, ErrMFARequired
		}
		return DeliveryTask{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.tasks[taskID]
	if value == nil {
		return DeliveryTask{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return DeliveryTask{}, false, ErrForbidden
	}
	scope := idempotencyScope(actor, "offer:"+taskID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != digest(revision) {
			return DeliveryTask{}, false, ErrIdempotencyConflict
		}
		return cloneTask(*value), true, nil
	}
	if value.Revision != revision || (value.Status != "READY_FOR_DISPATCH" && value.Status != "REASSIGNMENT_REQUIRED") {
		return DeliveryTask{}, false, ErrConflict
	}
	now := service.clock().UTC()
	expires := now.Add(service.configuration.OfferTTL)
	value.Status, value.Revision, value.OfferExpiresAt, value.UpdatedAt = "OFFERED", value.Revision+1, &expires, now
	value.AssignedRiderID, value.AllowedActions = "", []string{"ACCEPT"}
	service.recordAuditLocked(actor, "TASK_OFFERED", "DELIVERY_TASK", value.ID, "Task published to eligible riders")
	service.idempotency[scope] = idempotentResult{fingerprint: digest(revision), resourceID: value.ID}
	return cloneTask(*value), false, nil
}

func (service *Service) Offers(actor Actor) ([]DeliveryTask, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	profile, duty := service.riders[actor.Subject], service.duty[actor.Subject]
	if profile == nil || profile.Status != RiderApproved || duty == nil || duty.Status != "ACTIVE" || profile.tenantID != actor.TenantID || profile.country != actor.Country {
		return nil, ErrForbidden
	}
	now := service.clock().UTC()
	values := []DeliveryTask{}
	for _, value := range service.tasks {
		if value.tenantID == actor.TenantID && value.country == actor.Country && value.Status == "OFFERED" && value.OfferExpiresAt != nil && value.OfferExpiresAt.After(now) && value.ZoneID == duty.ZoneID {
			values = append(values, cloneTask(*value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].OfferExpiresAt.Before(*values[j].OfferExpiresAt) })
	return values, nil
}

func (service *Service) AcceptOffer(actor Actor, key, taskID string, revision int64) (DeliveryTask, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) {
		return DeliveryTask{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.tasks[taskID]
	if value == nil {
		return DeliveryTask{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "accept:"+taskID, key)
	fingerprint := digest(revision)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return DeliveryTask{}, false, ErrIdempotencyConflict
		}
		return cloneTask(*value), true, nil
	}
	profile, duty := service.riders[actor.Subject], service.duty[actor.Subject]
	if profile == nil || duty == nil || profile.Status != RiderApproved || duty.Status != "ACTIVE" || duty.ZoneID != value.ZoneID || value.tenantID != actor.TenantID || value.country != actor.Country {
		return DeliveryTask{}, false, ErrForbidden
	}
	if value.OfferExpiresAt == nil || !value.OfferExpiresAt.After(service.clock().UTC()) {
		return DeliveryTask{}, false, ErrOfferExpired
	}
	if value.Revision != revision || value.Status != "OFFERED" || value.AssignedRiderID != "" {
		return DeliveryTask{}, false, ErrConflict
	}
	if service.activeTaskCountLocked(actor.Subject) >= profile.MaxConcurrent {
		return DeliveryTask{}, false, ErrConflict
	}
	now := service.clock().UTC()
	value.Status, value.Revision, value.AssignedRiderID, value.AcceptedAt, value.UpdatedAt = "ASSIGNED", value.Revision+1, actor.Subject, &now, now
	value.AllowedActions = []string{"NAVIGATE_PICKUP", "MARK_PICKED_UP"}
	duty.LastSeenAt = now
	conversation := service.conversations[service.conversationOrder[value.OrderID]]
	if conversation != nil && !contains(conversation.ParticipantIDs, actor.Subject) {
		conversation.ParticipantIDs = append(conversation.ParticipantIDs, actor.Subject)
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneTask(*value), false, nil
}

func (service *Service) UpdateLocation(actor Actor, key string, input LocationUpdate) (RiderLocation, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || input.Sequence < 1 || !validPoint(input.Point) || input.AccuracyM <= 0 || input.AccuracyM > 500 || input.CapturedAt.IsZero() {
		return RiderLocation{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.updateLocationLocked(actor, key, input)
}

func (service *Service) updateLocationLocked(actor Actor, key string, input LocationUpdate) (RiderLocation, bool, error) {
	duty := service.duty[actor.Subject]
	if duty == nil || duty.Status != "ACTIVE" || duty.tenantID != actor.TenantID || duty.country != actor.Country {
		return RiderLocation{}, false, ErrForbidden
	}
	scope := idempotencyScope(actor, "location", key)
	fingerprint := digest(input)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return RiderLocation{}, false, ErrIdempotencyConflict
		}
		return *service.locations[actor.Subject], true, nil
	}
	existing := service.locations[actor.Subject]
	if existing != nil && input.Sequence <= existing.Sequence {
		return RiderLocation{}, false, ErrLocationStale
	}
	now := service.clock().UTC()
	if input.CapturedAt.After(now.Add(2*time.Minute)) || input.CapturedAt.Before(now.Add(-15*time.Minute)) {
		return RiderLocation{}, false, ErrLocationStale
	}
	value := &RiderLocation{RiderID: actor.Subject, Sequence: input.Sequence, Point: input.Point, AccuracyM: input.AccuracyM, UpdatedAt: now, ExpiresAt: now.Add(service.configuration.LocationTTL), tenantID: actor.TenantID, country: actor.Country}
	service.locations[actor.Subject] = value
	duty.LastSeenAt = now
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: actor.Subject}
	return *value, false, nil
}

func (service *Service) Location(actor Actor, riderID string) (RiderLocation, error) {
	if !validActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN", "CUSTOMER", "RESTAURANT_VENDOR") {
		return RiderLocation{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.locations[riderID]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return RiderLocation{}, ErrNotFound
	}
	if !value.ExpiresAt.After(service.clock().UTC()) {
		return RiderLocation{}, ErrLocationStale
	}
	return *value, nil
}

func (service *Service) MarkPickedUp(actor Actor, key, taskID string, revision int64) (DeliveryTask, bool, error) {
	return service.riderTaskTransition(actor, key, taskID, revision, "PICKED_UP", func(value *DeliveryTask, now time.Time) error {
		value.PickedUpAt = &now
		value.AllowedActions = []string{"NAVIGATE_DROPOFF", "COMPLETE_DELIVERY"}
		return nil
	})
}

func (service *Service) CompleteDelivery(actor Actor, key, taskID string, revision int64, request CompletionRequest) (DeliveryTask, bool, error) {
	if !strings.HasPrefix(request.BlurredPhotoAssetID, "asset-blurred-") || (!safeID(request.SignatureAssetID) && request.OTP == "") {
		return DeliveryTask{}, false, ErrInvalidRequest
	}
	return service.riderTaskTransition(actor, key, taskID, revision, "DELIVERED", func(value *DeliveryTask, now time.Time) error {
		if request.OTP != "" && digest(request.OTP) != value.deliveryOTPHash {
			return ErrForbidden
		}
		value.DeliveredAt = &now
		value.PODBlurredAssetID, value.PODSignatureID = request.BlurredPhotoAssetID, request.SignatureAssetID
		value.AllowedActions = nil
		service.createEarningLocked(value)
		if conversation := service.conversations[service.conversationOrder[value.OrderID]]; conversation != nil {
			conversation.ExpiresAt = now.Add(service.configuration.ChatAfterDeliveryTTL)
		}
		return nil
	})
}

func (service *Service) riderTaskTransition(actor Actor, key, taskID string, revision int64, status string, mutation func(*DeliveryTask, time.Time) error) (DeliveryTask, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) {
		return DeliveryTask{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.tasks[taskID]
	if value == nil {
		return DeliveryTask{}, false, ErrNotFound
	}
	fingerprint := digest(struct {
		Revision int64
		Status   string
	}{revision, status})
	scope := idempotencyScope(actor, "task:"+status+":"+taskID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return DeliveryTask{}, false, ErrIdempotencyConflict
		}
		return cloneTask(*value), true, nil
	}
	if value.AssignedRiderID != actor.Subject || value.tenantID != actor.TenantID || value.country != actor.Country {
		return DeliveryTask{}, false, ErrForbidden
	}
	allowedFrom := map[string]string{"PICKED_UP": "ASSIGNED", "DELIVERED": "PICKED_UP"}
	if value.Revision != revision || value.Status != allowedFrom[status] {
		return DeliveryTask{}, false, ErrInvalidTransition
	}
	now := service.clock().UTC()
	if err := mutation(value, now); err != nil {
		return DeliveryTask{}, false, err
	}
	value.Status, value.Revision, value.UpdatedAt = status, value.Revision+1, now
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneTask(*value), false, nil
}

func (service *Service) Tasks(actor Actor) ([]DeliveryTask, error) {
	if !validActor(actor) || !hasAnyRole(actor, "RIDER", "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN", "FRANCHISE_ADMIN") {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []DeliveryTask{}
	for _, value := range service.tasks {
		if value.tenantID != actor.TenantID || value.country != actor.Country {
			continue
		}
		if hasRole(actor, "RIDER") && value.AssignedRiderID != actor.Subject {
			continue
		}
		if hasRole(actor, "FRANCHISE_ADMIN") && !service.actorOwnsTerritory(actor.Subject, value.TerritoryID) {
			continue
		}
		values = append(values, cloneTask(*value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].UpdatedAt.After(values[j].UpdatedAt) })
	return values, nil
}

func (service *Service) Reassign(actor Actor, key, taskID string, revision int64, reason string) (DeliveryTask, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validKey(key) || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return DeliveryTask{}, false, ErrMFARequired
		}
		return DeliveryTask{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.tasks[taskID]
	if value == nil {
		return DeliveryTask{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "reassign:"+taskID, key)
	fingerprint := digest(struct {
		Revision int64
		Reason   string
	}{revision, reason})
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return DeliveryTask{}, false, ErrIdempotencyConflict
		}
		return cloneTask(*value), true, nil
	}
	if value.Revision != revision || (value.Status != "ASSIGNED" && value.Status != "REASSIGNMENT_REQUIRED") {
		return DeliveryTask{}, false, ErrInvalidTransition
	}
	now := service.clock().UTC()
	expires := now.Add(service.configuration.OfferTTL)
	value.Status, value.Revision, value.AssignedRiderID, value.OfferExpiresAt, value.UpdatedAt = "OFFERED", value.Revision+1, "", &expires, now
	value.ReassignmentCount++
	value.AllowedActions = []string{"ACCEPT"}
	service.recordAuditLocked(actor, "TASK_REASSIGNED", "DELIVERY_TASK", value.ID, reason)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneTask(*value), false, nil
}

func (service *Service) RecoverOffline(actor Actor, commands []OfflineCommand) ([]OfflineResult, error) {
	if !validActor(actor) || !hasRole(actor, "RIDER") || len(commands) == 0 || len(commands) > 100 {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	sorted := append([]OfflineCommand(nil), commands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].DeviceSequence < sorted[j].DeviceSequence })
	if service.offlineProcessed[actor.Subject] == nil {
		service.offlineProcessed[actor.Subject] = map[string]OfflineResult{}
	}
	expected := service.offlineSequence[actor.Subject] + 1
	results := make([]OfflineResult, 0, len(sorted))
	for _, command := range sorted {
		if previous, ok := service.offlineProcessed[actor.Subject][command.CommandID]; ok {
			results = append(results, previous)
			continue
		}
		if !validKey(command.CommandID) || command.DeviceSequence != expected {
			return nil, ErrConflict
		}
		result := OfflineResult{DeviceSequence: command.DeviceSequence, CommandID: command.CommandID, Status: "APPLIED"}
		switch command.Kind {
		case "LOCATION":
			location, err := locationFromPayload(command.Payload)
			if err != nil {
				return nil, err
			}
			location.Sequence = 1
			if current := service.locations[actor.Subject]; current != nil {
				location.Sequence = current.Sequence + 1
			}
			if _, _, err := service.updateLocationLocked(actor, command.CommandID, location); err != nil {
				return nil, err
			}
			result.ResourceStatus = "LOCATION_UPDATED"
		case "PICKUP":
			value := service.tasks[command.TaskID]
			if value == nil || value.AssignedRiderID != actor.Subject || value.Revision != command.Revision || value.Status != "ASSIGNED" {
				return nil, ErrConflict
			}
			now := service.clock().UTC()
			value.Status, value.Revision, value.PickedUpAt, value.UpdatedAt = "PICKED_UP", value.Revision+1, &now, now
			value.AllowedActions = []string{"NAVIGATE_DROPOFF", "COMPLETE_DELIVERY"}
			result.ResourceStatus = value.Status
		default:
			return nil, ErrInvalidRequest
		}
		service.offlineSequence[actor.Subject] = command.DeviceSequence
		expected++
		service.offlineProcessed[actor.Subject][command.CommandID] = result
		results = append(results, result)
	}
	return results, nil
}

func locationFromPayload(payload map[string]any) (LocationUpdate, error) {
	latitude, latOK := number(payload["latitude"])
	longitude, lonOK := number(payload["longitude"])
	accuracy, accuracyOK := number(payload["accuracy_m"])
	captured, capturedOK := payload["captured_at"].(string)
	parsed, err := time.Parse(time.RFC3339, captured)
	if !latOK || !lonOK || !accuracyOK || !capturedOK || err != nil {
		return LocationUpdate{}, ErrInvalidRequest
	}
	return LocationUpdate{Point: Point{Latitude: latitude, Longitude: longitude}, AccuracyM: accuracy, CapturedAt: parsed.UTC()}, nil
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (service *Service) activeTaskCountLocked(riderID string) int {
	count := 0
	for _, value := range service.tasks {
		if value.AssignedRiderID == riderID && (value.Status == "ASSIGNED" || value.Status == "PICKED_UP") {
			count++
		}
	}
	return count
}

func (service *Service) createEarningLocked(task *DeliveryTask) {
	if task.AssignedRiderID == "" {
		return
	}
	commission := task.Earning.AmountMinor * service.configuration.CommissionBasisPoints / 10000
	tax := commission * service.configuration.TaxBasisPoints / 10000
	service.sequence++
	now := service.clock().UTC()
	entry := &LedgerEntry{ID: "ledger-entry-" + sequenceID(service.sequence), AccountID: task.AssignedRiderID, ReferenceID: task.ID, Kind: "RIDER_DELIVERY_EARNING", Gross: task.Earning, Commission: Money{AmountMinor: commission, Currency: task.Earning.Currency}, Tax: Money{AmountMinor: tax, Currency: task.Earning.Currency}, Net: Money{AmountMinor: task.Earning.AmountMinor - commission - tax, Currency: task.Earning.Currency}, CalculationVersion: "rider-commission-v1", AvailableAt: now.Add(service.configuration.SettlementCooling), CreatedAt: now, tenantID: task.tenantID, country: task.country}
	service.ledger[entry.ID] = entry
}

func (service *Service) recordAttendanceLocked(actor Actor, sessionID, kind string, point Point, source string) AttendanceEntry {
	service.sequence++
	value := AttendanceEntry{ID: "attendance-" + sequenceID(service.sequence), SubjectID: actor.Subject, SessionID: sessionID, Kind: kind, Point: point, RecordedAt: service.clock().UTC(), Source: source, tenantID: actor.TenantID, country: actor.Country}
	service.attendance[value.ID] = &value
	return value
}

func (service *Service) recordAuditLocked(actor Actor, action, resourceType, resourceID, reason string) {
	service.sequence++
	service.audit = append(service.audit, &AuditEvent{ID: "fulfillment-audit-" + sequenceID(service.sequence), ActorID: actor.Subject, Action: action, ResourceType: resourceType, ResourceID: resourceID, Reason: strings.TrimSpace(reason), CreatedAt: service.clock().UTC(), tenantID: actor.TenantID, country: actor.Country})
}

func validRiderRegistration(value RiderRegistrationRequest) bool {
	if len(strings.TrimSpace(value.FullName)) < 3 || !regexp.MustCompile(`^\*{2,8}[0-9]{4}$`).MatchString(value.PhoneMasked) || !regexp.MustCompile(`^[A-Z_]{3,24}$`).MatchString(value.VehicleType) || len(strings.TrimSpace(value.VehicleNumber)) < 5 || !regexp.MustCompile(`^bankref_[A-Za-z0-9_-]{8,100}$`).MatchString(value.BankReference) || len(value.Documents) < 2 || len(value.Zones) == 0 {
		return false
	}
	for _, document := range value.Documents {
		if !regexp.MustCompile(`^[A-Z_]{2,40}$`).MatchString(document.Kind) || !safeID(document.AssetID) {
			return false
		}
	}
	for _, zone := range value.Zones {
		if !safeID(zone) {
			return false
		}
	}
	return true
}

func validTaskSeed(value TaskSeed) bool {
	return safeID(value.ID) && safeID(value.OrderID) && safeID(value.OrderType) && safeID(value.RegionID) && safeID(value.TerritoryID) && safeID(value.ZoneID) && validPoint(value.Pickup.Point) && validPoint(value.Dropoff.Point) && safeID(value.Pickup.AddressToken) && safeID(value.Dropoff.AddressToken) && value.DistanceMeters > 0 && value.Earning.AmountMinor > 0 && regexp.MustCompile(`^[A-Z]{3}$`).MatchString(value.Earning.Currency) && regexp.MustCompile(`^[0-9]{6}$`).MatchString(value.DeliveryOTP) && safeID(value.CustomerID) && safeID(value.CounterpartyID)
}

func validTerritory(value Territory) bool {
	return safeID(value.ID) && safeID(value.RegionID) && safeID(value.FranchiseID) && len(strings.TrimSpace(value.Name)) >= 3 && validPoint(value.Center) && value.RadiusKM > 0 && value.RadiusKM <= 500 && len(value.PostalCodes) > 0
}
func validActor(value Actor) bool {
	return safeID(value.TenantID) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(value.Country) && safeID(value.Subject) && len(value.Roles) > 0
}
func validKey(value string) bool { return len(value) >= 16 && len(value) <= 128 && safeID(value) }
func safeID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(value)
}
func validPoint(value Point) bool {
	return value.Latitude >= -90 && value.Latitude <= 90 && value.Longitude >= -180 && value.Longitude <= 180
}
func hasRole(actor Actor, role string) bool {
	for _, candidate := range actor.Roles {
		if strings.TrimSpace(candidate) == role {
			return true
		}
	}
	return false
}
func hasAnyRole(actor Actor, roles ...string) bool {
	for _, role := range roles {
		if hasRole(actor, role) {
			return true
		}
	}
	return false
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func sequenceID(value int64) string { return fmt.Sprintf("%010d", value) }
func idempotencyScope(actor Actor, operation, key string) string {
	return actor.TenantID + ":" + actor.Country + ":" + actor.Subject + ":" + operation + ":" + key
}
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func cloneRiderDocuments(values []RiderDocument) []RiderDocument {
	return append([]RiderDocument(nil), values...)
}
func cloneRider(value RiderProfile) RiderProfile {
	value.Documents = cloneRiderDocuments(value.Documents)
	value.Zones = append([]string(nil), value.Zones...)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}
func cloneTask(value DeliveryTask) DeliveryTask {
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}
func cloneTerritory(value Territory) Territory {
	value.PostalCodes = append([]string(nil), value.PostalCodes...)
	return value
}
func haversineMeters(first, second Point) float64 {
	const radius = 6371000.0
	p1, p2 := first.Latitude*math.Pi/180, second.Latitude*math.Pi/180
	dlat, dlon := (second.Latitude-first.Latitude)*math.Pi/180, (second.Longitude-first.Longitude)*math.Pi/180
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return radius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
func (service *Service) actorOwnsTerritory(franchiseID, territoryID string) bool {
	territory := service.territories[territoryID]
	return territory.ID != "" && territory.FranchiseID == franchiseID
}
func parseInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), float64(int64(typed)) == typed
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
