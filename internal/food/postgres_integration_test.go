//go:build integration

package food

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pay "github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

func TestPostgresFoodPricingPaymentReplayRestartLifecycleAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("FOOD_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("FOOD_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS food CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS food CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/food/000001_food.up.sql",
		"../../migrations/food/000002_durable_order_runtime.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	seedPostgresFood(t, ctx, pool, now)
	payments := &foodPaymentFake{values: map[string]pay.Payment{}, now: now}
	wallets := &foodWalletFake{}
	service, err := NewPostgresService(pool, payments, wallets, func() time.Time { return now })
	if err != nil || service.Ready(ctx) != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
	customer := Actor{TenantID: foodTenantOne, Country: "IN", Subject: foodCustomerOne, Roles: []string{"CUSTOMER"}}
	restaurants, err := service.Restaurants(customer, "600001", "South Indian")
	if err != nil || len(restaurants) != 1 || restaurants[0].OwnerID != "" {
		t.Fatalf("restaurants=%#v err=%v", restaurants, err)
	}
	menu, err := service.Menu(customer, foodRestaurantOne, "600001")
	if err != nil || len(menu) != 1 || len(menu[0].OptionGroups) != 1 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	cart, replay, err := service.SetCart(customer, "food-cart-postgres-000001", CartRequest{RestaurantID: foodRestaurantOne, PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: foodMenuOne, Quantity: 2, OptionIDs: []string{foodOptionOne}}}})
	if err != nil || replay || cart.Subtotal.AmountMinor != 24000 || cart.Tax.AmountMinor != 1200 || cart.Total.AmountMinor != 27200 {
		t.Fatalf("cart=%#v replay=%t err=%v", cart, replay, err)
	}
	order, replay, err := service.CreateOrder(customer, "food-order-postgres-00001", CreateOrderRequest{CartID: cart.ID, PaymentMethod: "WALLET"})
	if err != nil || replay || order.Status != StatusPendingRestaurant || order.Payment.Status != string(pay.StatusCaptured) {
		t.Fatalf("order=%#v replay=%t err=%v", order, replay, err)
	}
	restarted, _ := NewPostgresService(pool, payments, wallets, func() time.Time { return now })
	replayed, replay, err := restarted.CreateOrder(customer, "food-order-postgres-00001", CreateOrderRequest{CartID: cart.ID, PaymentMethod: "WALLET"})
	if err != nil || !replay || replayed.ID != order.ID || payments.creates != 1 || wallets.redeems != 1 {
		t.Fatalf("restart replay=%#v replay=%t payments=%d wallets=%d err=%v", replayed, replay, payments.creates, wallets.redeems, err)
	}
	if _, _, err := restarted.CreateOrder(customer, "food-order-postgres-00001", CreateOrderRequest{CartID: cart.ID, PaymentMethod: "RAZORPAY"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("order idempotency conflict=%v", err)
	}
	vendor := Actor{TenantID: foodTenantOne, Country: "IN", Subject: foodOwnerOne, Roles: []string{"RESTAURANT_VENDOR"}}
	for _, transition := range []OrderStatus{StatusAccepted, StatusPreparing, StatusReady} {
		order, _, err = restarted.RestaurantTransition(vendor, "food-vendor-"+string(transition)+"-0001", order.ID, order.Revision, RestaurantTransitionRequest{Status: transition})
		if err != nil {
			t.Fatalf("restaurant transition %s: %v", transition, err)
		}
	}
	dispatch := Actor{TenantID: foodTenantOne, Country: "IN", Subject: foodDispatchOne, Roles: []string{"DISPATCH"}}
	for _, transition := range []OrderStatus{StatusRiderAssigned, StatusPickedUp, StatusDelivered} {
		order, _, err = restarted.DispatchTransition(dispatch, "food-dispatch-"+string(transition)+"-01", order.ID, order.Revision, transition)
		if err != nil {
			t.Fatalf("dispatch transition %s: %v", transition, err)
		}
	}
	loaded, err := restarted.Order(customer, order.ID)
	if err != nil || loaded.Status != StatusDelivered || len(loaded.Timeline) != 7 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	wrongTenant := Actor{TenantID: foodTenantTwo, Country: "IN", Subject: foodCustomerOne, Roles: []string{"CUSTOMER"}}
	if _, err := restarted.Order(wrongTenant, order.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross tenant order=%v", err)
	}
	var orderCount, timelineCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM food.orders`).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM food.order_timeline`).Scan(&timelineCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 || timelineCount != 7 {
		t.Fatalf("orders=%d timeline=%d", orderCount, timelineCount)
	}
}

