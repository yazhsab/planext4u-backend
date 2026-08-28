package verticalslice

import (
	"net/http"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/food"
	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
	"github.com/yazhsab/planext4u-backend/internal/supply"
)

func phase4Handlers(clock func() time.Time) (http.Handler, http.Handler, http.Handler, error) {
	supplyService, err := supply.NewService(clock)
	if err != nil {
		return nil, nil, nil, err
	}
	supplyHandler, err := supply.NewHandler(supplyService)
	if err != nil {
		return nil, nil, nil, err
	}

	foodService, err := food.NewService(food.Configuration{
		TenantID: syntheticTenant, Country: "IN", CartTTL: 20 * time.Minute, AcceptanceTTL: 3 * time.Minute, TaxBasisPoints: 500,
		Restaurants: []food.Restaurant{{
			ID: "restaurant-saravana", OwnerID: "restaurant-owner-001", Name: "Saravana Kitchen", Cuisine: []string{"South Indian", "Vegetarian"}, PostalCodes: []string{"600001", "600002", "600003"}, Rating: 4.7, Verified: true, Open: true, AcceptUntilMinute: 1439, PreparationMinutes: 25,
			DeliveryFee: food.Money{AmountMinor: 2500, Currency: "INR"}, MinimumOrder: food.Money{AmountMinor: 10000, Currency: "INR"}, UpdatedAt: clock().UTC(),
		}},
		Menu: []food.MenuItem{
			{ID: "menu-meals-001", RestaurantID: "restaurant-saravana", Name: "South Indian meals", Description: "Fresh vegetarian thali prepared locally", Category: "Meals", Vegetarian: true, BasePrice: food.Money{AmountMinor: 15000, Currency: "INR"}, Available: true, ImageAssetID: "asset-food-meals-synthetic", OptionGroups: []food.OptionGroup{
				{ID: "group-size", Name: "Size", Minimum: 1, Maximum: 1, Options: []food.Option{{ID: "option-meals-regular", Name: "Regular", PriceDelta: food.Money{AmountMinor: 0, Currency: "INR"}, Available: true}, {ID: "option-meals-large", Name: "Large", PriceDelta: food.Money{AmountMinor: 3000, Currency: "INR"}, Available: true}}},
				{ID: "group-addon", Name: "Add-ons", Minimum: 0, Maximum: 2, Options: []food.Option{{ID: "option-curd", Name: "Curd", PriceDelta: food.Money{AmountMinor: 1500, Currency: "INR"}, Available: true}, {ID: "option-dessert", Name: "Dessert", PriceDelta: food.Money{AmountMinor: 2500, Currency: "INR"}, Available: true}}},
			}},
			{ID: "menu-dosa-001", RestaurantID: "restaurant-saravana", Name: "Masala dosa", Description: "Crisp dosa with potato masala", Category: "Tiffin", Vegetarian: true, BasePrice: food.Money{AmountMinor: 9000, Currency: "INR"}, Available: true, ImageAssetID: "asset-food-dosa-synthetic"},
		},
	}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	foodHandler, err := food.NewHandler(foodService)
	if err != nil {
		return nil, nil, nil, err
	}

	fulfillmentService, err := fulfillment.NewService(fulfillment.Configuration{
		TenantID: syntheticTenant, Country: "IN", OfferTTL: 45 * time.Second, LocationTTL: 2 * time.Minute, ChatAfterDeliveryTTL: 2 * time.Hour, SettlementCooling: 48 * time.Hour, CommissionBasisPoints: 1000, TaxBasisPoints: 1800,
		Territories: []fulfillment.Territory{
			{ID: "territory-chennai-core", RegionID: "region-chennai", FranchiseID: "franchise-chennai-001", Name: "Chennai core", Center: fulfillment.Point{Latitude: 13.0827, Longitude: 80.2707}, RadiusKM: 25, PostalCodes: []string{"600001", "600002", "600003"}},
			{ID: "territory-bengaluru-core", RegionID: "region-bengaluru", FranchiseID: "franchise-bengaluru-001", Name: "Bengaluru core", Center: fulfillment.Point{Latitude: 12.9716, Longitude: 77.5946}, RadiusKM: 25, PostalCodes: []string{"560001"}},
		},
	}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	fulfillmentHandler, err := fulfillment.NewHandler(fulfillmentService)
	if err != nil {
		return nil, nil, nil, err
	}
	return supplyHandler, foodHandler, fulfillmentHandler, nil
}

func phase4Route(path string) string {
	exact := map[string]bool{
		"/v1/vendor/applications": true, "/v1/vendor/application": true, "/v1/vendor/application/documents": true, "/v1/vendor/application/field-visit": true, "/v1/vendor/application/zones": true, "/v1/vendor/application/bank": true, "/v1/vendor/dashboard": true, "/v1/vendor/catalog": true, "/v1/vendor/work": true, "/v1/vendor/promotions": true,
		"/v1/restaurants": true, "/v1/food-carts": true, "/v1/food-orders": true,
		"/v1/rider/applications": true, "/v1/rider/profile": true, "/v1/rider/duty/start": true, "/v1/rider/duty": true, "/v1/rider/duty/end": true, "/v1/rider/offers": true, "/v1/rider/tasks": true, "/v1/rider/location": true, "/v1/rider/offline-recovery": true,
		"/v1/dispatch/tasks": true, "/v1/dispatch/stale-assignment-sweep": true, "/v1/settlements/ledger": true, "/v1/settlements/seed": true, "/v1/settlements/reconciliation": true, "/v1/payouts": true, "/v1/operations/territories": true, "/v1/operations/attendance": true, "/v1/operations/audit": true,
	}
	if exact[path] {
		return path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" {
		return ""
	}
	switch parts[1] {
	case "restaurants":
		if len(parts) == 4 && parts[3] == "menu" {
			return "/v1/restaurants/{restaurant_id}/menu"
		}
	case "food-orders":
		if len(parts) == 3 {
			return "/v1/food-orders/{order_id}"
		}
		if len(parts) == 4 {
			return "/v1/food-orders/{order_id}/" + parts[3]
		}
	case "vendor":
		if len(parts) == 5 && parts[2] == "applications" {
			return "/v1/vendor/applications/{vendor_id}/" + parts[4]
		}
		if len(parts) == 4 && parts[2] == "catalog" {
			return "/v1/vendor/catalog/{item_id}"
		}
		if len(parts) == 5 && parts[2] == "catalog" {
			return "/v1/vendor/catalog/{item_id}/" + parts[4]
		}
		if len(parts) == 5 && parts[2] == "work" {
			return "/v1/vendor/work/{work_id}/" + parts[4]
		}
	case "rider":
		if len(parts) == 5 && parts[2] == "applications" {
			return "/v1/rider/applications/{rider_id}/review"
		}
		if len(parts) == 5 && parts[2] == "tasks" {
			return "/v1/rider/tasks/{task_id}/" + parts[4]
		}
		if len(parts) == 4 && parts[2] == "locations" {
			return "/v1/rider/locations/{rider_id}"
		}
	case "dispatch":
		if len(parts) == 5 && parts[2] == "tasks" {
			return "/v1/dispatch/tasks/{task_id}/" + parts[4]
		}
	case "order-chats":
		if len(parts) == 3 {
			return "/v1/order-chats/{order_id}"
		}
		if len(parts) == 4 && parts[3] == "messages" {
			return "/v1/order-chats/{conversation_id}/messages"
		}
		if len(parts) == 6 && parts[3] == "messages" && parts[5] == "receipts" {
			return "/v1/order-chats/{conversation_id}/messages/{message_id}/receipts"
		}
		if len(parts) == 4 && parts[3] == "block" {
			return "/v1/order-chats/{conversation_id}/block"
		}
	case "payouts":
		if len(parts) == 4 {
			return "/v1/payouts/{payout_id}/" + parts[3]
		}
	case "operations":
		if len(parts) == 5 && parts[2] == "territories" {
			return "/v1/operations/territories/{territory_id}/field-check-in"
		}
		if len(parts) == 5 && parts[2] == "regions" {
			return "/v1/operations/regions/{region_id}/dashboard"
		}
	}
	return ""
}
