//go:build integration

package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/commerce"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

type recoveryCatalog struct{}

func (recoveryCatalog) Resolve(_ context.Context, _ commerce.Scope, id string) (commerce.VariantSnapshot, error) {
	return commerce.VariantSnapshot{VariantID: id, ItemID: postgresCheckoutItemOne, VendorID: postgresCheckoutVendorOne, ItemName: "Recovery item", VariantName: "Standard", UnitPrice: commerce.Money{AmountMinor: 10000, Currency: "INR"}, Available: true, Stock: 100, MaxPerOrder: 10}, nil
}

type recoveryFixture struct {
	pool         *pgxpool.Pool
	now          time.Time
	service      *Service
	scope        Scope
	quote        Quote
	config       Configuration
	dependencies Dependencies
	stock        *inventory.PostgresService
	wallets      *wallet.PostgresService
	payments     *payment.PostgresService
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	url := os.Getenv("CHECKOUT_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("CHECKOUT_DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schemas := []string{"commerce", "inventory", "wallet", "payment", "ordering"}
	for _, schema := range schemas {
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, schema := range schemas {
			_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		}
		pool.Close()
	})
	for _, path := range []string{"../../migrations/platform/000001_service_roles.up.sql", "../../migrations/platform/000002_commerce_roles.up.sql", "../../migrations/platform/000003_transaction_roles.up.sql", "../../migrations/platform/000008_admin_roles.up.sql"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, schema := range schemas {
		paths, err := filepath.Glob("../../migrations/" + schema + "/*.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, string(data)); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
	}
	f := &recoveryFixture{pool: pool, now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC), scope: Scope{TenantID: postgresCheckoutTenantOne, Country: "IN", CustomerID: postgresCheckoutCustomerOne}}
	clock := func() time.Time { return f.now }
	f.stock, err = inventory.NewPostgresService(pool, clock)
	if err != nil {
		t.Fatal(err)
	}
	f.wallets, err = wallet.NewPostgresService(pool, clock, wallet.RewardPolicy{}, wallet.Program{ReferralBaseURL: "https://planext4u.example/referral", RefillOffers: []wallet.RefillOffer{{ID: "refill-recovery-500", Country: "IN", Points: 500, Price: wallet.Money{AmountMinor: 5000, Currency: "INR"}, PaymentMethods: []string{"RAZORPAY"}, ExpiresAfter: 24 * time.Hour}}})
	if err != nil {
		t.Fatal(err)
	}
	f.payments, err = payment.NewPostgresService(pool, clock, map[payment.Method][]byte{payment.MethodRazorpay: []byte("synthetic-provider-secret-32-bytes-minimum")}, map[payment.Method]payment.ProviderInitializer{payment.MethodRazorpay: checkoutPaymentProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	orders, err := order.NewPostgresService(pool, clock)
	if err != nil {
		t.Fatal(err)
	}
	cart, err := commerce.NewPostgresService(pool, recoveryCatalog{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := NewStaticCommercialTermsResolver([]CommercialTermsPolicy{{TenantID: f.scope.TenantID, Country: "IN", Version: "recovery-commercial-v1", DefaultVendorTier: "BASIC", DefaultCommissionBasisPoints: 1000, DefaultWalletBasisPoints: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	f.dependencies = Dependencies{Cart: cart, Inventory: f.stock, Wallet: f.wallets, Payment: f.payments, Orders: orders, CommercialTerms: terms}
	store, err := NewPostgresStore(pool, clock)
	if err != nil {
		t.Fatal(err)
	}
	address, _, err := store.CreateAddress(f.scope, "recovery-address-create-001", "recovery-address", Address{Label: "Home", Line1: "10 Main Road", PostalCode: "600001", Locality: "Chennai", Serviceable: true, Default: true})
	if err != nil {
		t.Fatal(err)
	}
	f.config = Configuration{Addresses: []Address{address}, Slots: []DeliverySlot{{ID: "slot-recovery-001", Country: "IN", WindowStart: f.now.Add(24 * time.Hour), WindowEnd: f.now.Add(26 * time.Hour), Capacity: 100, Fee: Money{Currency: "INR"}}}, Policies: []PricingPolicy{{Version: "recovery-pricing-v1", Country: "IN", ProductTaxTreatment: ProductTaxInclusive, WalletMode: WalletHybrid, WalletPointValueMinor: 1, QuoteTTL: 10 * time.Minute, ReservationTTL: 20 * time.Minute}}}
	if _, err := pool.Exec(ctx, `INSERT INTO inventory.stock(tenant_id,country,variant_id,available_quantity,revision,updated_at) VALUES($1,'IN',$2,100,1,$3)`, f.scope.TenantID, postgresCheckoutVariantOne, f.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.wallets.Credit(wallet.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID}, "recovery-wallet-seed-0001", "TEST", "recovery-seed", 1000, f.now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, _, err := cart.Change(ctx, commerce.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID}, "recovery-cart-create-0001", 0, postgresCheckoutVariantOne, 2)
	if err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.quote, _, err = f.service.Quote(ctx, f.scope, "recovery-quote-create-001", QuoteRequest{CartRevision: c.Revision, AddressID: address.ID, DeliverySlot: "slot-recovery-001", WalletPoints: 400})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *recoveryFixture) restart(t *testing.T) {
	t.Helper()
	store, _ := NewPostgresStore(f.pool, func() time.Time { return f.now })
	var err error
	f.service, err = NewPersistentService(f.dependencies, f.config, store, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCheckoutRecoversEveryCommittedEffectPastQuoteExpiry(t *testing.T) {
	for _, stage := range []string{"stock-reserved", "wallet-debited", "payment-created", "order-created", "process-linked", "stock-committed"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryFixture(t)
			method := payment.MethodRazorpay
			if stage == "stock-committed" {
				method = payment.MethodCOD
			}
			interruption := errors.New("simulated process termination")
			f.service.afterEffect = func(current string) error {
				if current == stage {
					return interruption
				}
				return nil
			}
			if _, _, err := f.service.Place(context.Background(), f.scope, "recovery-place-command-001", f.quote.ID, method); !errors.Is(err, interruption) {
				t.Fatalf("did not interrupt %s: %v", stage, err)
			}
			f.now = f.now.Add(11 * time.Minute)
			f.restart(t)
			if count, err := f.service.RecoverPlacements(context.Background(), 100); err != nil || count != 1 {
				t.Fatalf("recover count=%d err=%v", count, err)
			}
			result, replay, err := f.service.Place(context.Background(), f.scope, "recovery-place-command-001", f.quote.ID, method)
			if err != nil || !replay {
				t.Fatalf("replay=%t err=%v", replay, err)
			}
			for table, want := range map[string]int{"inventory.reservations": 1, "payment.payments": 1, "ordering.orders": 1, "commerce.checkout_processes": 1} {
				var count int
				if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
			account, err := f.wallets.Account(wallet.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID})
			if err != nil || account.Balance != 600 || !wallet.Verify(account) {
				t.Fatalf("wallet=%#v err=%v", account, err)
			}
			available, err := f.stock.Available(inventory.Scope{TenantID: f.scope.TenantID, Country: "IN"}, postgresCheckoutVariantOne)
			if err != nil || available != 98 {
				t.Fatalf("stock=%d err=%v", available, err)
			}
			if result.Order.ID == "" {
				t.Fatal("missing recovered order")
			}
		})
	}
}

func TestPostgresCheckoutExpiredInterruptedDebitIsCompensated(t *testing.T) {
	f := newRecoveryFixture(t)
	f.service.afterEffect = func(stage string) error {
		if stage == "wallet-debited" {
			return errors.New("crash")
		}
		return nil
	}
	if _, _, err := f.service.Place(context.Background(), f.scope, "recovery-place-command-001", f.quote.ID, payment.MethodRazorpay); err == nil || err.Error() != "crash" {
		t.Fatalf("missing debit interruption: %v", err)
	}
	f.now = f.now.Add(21 * time.Minute)
	f.restart(t)
	if count, err := f.service.RecoverPlacements(context.Background(), 100); err != nil || count != 1 {
		t.Fatalf("recovered=%d err=%v", count, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, _, err := f.service.Place(context.Background(), f.scope, "recovery-place-command-001", f.quote.ID, payment.MethodRazorpay); !errors.Is(err, ErrQuoteExpired) {
			t.Fatalf("expired replay: %v", err)
		}
	}
	account, err := f.wallets.Account(wallet.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID})
	if err != nil || account.Balance != 1000 || !wallet.Verify(account) {
		t.Fatalf("wallet=%#v err=%v", account, err)
	}
	stock, err := f.stock.Available(inventory.Scope{TenantID: f.scope.TenantID, Country: "IN"}, postgresCheckoutVariantOne)
	if err != nil || stock != 100 {
		t.Fatalf("stock=%d err=%v", stock, err)
	}
}

func TestPostgresCheckoutLateCaptureRefundsExpiredReservation(t *testing.T) {
	f := newRecoveryFixture(t)
	result, _, err := f.service.Place(context.Background(), f.scope, "recovery-place-command-001", f.quote.ID, payment.MethodRazorpay)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(21 * time.Minute)
	event := payment.ProviderEvent{EventID: "recovery-late-capture-001", PaymentID: result.Payment.ID, ProviderReference: result.Payment.ProviderReference, Status: payment.StatusCaptured, AmountMinor: result.Payment.Amount.AmountMinor, Currency: "INR"}
	body, _ := json.Marshal(event)
	captured, _, err := f.payments.HandleWebhook(payment.MethodRazorpay, payment.Sign([]byte("synthetic-provider-secret-32-bytes-minimum"), body), body)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ApplyPayment(context.Background(), "recovery-late-capture-001", captured); err != nil {
		t.Fatal(err)
	}
	current, err := f.dependencies.Orders.Get(orderScope(f.scope), result.Order.ID)
	if err != nil || current.Status != order.StatusCancelled {
		t.Fatalf("order=%#v err=%v", current, err)
	}
	p, err := f.payments.Get(payment.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID}, captured.ID)
	if err != nil || p.Status != payment.StatusRefundSubmitted {
		t.Fatalf("payment=%#v err=%v", p, err)
	}
	if err := f.service.RecoverPaymentFinalizations(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var balance int64
	if err := f.pool.QueryRow(context.Background(), `SELECT balance_points FROM wallet.accounts WHERE customer_identity_id=$1`, f.scope.CustomerID).Scan(&balance); err != nil || balance != 1000 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
}

func TestPostgresCheckoutCompensationSurvivesEveryCommittedCleanupEffect(t *testing.T) {
	for _, stage := range []string{"checkout-payment-compensated", "checkout-wallet-reversed", "checkout-stock-restored", "checkout-order-cancelled"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.service.afterEffect = func(current string) error {
				if current == "payment-created" {
					return errors.New("initial crash")
				}
				return nil
			}
			if _, _, err := f.service.Place(context.Background(), f.scope, "recovery-place-command-001", f.quote.ID, payment.MethodRazorpay); err == nil {
				t.Fatal("missing initial interruption")
			}
			f.now = f.now.Add(21 * time.Minute)
			f.restart(t)
			interruption := errors.New("compensation crash")
			f.service.afterEffect = func(current string) error {
				if current == stage {
					return interruption
				}
				return nil
			}
			if _, err := f.service.RecoverPlacements(context.Background(), 100); !errors.Is(err, interruption) {
				t.Fatalf("missing %s interruption: %v", stage, err)
			}
			f.restart(t)
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := f.service.RecoverPlacements(context.Background(), 100); err != nil {
					t.Fatal(err)
				}
			}
			account, err := f.wallets.Account(wallet.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID})
			if err != nil || account.Balance != 1000 || !wallet.Verify(account) {
				t.Fatalf("wallet=%#v err=%v", account, err)
			}
			available, err := f.stock.Available(inventory.Scope{TenantID: f.scope.TenantID, Country: "IN"}, postgresCheckoutVariantOne)
			if err != nil || available != 100 {
				t.Fatalf("stock=%d err=%v", available, err)
			}
			orders, err := f.dependencies.Orders.List(orderScope(f.scope))
			if err != nil || len(orders) != 1 || orders[0].Status != order.StatusCancelled {
				t.Fatalf("orders=%#v err=%v", orders, err)
			}
		})
	}
}

func TestPostgresRefillJournalRecoversPaymentAndCreditsWithStableExpiry(t *testing.T) {
	for _, stage := range []string{"refill-payment-created", "refill-process-linked"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryFixture(t)
			interruption := errors.New("refill interrupted")
			f.service.afterEffect = func(current string) error {
				if current == stage {
					return interruption
				}
				return nil
			}
			key := strings.Repeat("r", 128)
			if _, _, err := f.service.CreateWalletRefill(context.Background(), f.scope, key, "refill-recovery-500", payment.MethodRazorpay); !errors.Is(err, interruption) {
				t.Fatalf("missing refill interruption: %v", err)
			}
			f.now = f.now.Add(time.Minute)
			f.restart(t)
			if count, err := f.service.RecoverPlacements(context.Background(), 100); err != nil || count != 1 {
				t.Fatalf("recovered=%d err=%v", count, err)
			}
			result, replay, err := f.service.CreateWalletRefill(context.Background(), f.scope, key, "refill-recovery-500", payment.MethodRazorpay)
			if err != nil || !replay {
				t.Fatalf("replay=%t err=%v", replay, err)
			}
			event := payment.ProviderEvent{EventID: "refill-recovery-capture-001", PaymentID: result.Payment.ID, ProviderReference: result.Payment.ProviderReference, Status: payment.StatusCaptured, AmountMinor: 5000, Currency: "INR"}
			body, _ := json.Marshal(event)
			if _, _, err := f.payments.HandleWebhook(payment.MethodRazorpay, payment.Sign([]byte("synthetic-provider-secret-32-bytes-minimum"), body), body); err != nil {
				t.Fatal(err)
			}
			creditInterrupted := errors.New("credit committed before interruption")
			f.service.afterEffect = func(current string) error {
				if current == "refill-credited" {
					return creditInterrupted
				}
				return nil
			}
			if err := f.service.RecoverPaymentFinalizations(context.Background(), 100); !errors.Is(err, creditInterrupted) {
				t.Fatalf("missing committed credit interruption: %v", err)
			}
			f.restart(t)
			f.now = f.now.Add(3 * time.Minute)
			if _, _, err := f.service.FinalizeProviderWalletRefill(result.Payment.ID); err != nil {
				t.Fatalf("credit retry expiry changed: %v", err)
			}
			account, err := f.wallets.Account(wallet.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID})
			if err != nil || account.Balance != 1500 || !wallet.Verify(account) {
				t.Fatalf("wallet=%#v err=%v", account, err)
			}
			var payments int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM payment.payments`).Scan(&payments); err != nil || payments != 1 {
				t.Fatalf("payments=%d err=%v", payments, err)
			}
		})
	}
}

func TestPostgresCheckoutAcceptsMaximumLengthCommandKeyWithoutLosingEffects(t *testing.T) {
	f := newRecoveryFixture(t)
	key := strings.Repeat("x", 128)
	for attempt := 0; attempt < 2; attempt++ {
		if _, _, err := f.service.Place(context.Background(), f.scope, key, f.quote.ID, payment.MethodRazorpay); err != nil {
			t.Fatal(err)
		}
	}
	account, err := f.wallets.Account(wallet.Scope{TenantID: f.scope.TenantID, Country: "IN", CustomerID: f.scope.CustomerID})
	if err != nil || account.Balance != 600 {
		t.Fatalf("balance=%d err=%v", account.Balance, err)
	}
}
