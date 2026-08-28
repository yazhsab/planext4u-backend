package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/commerce"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

func TestBECheckout001QuotePlaceAndSignedPaymentCompletion(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	quote, replayed, err := fixture.service.Quote(context.Background(), fixture.scope, "idem-checkout-quote-0001", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001", PromotionCode: "LOCAL10", WalletPoints: 400})
	if err != nil || replayed || quote.Subtotal.AmountMinor != 20000 || quote.Discount.AmountMinor != 2000 || quote.Tax.AmountMinor != 900 || quote.Fees.AmountMinor != 1500 || quote.WalletApplied.AmountMinor != 400 || quote.Total.AmountMinor != 20000 {
		t.Fatalf("quote = %#v replayed=%v err=%v", quote, replayed, err)
	}
	result, replayed, err := fixture.service.Place(context.Background(), fixture.scope, "idem-checkout-place-0001", quote.ID, payment.MethodRazorpay)
	if err != nil || replayed || result.Order.Status != order.StatusPendingPayment || result.Reservation.State != inventory.StateReserved || result.Payment.Status != payment.StatusProviderOrderCreated || result.WalletDebit == nil {
		t.Fatalf("place = %#v replayed=%v err=%v", result, replayed, err)
	}
	replayedResult, replayed, err := fixture.service.Place(context.Background(), fixture.scope, "idem-checkout-place-0001", quote.ID, payment.MethodRazorpay)
	if err != nil || !replayed || replayedResult.Order.ID != result.Order.ID {
		t.Fatalf("place replay = %#v replayed=%v err=%v", replayedResult, replayed, err)
	}
	body, _ := json.Marshal(payment.ProviderEvent{EventID: "event-checkout-captured-001", PaymentID: result.Payment.ID, ProviderReference: result.Payment.ProviderReference, Status: payment.StatusCaptured, AmountMinor: quote.Total.AmountMinor, Currency: quote.Total.Currency})
	if _, _, err := fixture.payment.HandleWebhook(payment.MethodRazorpay, payment.Sign(fixture.secret, body), body); err != nil {
		t.Fatal(err)
	}
	placed, replayed, err := fixture.service.FinalizeCapturedPayment(fixture.scope, "idem-checkout-finalize-0001", result.Payment.ID)
	if err != nil || replayed || placed.Status != order.StatusPlaced {
		t.Fatalf("finalize = %#v replayed=%v err=%v", placed, replayed, err)
	}
	if available, _ := fixture.inventory.Available(inventory.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country}, "variant-local-001"); available != 8 {
		t.Fatalf("committed stock = %d", available)
	}
	account, _ := fixture.wallet.Account(wallet.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country, CustomerID: fixture.scope.CustomerID})
	if account.Balance != 600 || !wallet.Verify(account) {
		t.Fatalf("wallet = %#v", account)
	}
}

func TestBECheckout002StaleQuoteAndScopeIsolationDoNotReserve(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	quote, _, _ := fixture.service.Quote(context.Background(), fixture.scope, "idem-checkout-quote-0002", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001"})
	other := fixture.scope
	other.CustomerID = "customer-synthetic-002"
	if _, _, err := fixture.service.Place(context.Background(), other, "idem-checkout-place-0002", quote.ID, payment.MethodRazorpay); !errors.Is(err, ErrQuoteNotFound) {
		t.Fatalf("cross-customer placement = %v", err)
	}
	fixture.provider.setPrice(12000)
	if _, _, err := fixture.service.Place(context.Background(), fixture.scope, "idem-checkout-place-0003", quote.ID, payment.MethodRazorpay); !errors.Is(err, ErrQuoteStale) {
		t.Fatalf("stale quote placement = %v", err)
	}
	if available, _ := fixture.inventory.Available(inventory.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country}, "variant-local-001"); available != 10 {
		t.Fatalf("stock changed on rejected placement = %d", available)
	}
}

