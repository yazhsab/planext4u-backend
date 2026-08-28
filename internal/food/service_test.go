package food

import (
	"errors"
	"testing"
	"time"
)

func TestBEP4007AuthoritativeFoodPricingAndCustomisation(t *testing.T) {
	service, _ := foodFixture(t)
	customer := foodActorFor("customer-food-001", "CUSTOMER")
	restaurants, err := service.Restaurants(customer, "600001", "South Indian")
	if err != nil || len(restaurants) != 1 || restaurants[0].OwnerID != "" {
		t.Fatalf("restaurants=%#v err=%v", restaurants, err)
	}
	menu, err := service.Menu(customer, restaurants[0].ID, "600001")
	if err != nil || len(menu) != 2 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	if _, _, err := service.SetCart(customer, "food-cart-missing-size1", CartRequest{RestaurantID: "restaurant-saravana", PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: "menu-meals-001", Quantity: 1}}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing required customization error=%v", err)
	}
	cart, replay, err := service.SetCart(customer, "food-cart-priced-0001", CartRequest{RestaurantID: "restaurant-saravana", PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: "menu-meals-001", Quantity: 2, OptionIDs: []string{"option-meals-large", "option-curd"}, Note: "Less spicy"}}})
	if err != nil || replay || cart.Subtotal.AmountMinor != 39000 || cart.Tax.AmountMinor != 1950 || cart.Total.AmountMinor != 43450 || cart.PricingVersion != "food-pricing-v1" {
		t.Fatalf("cart=%#v replay=%v err=%v", cart, replay, err)
	}
	again, replay, err := service.SetCart(customer, "food-cart-priced-0001", CartRequest{RestaurantID: "restaurant-saravana", PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: "menu-meals-001", Quantity: 2, OptionIDs: []string{"option-meals-large", "option-curd"}, Note: "Less spicy"}}})
	if err != nil || !replay || again.ID != cart.ID {
		t.Fatalf("cart replay=%#v replay=%v err=%v", again, replay, err)
	}
	order, replay, err := service.CreateOrder(customer, "food-order-create-001", CreateOrderRequest{CartID: cart.ID, PaymentMethod: "WALLET"})
	if err != nil || replay || order.Total != cart.Total || order.Payment.Status != "CAPTURED" || order.Status != StatusPendingRestaurant {
		t.Fatalf("order=%#v replay=%v err=%v", order, replay, err)
	}
	attacker := foodActorFor("customer-food-attacker", "CUSTOMER")
	if _, err := service.Order(attacker, order.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner order error=%v", err)
	}
}

func TestBEP4007RestaurantQueueTransitionsRejectAndRefund(t *testing.T) {
	service, _ := foodFixture(t)
	customer := foodActorFor("customer-food-002", "CUSTOMER")
	order := foodOrderFixture(t, service, customer, "reject")
	vendor := foodActorFor("restaurant-owner-001", "RESTAURANT_VENDOR")
	value, replay, err := service.RestaurantTransition(vendor, "food-order-reject-001", order.ID, order.Revision, RestaurantTransitionRequest{Status: StatusRejected, Reason: "Kitchen capacity exhausted"})
	if err != nil || replay || value.Status != StatusRejected || value.Payment.Status != "REFUND_SUBMITTED" || value.Payment.RefundState != "SUBMITTED" {
		t.Fatalf("rejected=%#v replay=%v err=%v", value, replay, err)
	}
	otherVendor := foodActorFor("restaurant-owner-attacker", "RESTAURANT_VENDOR")
	second := foodOrderFixture(t, service, foodActorFor("customer-food-003", "CUSTOMER"), "owner")
	if _, _, err := service.RestaurantTransition(otherVendor, "food-order-owner-deny1", second.ID, second.Revision, RestaurantTransitionRequest{Status: StatusAccepted}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-restaurant transition error=%v", err)
	}
	value, _, err = service.RestaurantTransition(vendor, "food-order-accept-001", second.ID, second.Revision, RestaurantTransitionRequest{Status: StatusAccepted})
	if err != nil || value.EstimatedReadyAt == nil {
		t.Fatalf("accepted=%#v err=%v", value, err)
	}
	value, _, _ = service.RestaurantTransition(vendor, "food-order-preparing-1", second.ID, value.Revision, RestaurantTransitionRequest{Status: StatusPreparing})
	value, _, err = service.RestaurantTransition(vendor, "food-order-ready-00001", second.ID, value.Revision, RestaurantTransitionRequest{Status: StatusReady})
	if err != nil || value.Status != StatusReady {
		t.Fatalf("ready=%#v err=%v", value, err)
	}
}