func seedPostgresFood(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO food.restaurants (id,tenant_id,country,owner_identity_id,name,cuisine,postal_codes,rating,verified,open,accept_until_minute,preparation_minutes,delivery_fee_minor,minimum_order_minor,currency,updated_at) VALUES ($1,$2,'IN',$3,'P4U Local Kitchen',ARRAY['South Indian'],ARRAY['600001'],4.8,true,true,1440,25,2000,10000,'INR',$4)`, []any{foodRestaurantOne, foodTenantOne, foodOwnerOne, now}},
		{`INSERT INTO food.menu_items (id,restaurant_id,name,description,category,vegetarian,base_price_minor,currency,available,image_asset_id) VALUES ($1,$2,'Millet meal','Healthy local millet meal','Meals',true,10000,'INR',true,$3)`, []any{foodMenuOne, foodRestaurantOne, foodAssetOne}},
		{`INSERT INTO food.option_groups (id,menu_item_id,name,minimum,maximum,instructions) VALUES ($1,$2,'Portion',1,1,'Choose one portion')`, []any{foodGroupOne, foodMenuOne}},
		{`INSERT INTO food.options (id,option_group_id,name,price_delta_minor,currency,available) VALUES ($1,$2,'Large',2000,'INR',true)`, []any{foodOptionOne, foodGroupOne}},
		{`INSERT INTO food.policies (tenant_id,country,version,cart_ttl_seconds,acceptance_ttl_seconds,tax_basis_points,wallet_point_value_minor,updated_at) VALUES ($1,'IN','food-pricing-v1',1200,180,500,100,$2)`, []any{foodTenantOne, now}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

type foodPaymentFake struct {
	mu      sync.Mutex
	values  map[string]pay.Payment
	now     time.Time
	creates int
}

func (fake *foodPaymentFake) Create(_ pay.Scope, key, reference string, method pay.Method, amount pay.Money) (pay.Payment, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
	if value, found := fake.values[id]; found {
		return value, true, nil
	}
	fake.creates++
	value := pay.Payment{ID: id, OrderReference: reference, Method: method, Status: pay.StatusCaptured, Amount: amount, CreatedAt: fake.now, UpdatedAt: fake.now}
	fake.values[id] = value
	return value, false, nil
}

func (fake *foodPaymentFake) RequestRefund(_ pay.Scope, id string, _ pay.Money) (pay.Payment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	value := fake.values[id]
	value.Status = pay.StatusRefundSubmitted
	fake.values[id] = value
	return value, nil
}

func (fake *foodPaymentFake) CancelUncaptured(_ pay.Scope, id string) (pay.Payment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	value := fake.values[id]
	value.Status = pay.StatusCancelled
	fake.values[id] = value
	return value, nil
}

type foodWalletFake struct {
	mu      sync.Mutex
	redeems int
}

func (fake *foodWalletFake) Redeem(_ wallet.Scope, key, _ string, points int64) (wallet.LedgerEntry, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.redeems++
	return wallet.LedgerEntry{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(), DeltaPoints: -points}, false, nil
}

func (fake *foodWalletFake) ReverseDebit(_ wallet.Scope, key, _, _ string) (wallet.LedgerEntry, bool, error) {
	return wallet.LedgerEntry{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()}, false, nil
}

const (
	foodTenantOne     = "c6ae3218-ef27-4280-b836-4b7ee9573ac5"
	foodTenantTwo     = "e0150d2b-ff80-4f8d-838e-c663b91e53d8"
	foodCustomerOne   = "7742bc52-1821-40ef-ad32-8b087d0ca80b"
	foodOwnerOne      = "0c556fe6-f236-4d1c-bda8-afec410b4c07"
	foodDispatchOne   = "29dd769c-24e3-43d5-8265-2ea1461fce51"
	foodRestaurantOne = "fbb9e354-e5ef-45f6-9409-48aa93143c6e"
	foodMenuOne       = "9991e88a-214d-4a94-92cd-6a86c2b1fbb8"
	foodGroupOne      = "7db4fc10-b632-4c98-812d-901f1ca72741"
	foodOptionOne     = "632352bc-1db5-450b-84d5-adf057fe7aaa"
	foodAssetOne      = "4c6dc348-1753-497f-8b19-f2404db47ab3"
)