func TestBECheckout003PointsOnlyPolicyAndCODCommit(t *testing.T) {
	t.Parallel()
	pointsOnly := newCheckoutFixture(t, WalletPointsOnly)
	if _, _, err := pointsOnly.service.Quote(context.Background(), pointsOnly.scope, "idem-checkout-points-0001", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001", WalletPoints: 1000}); !errors.Is(err, ErrPaymentMethod) {
		t.Fatalf("underfunded points-only quote = %v", err)
	}
	hybrid := newCheckoutFixture(t, WalletHybrid)
	quote, _, _ := hybrid.service.Quote(context.Background(), hybrid.scope, "idem-checkout-cod-quote-0001", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001"})
	result, _, err := hybrid.service.Place(context.Background(), hybrid.scope, "idem-checkout-cod-place-0001", quote.ID, payment.MethodCOD)
	if err != nil || result.Order.Status != order.StatusPlaced || result.Reservation.State != inventory.StateCommitted {
		t.Fatalf("COD placement = %#v err=%v", result, err)
	}
}

func TestBECheckout004AddressBookIsScopedServiceableAndIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	fixture.service.config.PostalZones = []PostalZone{{Country: "IN", PostalCode: "641001", Locality: "Coimbatore"}}
	input := AddressInput{Label: "Work", Line1: "42 Commerce Road", PostalCode: "641001", Locality: "Coimbatore"}
	created, replayed, err := fixture.service.CreateAddress(fixture.scope, "idem-address-create-0001", input)
	if err != nil || replayed || !created.Serviceable || created.Revision != 1 {
		t.Fatalf("create address = %#v replayed=%v err=%v", created, replayed, err)
	}
	replay, replayed, err := fixture.service.CreateAddress(fixture.scope, "idem-address-create-0001", input)
	if err != nil || !replayed || replay.ID != created.ID {
		t.Fatalf("address replay = %#v replayed=%v err=%v", replay, replayed, err)
	}
	if _, _, err = fixture.service.CreateAddress(fixture.scope, "idem-address-create-0001", AddressInput{Label: "Other", Line1: "1 Other Road", PostalCode: "641001", Locality: "Coimbatore"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting create replay = %v", err)
	}
	updated, _, err := fixture.service.UpdateAddress(fixture.scope, "idem-address-update-0001", created.ID, created.Revision, AddressInput{Label: "Work", Line1: "42 Commerce Road", PostalCode: "999999", Locality: "Outside", Default: true})
	if err != nil || updated.Serviceable || !updated.Default || updated.Revision != 2 {
		t.Fatalf("update address = %#v err=%v", updated, err)
	}
	if _, _, err = fixture.service.UpdateAddress(fixture.scope, "idem-address-update-0002", created.ID, created.Revision, input); !errors.Is(err, ErrAddressConflict) {
		t.Fatalf("stale address update = %v", err)
	}
	other := fixture.scope
	other.CustomerID = "customer-synthetic-002"
	if _, _, err = fixture.service.UpdateAddress(other, "idem-address-update-0003", created.ID, updated.Revision, input); !errors.Is(err, ErrAddressNotFound) {
		t.Fatalf("cross-customer address update = %v", err)
	}
	if replayed, err = fixture.service.DeleteAddress(fixture.scope, "idem-address-delete-0001", created.ID, updated.Revision); err != nil || replayed {
		t.Fatalf("delete address replayed=%v err=%v", replayed, err)
	}
	if replayed, err = fixture.service.DeleteAddress(fixture.scope, "idem-address-delete-0001", created.ID, updated.Revision); err != nil || !replayed {
		t.Fatalf("delete replay replayed=%v err=%v", replayed, err)
	}
}

type checkoutProvider struct {
	mu    sync.Mutex
	price int64
}

func (provider *checkoutProvider) Resolve(_ context.Context, _ commerce.Scope, variantID string) (commerce.VariantSnapshot, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if variantID != "variant-local-001" {
		return commerce.VariantSnapshot{}, commerce.ErrVariantNotFound
	}
	return commerce.VariantSnapshot{VariantID: variantID, ItemID: "item-local-001", VendorID: "vendor-local-001", ItemName: "Local grocery", VariantName: "Standard", UnitPrice: commerce.Money{AmountMinor: provider.price, Currency: "INR"}, Available: true, Stock: 10, MaxPerOrder: 5}, nil
}

func (provider *checkoutProvider) setPrice(value int64) {
	provider.mu.Lock()
	provider.price = value
	provider.mu.Unlock()
}

type checkoutFixture struct {
	service   *Service
	scope     Scope
	provider  *checkoutProvider
	inventory *inventory.Service
	wallet    *wallet.Service
	payment   *payment.Service
	orders    *order.Service
	secret    []byte
}

type checkoutPaymentProvider struct{}

func (checkoutPaymentProvider) Initialize(_ context.Context, input payment.ProviderInitialization) (payment.ProviderSession, error) {
	return payment.ProviderSession{
		ProviderReference: "order-provider-" + input.PaymentID,
		ClientHandoff:     payment.ClientHandoff{Type: "RAZORPAY_CHECKOUT", PublicKey: "rzp_test_checkout", ProviderOrderID: "order-provider-" + input.PaymentID},
	}, nil
}

func (checkoutPaymentProvider) Refund(_ context.Context, input payment.ProviderRefundRequest) (payment.ProviderRefundSubmission, error) {
	return payment.ProviderRefundSubmission{ProviderRefundReference: "refund-provider-" + input.PaymentID, Status: payment.StatusRefundSubmitted}, nil
}

func newCheckoutFixture(t *testing.T, mode WalletMode) checkoutFixture {
	t.Helper()
	clock := func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }
	scope := Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
	provider := &checkoutProvider{price: 10000}
	cart, _ := commerce.NewService(provider, clock)
	_, _, err := cart.Change(context.Background(), commerce.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, "idem-cart-checkout-0001", 0, "variant-local-001", 2)
	if err != nil {
		t.Fatal(err)
	}
	inventoryService, _ := inventory.NewService([]inventory.SeedStock{{Scope: inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}, VariantID: "variant-local-001", Quantity: 10}}, clock)
	walletService, _ := wallet.NewServiceWithProgram(clock, wallet.RewardPolicy{DailyDeviceCap: 100, Cooldown: time.Minute, ReferralSenderPoints: 500, ReferralRecipientPoints: 250, ReferralExpiry: 365 * 24 * time.Hour}, wallet.Program{
		ReferralBaseURL: "https://planext4u.net/referral",
		RefillOffers:    []wallet.RefillOffer{{ID: "refill-test-5000", Country: "IN", Points: 5000, BonusPoints: 250, Price: wallet.Money{AmountMinor: 5000, Currency: "INR"}, PaymentMethods: []string{"RAZORPAY"}, ExpiresAfter: 365 * 24 * time.Hour}},
	})
	_, _, _ = walletService.Credit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, "idem-checkout-wallet-credit", "SYNTHETIC_BALANCE", "seed-checkout-001", 1000, clock().Add(365*24*time.Hour))
	secret := []byte("synthetic-provider-secret-32-bytes-minimum")
	paymentService, _ := payment.NewServiceWithProviders(clock, map[payment.Method][]byte{payment.MethodRazorpay: secret, payment.MethodPaystack: secret}, map[payment.Method]payment.ProviderInitializer{payment.MethodRazorpay: checkoutPaymentProvider{}})
	orderService, _ := order.NewService(clock)
	service, err := NewService(Dependencies{Cart: cart, Inventory: inventoryService, Wallet: walletService, Payment: paymentService, Orders: orderService}, Configuration{
		Addresses:  []Address{{ID: "address-home-001", Label: "Home", Line1: "12 Market Street", PostalCode: "641001", Locality: "Coimbatore", TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}},
		Slots:      []DeliverySlot{{ID: "slot-standard-001", Country: "IN", WindowStart: clock().Add(24 * time.Hour), WindowEnd: clock().Add(28 * time.Hour), Fee: Money{AmountMinor: 1000, Currency: "INR"}, Capacity: 20}},
		Promotions: []Promotion{{Code: "LOCAL10", Country: "IN", MinimumSubtotal: 10000, DiscountBasisPts: 1000, MaximumDiscount: 5000, StartsAt: clock().Add(-time.Hour), EndsAt: clock().Add(24 * time.Hour)}},
		Policies:   []PricingPolicy{{Version: "pricing-2026-01", Country: "IN", TaxBasisPoints: 500, PlatformFeeMinor: 500, WalletPointValueMinor: 1, WalletMode: mode, QuoteTTL: 10 * time.Minute, ReservationTTL: 15 * time.Minute}},
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return checkoutFixture{service: service, scope: scope, provider: provider, inventory: inventoryService, wallet: walletService, payment: paymentService, orders: orderService, secret: secret}
}

func TestApprovedPartialReturnSplitsTenderRestocksAndFinalizesFromWebhook(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	quote, _, _ := fixture.service.Quote(context.Background(), fixture.scope, "idem-return-quote-0001", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001", PromotionCode: "LOCAL10", WalletPoints: 400})
	placed, _, err := fixture.service.Place(context.Background(), fixture.scope, "idem-return-place-0001", quote.ID, payment.MethodRazorpay)
	if err != nil {
		t.Fatal(err)
	}
	captureBody, _ := json.Marshal(payment.ProviderEvent{EventID: "event-return-captured-001", PaymentID: placed.Payment.ID, ProviderReference: placed.Payment.ProviderReference, Status: payment.StatusCaptured, AmountMinor: placed.Payment.Amount.AmountMinor, Currency: placed.Payment.Amount.Currency})
	if _, _, err = fixture.payment.HandleWebhook(payment.MethodRazorpay, payment.Sign(fixture.secret, captureBody), captureBody); err != nil {
		t.Fatal(err)
	}
	value, _, err := fixture.service.FinalizeCapturedPayment(fixture.scope, "idem-return-finalize-0001", placed.Payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		status order.Status
		actor  string
	}{{order.StatusAccepted, "VENDOR"}, {order.StatusPacking, "VENDOR"}, {order.StatusReadyForHandover, "VENDOR"}, {order.StatusAssigned, "PLATFORM"}, {order.StatusPickedUp, "RIDER"}, {order.StatusOutForDelivery, "RIDER"}}
	for index, step := range steps {
		value, _, err = fixture.orders.Transition(orderScope(fixture.scope), "idem-checkout-return-step-000"+string(rune('1'+index)), value.ID, value.Revision, step.status, step.actor, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	value, _, _ = fixture.orders.RecordPOD(orderScope(fixture.scope), "idem-checkout-return-pod-0001", value.ID, value.Revision, order.Proof{PolicyVersion: "pod-v1", OTPVerified: true})
	value, _, _ = fixture.orders.Transition(orderScope(fixture.scope), "idem-checkout-return-confirm-0001", value.ID, value.Revision, order.StatusCompleted, "CUSTOMER", "")
	requested, _, err := fixture.orders.RequestReturn(orderScope(fixture.scope), "idem-checkout-return-request-0001", value.ID, value.Revision, []order.ReturnLine{{VariantID: "variant-local-001", Quantity: 1}}, "One item arrived damaged")
	if err != nil {
		t.Fatal(err)
	}
	result, replayed, err := fixture.service.ApproveReturn(context.Background(), fixture.scope, "idem-checkout-return-approve-0001", value.ID, requested.Revision, "Damage evidence verified")
	if err != nil || replayed || !result.Pending || result.Order.Status != order.StatusReturned || result.Payment == nil || result.Payment.RefundAmount == nil || result.Payment.RefundAmount.AmountMinor != 9250 || result.WalletRefund == nil || result.WalletRefund.DeltaPoints != 200 {
		t.Fatalf("return result=%#v replayed=%v err=%v", result, replayed, err)
	}
	if available, _ := fixture.inventory.Available(inventory.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country}, "variant-local-001"); available != 9 {
		t.Fatalf("restocked inventory=%d", available)
	}
	account, _ := fixture.wallet.Account(wallet.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country, CustomerID: fixture.scope.CustomerID})
	if account.Balance != 800 || !wallet.Verify(account) {
		t.Fatalf("wallet after partial refund=%#v", account)
	}
	if replayResult, wasReplay, retryErr := fixture.service.ApproveReturn(context.Background(), fixture.scope, "idem-checkout-return-approve-0001", value.ID, requested.Revision, "Damage evidence verified"); retryErr != nil || !wasReplay || replayResult.Order.Status != order.StatusReturned {
		t.Fatalf("return replay=%#v replayed=%v err=%v", replayResult, wasReplay, retryErr)
	}
	refundBody, _ := json.Marshal(payment.ProviderEvent{EventID: "event-return-refunded-001", PaymentID: placed.Payment.ID, ProviderReference: placed.Payment.ProviderReference, Status: payment.StatusRefunded, AmountMinor: 9250, Currency: "INR"})
	if _, _, err = fixture.payment.HandleWebhook(payment.MethodRazorpay, payment.Sign(fixture.secret, refundBody), refundBody); err != nil {
		t.Fatal(err)
	}
	refunded, replayed, err := fixture.service.FinalizeProviderRefund("idem-return-webhook-0001", placed.Payment.ID)
	if err != nil || replayed || refunded.Status != order.StatusRefunded || refunded.Return == nil || refunded.Return.RefundAmount.AmountMinor != 9450 {
		t.Fatalf("final refund=%#v replayed=%v err=%v", refunded, replayed, err)
	}
	if again, wasReplay, retryErr := fixture.service.FinalizeProviderRefund("idem-return-webhook-0001", placed.Payment.ID); retryErr != nil || !wasReplay || again.Status != order.StatusRefunded {
		t.Fatalf("refund replay=%#v replayed=%v err=%v", again, wasReplay, retryErr)
	}
}

func TestWalletRefillCreditsOnlyAfterCapturedProviderWebhook(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	experience, err := fixture.service.WalletExperience(fixture.scope)
	if err != nil || len(experience.Refills) != 1 || experience.Refills[0].BonusPoints != 250 {
		t.Fatalf("experience=%#v err=%v", experience, err)
	}
	result, replayed, err := fixture.service.CreateWalletRefill(context.Background(), fixture.scope, "idem-wallet-refill-0001", experience.Refills[0].ID, payment.MethodRazorpay)
	if err != nil || replayed || result.Payment.Status != payment.StatusProviderOrderCreated || result.Credit != nil {
		t.Fatalf("refill=%#v replayed=%v err=%v", result, replayed, err)
	}
	account, _ := fixture.wallet.Account(wallet.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country, CustomerID: fixture.scope.CustomerID})
	if account.Balance != 1000 {
		t.Fatalf("credited before capture=%d", account.Balance)
	}
	captureBody, _ := json.Marshal(payment.ProviderEvent{EventID: "event-refill-captured-001", PaymentID: result.Payment.ID, ProviderReference: result.Payment.ProviderReference, Status: payment.StatusCaptured, AmountMinor: result.Payment.Amount.AmountMinor, Currency: result.Payment.Amount.Currency})
	if _, _, err = fixture.payment.HandleWebhook(payment.MethodRazorpay, payment.Sign(fixture.secret, captureBody), captureBody); err != nil {
		t.Fatal(err)
	}
	credit, replayed, err := fixture.service.FinalizeProviderWalletRefill(result.Payment.ID)
	if err != nil || replayed || credit.DeltaPoints != 5250 {
		t.Fatalf("credit=%#v replayed=%v err=%v", credit, replayed, err)
	}
	if _, replayed, err = fixture.service.FinalizeProviderWalletRefill(result.Payment.ID); err != nil || !replayed {
		t.Fatalf("credit replayed=%v err=%v", replayed, err)
	}
	account, _ = fixture.wallet.Account(wallet.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country, CustomerID: fixture.scope.CustomerID})
	if account.Balance != 6250 || !wallet.Verify(account) {
		t.Fatalf("refill account=%#v", account)
	}
}

