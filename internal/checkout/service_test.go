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
	secret    []byte
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
	walletService, _ := wallet.NewService(clock, wallet.RewardPolicy{DailyDeviceCap: 100, Cooldown: time.Minute})
	_, _, _ = walletService.Credit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, "idem-checkout-wallet-credit", "SYNTHETIC_BALANCE", "seed-checkout-001", 1000, clock().Add(365*24*time.Hour))
	secret := []byte("synthetic-provider-secret-32-bytes-minimum")
	paymentService, _ := payment.NewService(clock, map[payment.Method][]byte{payment.MethodRazorpay: secret, payment.MethodPaystack: secret})
	orderService, _ := order.NewService(clock)
	service, err := NewService(Dependencies{Cart: cart, Inventory: inventoryService, Wallet: walletService, Payment: paymentService, Orders: orderService}, Configuration{
		Addresses:  []Address{{ID: "address-home-001", Label: "Home", PostalCode: "641001", Locality: "Coimbatore", TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}},
		Slots:      []DeliverySlot{{ID: "slot-standard-001", Country: "IN", WindowStart: clock().Add(24 * time.Hour), WindowEnd: clock().Add(28 * time.Hour), Fee: Money{AmountMinor: 1000, Currency: "INR"}, Capacity: 20}},
		Promotions: []Promotion{{Code: "LOCAL10", Country: "IN", MinimumSubtotal: 10000, DiscountBasisPts: 1000, MaximumDiscount: 5000, StartsAt: clock().Add(-time.Hour), EndsAt: clock().Add(24 * time.Hour)}},
		Policies:   []PricingPolicy{{Version: "pricing-2026-01", Country: "IN", TaxBasisPoints: 500, PlatformFeeMinor: 500, WalletPointValueMinor: 1, WalletMode: mode, QuoteTTL: 10 * time.Minute, ReservationTTL: 15 * time.Minute}},
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return checkoutFixture{service: service, scope: scope, provider: provider, inventory: inventoryService, wallet: walletService, payment: paymentService, secret: secret}
}
