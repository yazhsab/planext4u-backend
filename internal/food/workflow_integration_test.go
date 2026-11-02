//go:build integration

package food

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pay "github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

type foodProvider struct{ fail bool }

func (p foodProvider) Initialize(_ context.Context, input pay.ProviderInitialization) (pay.ProviderSession, error) {
	if p.fail {
		return pay.ProviderSession{}, pay.ErrProviderUnavailable
	}
	return pay.ProviderSession{ProviderReference: "order-" + input.PaymentID, ClientHandoff: pay.ClientHandoff{Type: "RAZORPAY_CHECKOUT", PublicKey: "rzp_synthetic", ProviderOrderID: "order-" + input.PaymentID}}, nil
}
func (foodProvider) Refund(_ context.Context, input pay.ProviderRefundRequest) (pay.ProviderRefundSubmission, error) {
	return pay.ProviderRefundSubmission{ProviderRefundReference: "refund-" + input.PaymentID, Status: pay.StatusRefundSubmitted}, nil
}

type foodRecoveryFixture struct {
	pool     *pgxpool.Pool
	now      time.Time
	service  *PostgresService
	payments *pay.PostgresService
	wallets  *wallet.PostgresService
	customer Actor
	cart     Cart
}

func newFoodRecoveryFixture(t *testing.T, failedProvider bool) *foodRecoveryFixture {
	t.Helper()
	url := os.Getenv("FOOD_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("FOOD_DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schemas := []string{"food", "payment", "wallet", "fulfillment"}
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
	for _, path := range []string{"../../migrations/platform/000001_service_roles.up.sql", "../../migrations/platform/000003_transaction_roles.up.sql", "../../migrations/platform/000005_phase4_roles.up.sql", "../../migrations/platform/000008_admin_roles.up.sql"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, schema := range schemas {
		paths, _ := filepath.Glob("../../migrations/" + schema + "/*.up.sql")
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
	f := &foodRecoveryFixture{pool: pool, now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC), customer: Actor{TenantID: foodTenantOne, Country: "IN", Subject: foodCustomerOne, Roles: []string{"CUSTOMER"}}}
	seedPostgresFood(t, ctx, pool, f.now)
	clock := func() time.Time { return f.now }
	f.payments, err = pay.NewPostgresService(pool, clock, map[pay.Method][]byte{pay.MethodRazorpay: []byte("food-synthetic-provider-secret-minimum-32")}, map[pay.Method]pay.ProviderInitializer{pay.MethodRazorpay: foodProvider{fail: failedProvider}})
	if err != nil {
		t.Fatal(err)
	}
	f.wallets, err = wallet.NewPostgresService(pool, clock, wallet.RewardPolicy{}, wallet.Program{ReferralBaseURL: "https://planext4u.example/referral"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.wallets.Credit(wallet.Scope{TenantID: foodTenantOne, Country: "IN", CustomerID: foodCustomerOne}, "food-recovery-wallet-seed", "TEST", "food-seed", 1000, f.now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.cart, _, err = f.service.SetCart(f.customer, "food-recovery-cart-00001", CartRequest{RestaurantID: foodRestaurantOne, PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: foodMenuOne, Quantity: 2, OptionIDs: []string{foodOptionOne}}}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *foodRecoveryFixture) restart(t *testing.T) {
	t.Helper()
	var err error
	f.service, err = NewPostgresService(f.pool, f.payments, f.wallets, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
}
func (f *foodRecoveryFixture) balance(t *testing.T) int64 {
	t.Helper()
	account, err := f.wallets.Account(wallet.Scope{TenantID: foodTenantOne, Country: "IN", CustomerID: foodCustomerOne})
	if err != nil || !wallet.Verify(account) {
		t.Fatalf("invalid wallet: %#v %v", account, err)
	}
	return account.Balance
}

func TestPostgresFoodRecoversEveryCommittedEffectWithoutAnotherDebit(t *testing.T) {
	for _, stage := range []string{"wallet-debited", "payment-created", "order-created"} {
		t.Run(stage, func(t *testing.T) {
			f := newFoodRecoveryFixture(t, false)
			interrupted := errors.New("process interrupted")
			f.service.afterEffect = func(value string) error {
				if value == stage {
					return interrupted
				}
				return nil
			}
			request := CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "WALLET"}
			if _, _, err := f.service.CreateOrder(f.customer, "food-recovery-order-00001", request); !errors.Is(err, interrupted) {
				t.Fatalf("no interruption: %v", err)
			}
			f.restart(t)
			if count, err := f.service.RecoverCheckouts(context.Background(), 100); err != nil || count != 1 {
				t.Fatalf("count=%d err=%v", count, err)
			}
			if _, replay, err := f.service.CreateOrder(f.customer, "food-recovery-order-00001", request); err != nil || !replay {
				t.Fatalf("replay=%t err=%v", replay, err)
			}
			if balance := f.balance(t); balance != 728 {
				t.Fatalf("balance=%d", balance)
			}
			for table := range map[string]bool{"food.orders": true, "payment.payments": true} {
				var count int
				if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestPostgresFoodDifferentKeysConcurrentlyClaimOneCartBeforeMoney(t *testing.T) {
	f := newFoodRecoveryFixture(t, false)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := f.service.CreateOrder(f.customer, fmt.Sprintf("food-concurrent-order-%03d", i), CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "WALLET"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 || f.balance(t) != 728 {
		t.Fatalf("success=%d balance=%d", success, f.balance(t))
	}
	var debits int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM wallet.ledger_entries WHERE entry_type='DEBIT'`).Scan(&debits); err != nil || debits != 1 {
		t.Fatalf("debits=%d err=%v", debits, err)
	}
}

func TestPostgresFoodUnpaidFailedExpiredAndOutOfOrderPaymentsCannotFulfil(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint("provider-failed-", failed), func(t *testing.T) {
			f := newFoodRecoveryFixture(t, failed)
			value, _, err := f.service.CreateOrder(f.customer, "food-prepaid-order-00001", CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "RAZORPAY"})
			if err != nil || value.Status != StatusAwaitingPayment {
				t.Fatalf("value=%#v err=%v", value, err)
			}
			vendor := Actor{TenantID: foodTenantOne, Country: "IN", Subject: foodOwnerOne, Roles: []string{"RESTAURANT_VENDOR"}}
			if _, _, err := f.service.RestaurantTransition(vendor, "food-unpaid-accept-00001", value.ID, value.Revision, RestaurantTransitionRequest{Status: StatusAccepted}); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("unpaid accepted: %v", err)
			}
			if failed {
				return
			}
			p, err := f.payments.Get(pay.Scope{TenantID: foodTenantOne, Country: "IN", CustomerID: foodCustomerOne}, value.Payment.Reference)
			if err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(21 * time.Minute)
			if _, err := f.service.Expire(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			for i, status := range []pay.Status{pay.StatusCaptured, pay.StatusAuthorised, pay.StatusFailedFinal, pay.StatusCaptured} {
				body, _ := json.Marshal(pay.ProviderEvent{EventID: fmt.Sprintf("food-late-event-%03d", i), PaymentID: p.ID, ProviderReference: p.ProviderReference, Status: status, AmountMinor: p.Amount.AmountMinor, Currency: "INR"})
				for attempt := 0; attempt < 2; attempt++ {
					if _, _, err := f.payments.HandleWebhook(pay.MethodRazorpay, pay.Sign([]byte("food-synthetic-provider-secret-minimum-32"), body), body); err != nil {
						t.Fatal(err)
					}
					if found, err := f.service.HandlePayment(context.Background(), p.ID); err != nil || !found {
						t.Fatalf("found=%t err=%v", found, err)
					}
				}
			}
			loaded, err := f.service.Order(f.customer, value.ID)
			if err != nil || loaded.Status != StatusTimedOut || loaded.Payment.Status != string(pay.StatusRefundSubmitted) {
				t.Fatalf("late=%#v err=%v", loaded, err)
			}
			if f.balance(t) != 1000 {
				t.Fatal("prepaid altered wallet")
			}
		})
	}
}

func TestPostgresFoodRequiresAssignmentAndRejectsOtherIdentityScopesWithoutMutation(t *testing.T) {
	f := newFoodRecoveryFixture(t, false)
	value, _, err := f.service.CreateOrder(f.customer, "food-owned-order-000001", CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "WALLET"})
	if err != nil {
		t.Fatal(err)
	}
	before := f.balance(t)
	other := uuid.NewString()
	for _, actor := range []Actor{
		{TenantID: foodTenantOne, Country: "IN", Subject: other, Roles: []string{"RIDER"}},
		{TenantID: foodTenantOne, Country: "IN", Subject: other, Roles: []string{"CUSTOMER"}},
		{TenantID: foodTenantTwo, Country: "IN", Subject: foodCustomerOne, Roles: []string{"CUSTOMER", "DISPATCH"}},
		{TenantID: foodTenantOne, Country: "NG", Subject: foodCustomerOne, Roles: []string{"CUSTOMER", "DISPATCH"}},
	} {
		if _, err := f.service.Order(actor, value.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("read allowed for %#v: %v", actor, err)
		}
		if _, _, err := f.service.DispatchTransition(actor, "food-denied-update-00001", value.ID, value.Revision, StatusRiderAssigned); !errors.Is(err, ErrForbidden) {
			t.Fatalf("mutation allowed for %#v: %v", actor, err)
		}
	}
	loaded, err := f.service.Order(f.customer, value.ID)
	if err != nil || loaded.Revision != value.Revision || f.balance(t) != before {
		t.Fatalf("denial changed state: %#v %v", loaded, err)
	}
	rider := Actor{TenantID: foodTenantOne, Country: "IN", Subject: other, Roles: []string{"RIDER"}}
	_, err = f.pool.Exec(context.Background(), `INSERT INTO fulfillment.delivery_tasks(id,tenant_id,country,order_id,order_type,region_id,territory_id,zone_id,revision,status,assigned_rider_identity_id,pickup,dropoff,distance_meters,earning_minor,currency,delivery_otp_digest,updated_at) VALUES($1,$2,'IN',$3,'FOOD','region','territory','zone',1,'ASSIGNED',$4,'{}','{}',1000,1000,'INR','x',$5)`, uuid.NewString(), foodTenantOne, value.ID, other, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Order(rider, value.ID); err != nil {
		t.Fatalf("assigned rider cannot read: %v", err)
	}
	rider.Subject = uuid.NewString()
	if _, err := f.service.Order(rider, value.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other rider read: %v", err)
	}
}

func TestPostgresFoodCompensationIntentSurvivesInterruptedRefund(t *testing.T) {
	for _, stage := range []string{"order-mutated", "wallet-reversed", "payment-compensated"} {
		t.Run(stage, func(t *testing.T) {
			f := newFoodRecoveryFixture(t, false)
			value, _, err := f.service.CreateOrder(f.customer, "food-compensation-order-001", CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "WALLET"})
			if err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(4 * time.Minute)
			interruption := errors.New("refund process interrupted")
			f.service.afterEffect = func(current string) error {
				if current == stage {
					return interruption
				}
				return nil
			}
			if _, err := f.service.Expire(context.Background(), 100); !errors.Is(err, interruption) {
				t.Fatalf("no %s interruption: %v", stage, err)
			}
			f.restart(t)
			for attempt := 0; attempt < 2; attempt++ {
				if err := f.service.RecoverPayments(context.Background(), 100); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := f.service.Order(f.customer, value.ID)
			if err != nil || loaded.Status != StatusTimedOut || loaded.Payment.RefundState != "COMPLETE" {
				t.Fatalf("recovery=%#v err=%v", loaded, err)
			}
			if f.balance(t) != 1000 {
				t.Fatalf("refund balance=%d", f.balance(t))
			}
			var reversals int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM wallet.ledger_entries WHERE entry_type='REVERSAL'`).Scan(&reversals); err != nil || reversals != 1 {
				t.Fatalf("reversals=%d err=%v", reversals, err)
			}
		})
	}
}

func TestPostgresFoodVerifiedCaptureEntersRestaurantQueueOnce(t *testing.T) {
	f := newFoodRecoveryFixture(t, false)
	value, _, err := f.service.CreateOrder(f.customer, "food-capture-on-time-001", CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "RAZORPAY"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.payments.Get(pay.Scope{TenantID: foodTenantOne, Country: "IN", CustomerID: foodCustomerOne}, value.Payment.Reference)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(pay.ProviderEvent{EventID: "food-verified-capture-001", PaymentID: p.ID, ProviderReference: p.ProviderReference, Status: pay.StatusCaptured, AmountMinor: p.Amount.AmountMinor, Currency: "INR"})
	for attempt := 0; attempt < 3; attempt++ {
		if _, _, err := f.payments.HandleWebhook(pay.MethodRazorpay, pay.Sign([]byte("food-synthetic-provider-secret-minimum-32"), body), body); err != nil {
			t.Fatal(err)
		}
		if found, err := f.service.HandlePayment(context.Background(), p.ID); err != nil || !found {
			t.Fatalf("found=%t err=%v", found, err)
		}
	}
	loaded, err := f.service.Order(f.customer, value.ID)
	if err != nil || loaded.Status != StatusPendingRestaurant || loaded.Revision != 2 || len(loaded.Timeline) != 2 {
		t.Fatalf("capture=%#v err=%v", loaded, err)
	}
	vendor := Actor{TenantID: foodTenantOne, Country: "IN", Subject: foodOwnerOne, Roles: []string{"RESTAURANT_VENDOR"}}
	if _, _, err := f.service.RestaurantTransition(vendor, "food-paid-accept-00001", loaded.ID, loaded.Revision, RestaurantTransitionRequest{Status: StatusAccepted}); err != nil {
		t.Fatalf("paid order unavailable: %v", err)
	}
}

func TestPostgresFoodDisabledProviderIsNotOfferedOrClaimed(t *testing.T) {
	f := newFoodRecoveryFixture(t, false)
	payments, err := pay.NewPostgresService(f.pool, func() time.Time { return f.now }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPostgresService(f.pool, payments, f.wallets, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	methods := service.AvailablePaymentMethods(f.customer)
	if len(methods) != 1 || methods[0] != "WALLET" {
		t.Fatalf("disabled methods=%v", methods)
	}
	if _, _, err := service.CreateOrder(f.customer, "food-disabled-provider-001", CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "RAZORPAY"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("disabled provider accepted: %v", err)
	}
	if _, _, err := service.CreateOrder(f.customer, "food-fallback-wallet-001", CreateOrderRequest{CartID: f.cart.ID, PaymentMethod: "WALLET"}); err != nil {
		t.Fatalf("unavailable method claimed the cart: %v", err)
	}
	if f.balance(t) != 728 {
		t.Fatal("unexpected wallet debit")
	}
}