func TestPendingPaymentCancellationReleasesStockAndReversesWallet(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	quote, _, _ := fixture.service.Quote(context.Background(), fixture.scope, "idem-cancel-quote-0001", QuoteRequest{CartRevision: 1, AddressID: "address-home-001", DeliverySlot: "slot-standard-001", WalletPoints: 400})
	placed, _, err := fixture.service.Place(context.Background(), fixture.scope, "idem-cancel-place-0001", quote.ID, payment.MethodRazorpay)
	if err != nil {
		t.Fatal(err)
	}
	requested, _, err := fixture.orders.Transition(orderScope(fixture.scope), "idem-cancel-request-0001", placed.Order.ID, placed.Order.Revision, order.StatusCancelRequested, "CUSTOMER", "Ordered by mistake")
	if err != nil {
		t.Fatal(err)
	}
	result, replayed, err := fixture.service.FinalizeCancellation(context.Background(), fixture.scope, "idem-cancel-finalize-0001", requested.ID, requested.Revision, "Cancellation approved before capture")
	if err != nil || replayed || result.Order.Status != order.StatusCancelled || result.Payment.Status != payment.StatusCancelled || result.RefundPending || result.WalletRefund == nil || result.WalletRefund.DeltaPoints != 400 || result.Inventory.State != inventory.StateReleased {
		t.Fatalf("cancel=%#v replayed=%v err=%v", result, replayed, err)
	}
	if available, _ := fixture.inventory.Available(inventory.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country}, "variant-local-001"); available != 10 {
		t.Fatalf("released stock=%d", available)
	}
	account, _ := fixture.wallet.Account(wallet.Scope{TenantID: fixture.scope.TenantID, Country: fixture.scope.Country, CustomerID: fixture.scope.CustomerID})
	if account.Balance != 1000 || !wallet.Verify(account) {
		t.Fatalf("wallet after cancellation=%#v", account)
	}
	if again, wasReplay, retryErr := fixture.service.FinalizeCancellation(context.Background(), fixture.scope, "idem-cancel-finalize-0001", requested.ID, requested.Revision, "Cancellation approved before capture"); retryErr != nil || !wasReplay || again.Order.Status != order.StatusCancelled {
		t.Fatalf("cancel replay=%#v replayed=%v err=%v", again, wasReplay, retryErr)
	}
}