func TestBEP4007RestaurantTimeoutCutoffAndUnavailableItem(t *testing.T) {
	service, now := foodFixture(t)
	customer := foodActorFor("customer-food-004", "CUSTOMER")
	order := foodOrderFixture(t, service, customer, "timeout")
	*now = now.Add(4 * time.Minute)
	value, err := service.Order(customer, order.ID)
	if err != nil || value.Status != StatusTimedOut || value.Payment.Status != "REFUND_SUBMITTED" {
		t.Fatalf("timed out=%#v err=%v", value, err)
	}
	*now = time.Date(2026, 8, 28, 22, 0, 0, 0, time.UTC)
	if _, _, err := service.SetCart(customer, "food-cart-after-cutoff1", CartRequest{RestaurantID: "restaurant-saravana", PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: "menu-meals-001", Quantity: 1, OptionIDs: []string{"option-meals-regular"}}}}); !errors.Is(err, ErrRestaurantClosed) {
		t.Fatalf("cutoff error=%v", err)
	}
	*now = time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	if _, _, err := service.SetCart(customer, "food-cart-unavailable01", CartRequest{RestaurantID: "restaurant-saravana", PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: "menu-special-001", Quantity: 1}}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable error=%v", err)
	}
}

func foodFixture(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	service, err := NewService(Configuration{
		Restaurants: []Restaurant{{ID: "restaurant-saravana", OwnerID: "restaurant-owner-001", Name: "Saravana Kitchen", Cuisine: []string{"South Indian", "Vegetarian"}, PostalCodes: []string{"600001", "600002"}, Rating: 4.7, Verified: true, Open: true, AcceptUntilMinute: 21 * 60, PreparationMinutes: 25, DeliveryFee: Money{AmountMinor: 2500, Currency: "INR"}, MinimumOrder: Money{AmountMinor: 10000, Currency: "INR"}, tenantID: "tenant-synthetic-001", country: "IN"}},
		Menu: []MenuItem{
			{ID: "menu-meals-001", RestaurantID: "restaurant-saravana", Name: "South Indian meals", Description: "Fresh vegetarian thali", Category: "Meals", Vegetarian: true, BasePrice: Money{AmountMinor: 15000, Currency: "INR"}, Available: true, OptionGroups: []OptionGroup{{ID: "group-size", Name: "Size", Minimum: 1, Maximum: 1, Options: []Option{{ID: "option-meals-regular", Name: "Regular", PriceDelta: Money{AmountMinor: 0, Currency: "INR"}, Available: true}, {ID: "option-meals-large", Name: "Large", PriceDelta: Money{AmountMinor: 3000, Currency: "INR"}, Available: true}}}, {ID: "group-addon", Name: "Add-ons", Minimum: 0, Maximum: 2, Options: []Option{{ID: "option-curd", Name: "Curd", PriceDelta: Money{AmountMinor: 1500, Currency: "INR"}, Available: true}}}}, tenantID: "tenant-synthetic-001", country: "IN"},
			{ID: "menu-special-001", RestaurantID: "restaurant-saravana", Name: "Chef special", Description: "Limited special", Category: "Special", BasePrice: Money{AmountMinor: 20000, Currency: "INR"}, Available: false, tenantID: "tenant-synthetic-001", country: "IN"},
		}, CartTTL: 20 * time.Minute, AcceptanceTTL: 3 * time.Minute, TaxBasisPoints: 500,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service, &now
}

func foodActorFor(subject, role string) Actor {
	return Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: subject, Roles: []string{role}}
}

func foodOrderFixture(t *testing.T, service *Service, actor Actor, suffix string) Order {
	t.Helper()
	cart, _, err := service.SetCart(actor, "food-cart-fixture-"+suffix, CartRequest{RestaurantID: "restaurant-saravana", PostalCode: "600001", Lines: []CartLineRequest{{MenuItemID: "menu-meals-001", Quantity: 1, OptionIDs: []string{"option-meals-regular"}}}})
	if err != nil {
		t.Fatal(err)
	}
	order, _, err := service.CreateOrder(actor, "food-order-fixture-"+suffix, CreateOrderRequest{CartID: cart.ID, PaymentMethod: "RAZORPAY"})
	if err != nil {
		t.Fatal(err)
	}
	return order
}
