package verticalslice

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/booking"
	"github.com/yazhsab/planext4u-backend/internal/catalog"
	"github.com/yazhsab/planext4u-backend/internal/checkout"
	"github.com/yazhsab/planext4u-backend/internal/commerce"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/gateway"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/notification"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

type Config struct {
	SigningKey                  []byte
	Clock                       func() time.Time
	Logger                      *slog.Logger
	Readiness                   func() error
	NotificationProviderFactory func(notification.DeviceResolver) (map[notification.Channel]notification.Provider, error)
}

func New(config Config) (http.Handler, error) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	tokens, err := newTokenService(config.SigningKey, config.Clock)
	if err != nil {
		return nil, err
	}
	configuration, err := configurationHandler(config.Clock)
	if err != nil {
		return nil, err
	}
	catalogHandler, err := customerCatalogHandler(config.Clock)
	if err != nil {
		return nil, err
	}
	notificationRepository := notification.NewMemoryRepository()
	publishedAt := config.Clock().UTC()
	notificationRepository.PutTemplate(notification.Template{TenantID: syntheticTenant, Country: "IN", Key: "order-status", Version: 1, Locale: "en", Channel: notification.ChannelPush, Status: notification.TemplatePublished, Body: "Order {{order_id}} is now {{status}}.", Variables: []string{"order_id", "status"}, PublishedAt: &publishedAt})
	providers := map[notification.Channel]notification.Provider{}
	if config.NotificationProviderFactory != nil {
		providers, err = config.NotificationProviderFactory(notificationRepository)
		if err != nil {
			return nil, err
		}
	}
	notificationService, err := notification.NewService(notificationRepository, providers, config.Clock)
	if err != nil {
		return nil, err
	}
	orderNotifier, err := notification.NewOrderNotifier(notificationService, 1)
	if err != nil {
		return nil, err
	}
	notificationHandler, err := notification.NewHandler(notificationService)
	if err != nil {
		return nil, err
	}
	commerceHandler, transactionHandler, bookingHandler, err := customerTransactionHandlers(config.Clock, orderNotifier)
	if err != nil {
		return nil, err
	}
	supplyHandler, foodHandler, fulfillmentHandler, err := phase4Handlers(config.Clock)
	if err != nil {
		return nil, err
	}
	socialHandler, err := phase5Handler(config.Clock)
	if err != nil {
		return nil, err
	}

	upstream := http.NewServeMux()
	upstream.Handle("/v1/auth/exchange", authHandler{tokens: tokens, clock: config.Clock})
	upstream.Handle("/v1/bootstrap", configuration)
	upstream.Handle("/v1/home", catalogHandler)
	upstream.Handle("/v1/catalog/", catalogHandler)
	upstream.Handle("/v1/serviceability/check", catalogHandler)
	upstream.Handle("/v1/geocoding/search", catalogHandler)
	upstream.Handle("/v1/cart", commerceHandler)
	upstream.Handle("/v1/cart/", commerceHandler)
	for _, path := range []string{"/v1/addresses", "/v1/delivery-slots", "/v1/checkout/", "/v1/payments/", "/v1/orders", "/v1/orders/", "/v1/wallet", "/v1/wallet/"} {
		upstream.Handle(path, transactionHandler)
	}
	for _, path := range []string{"/v1/services", "/v1/services/", "/v1/service-slot-holds", "/v1/service-slot-holds/", "/v1/service-bookings", "/v1/service-bookings/"} {
		upstream.Handle(path, bookingHandler)
	}
	upstream.Handle("/v1/notifications/devices/", notificationHandler)
	upstream.Handle("/v1/vendor/", supplyHandler)
	upstream.Handle("/v1/restaurants", foodHandler)
	upstream.Handle("/v1/restaurants/", foodHandler)
	upstream.Handle("/v1/food-carts", foodHandler)
	upstream.Handle("/v1/food-orders", foodHandler)
	upstream.Handle("/v1/food-orders/", foodHandler)
	upstream.Handle("/v1/rider/", fulfillmentHandler)
	upstream.Handle("/v1/dispatch/", fulfillmentHandler)
	upstream.Handle("/v1/order-chats/", fulfillmentHandler)
	upstream.Handle("/v1/settlements/", fulfillmentHandler)
	upstream.Handle("/v1/payouts", fulfillmentHandler)
	upstream.Handle("/v1/payouts/", fulfillmentHandler)
	upstream.Handle("/v1/operations/", fulfillmentHandler)
	upstream.Handle("/v1/social/", socialHandler)
	upstream.Handle("/v1/moderation/", socialHandler)

	gatewayConfig := gateway.DefaultConfig(nil)
	gatewayConfig.UpstreamHandler = upstream
	gatewayConfig.AnonymousRoutes["/v1/payments/webhooks/razorpay"] = map[string]struct{}{http.MethodPost: {}}
	gatewayConfig.AnonymousRoutes["/v1/payments/webhooks/paystack"] = map[string]struct{}{http.MethodPost: {}}
	gatewayConfig.Readiness = func(context.Context) error { return nil }
	if config.Readiness != nil {
		gatewayConfig.Readiness = func(context.Context) error { return config.Readiness() }
	}
	return gateway.NewHandler(gatewayConfig, tokens, config.Logger)
}

func Route(request *http.Request) string {
	if route := phase5Route(request.URL.Path); route != "" {
		return route
	}
	if route := phase4Route(request.URL.Path); route != "" {
		return route
	}
	switch request.URL.Path {
	case "/healthz", "/readyz", "/health/ready", "/v1/auth/exchange", "/v1/bootstrap", "/v1/home", "/v1/catalog/categories", "/v1/catalog/items", "/v1/catalog/search", "/v1/catalog/suggestions", "/v1/serviceability/check", "/v1/geocoding/search", "/v1/cart", "/v1/addresses", "/v1/delivery-slots", "/v1/checkout/quotes", "/v1/checkout/orders", "/v1/orders", "/v1/wallet", "/v1/wallet/experience", "/v1/wallet/referrals", "/v1/wallet/refills", "/v1/notifications/devices/current", "/v1/payments/webhooks/razorpay", "/v1/payments/webhooks/paystack", "/v1/services", "/v1/service-slot-holds", "/v1/service-bookings":
		return request.URL.Path
	default:
		if strings.HasPrefix(request.URL.Path, "/v1/catalog/items/") {
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) == 5 && parts[4] == "questions" {
				return "/v1/catalog/items/{item_id}/questions"
			}
			return "/v1/catalog/items/{item_id}"
		}
		if strings.HasPrefix(request.URL.Path, "/v1/cart/items/") {
			return "/v1/cart/items/{variant_id}"
		}
		if strings.HasPrefix(request.URL.Path, "/v1/payments/") {
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) == 4 && parts[3] == "retry" {
				return "/v1/payments/{payment_id}/retry"
			}
			return "/v1/payments/{payment_id}"
		}
		if strings.HasPrefix(request.URL.Path, "/v1/orders/") {
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) == 3 {
				return "/v1/orders/{order_id}"
			}
			if len(parts) == 4 {
				return "/v1/orders/{order_id}/" + parts[3]
			}
		}
		if strings.HasPrefix(request.URL.Path, "/v1/services/") {
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) == 4 && parts[3] == "slots" {
				return "/v1/services/{service_id}/slots"
			}
			return "/v1/services/{service_id}"
		}
		if strings.HasPrefix(request.URL.Path, "/v1/service-slot-holds/") {
			return "/v1/service-slot-holds/{hold_id}"
		}
		if strings.HasPrefix(request.URL.Path, "/v1/service-bookings/") {
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) == 3 {
				return "/v1/service-bookings/{booking_id}"
			}
			if len(parts) == 4 {
				return "/v1/service-bookings/{booking_id}/" + parts[3]
			}
		}
		return "unmatched"
	}
}

type syntheticCommerceSnapshots map[string]commerce.VariantSnapshot

func (snapshots syntheticCommerceSnapshots) Resolve(_ context.Context, _ commerce.Scope, variantID string) (commerce.VariantSnapshot, error) {
	value, exists := snapshots[variantID]
	if !exists {
		return commerce.VariantSnapshot{}, commerce.ErrVariantNotFound
	}
	return value, nil
}

func customerTransactionHandlers(clock func() time.Time, notifier order.Notifier) (http.Handler, http.Handler, http.Handler, error) {
	service, err := commerce.NewService(syntheticCommerceSnapshots{
		"variant-milk-1l":          {VariantID: "variant-milk-1l", ItemID: "item-milk", VendorID: "vendor-dairy-001", ItemName: "Fresh milk", VariantName: "1 litre", UnitPrice: commerce.Money{AmountMinor: 6500, Currency: "INR"}, Available: true, Stock: 50, MaxPerOrder: 10},
		"variant-groceries-weekly": {VariantID: "variant-groceries-weekly", ItemID: "item-groceries", VendorID: "vendor-mart-001", ItemName: "Weekly groceries", VariantName: "Essential bundle", UnitPrice: commerce.Money{AmountMinor: 120000, Currency: "INR"}, Available: true, Stock: 20, MaxPerOrder: 3},
		"variant-sesame-oil-500ml": {VariantID: "variant-sesame-oil-500ml", ItemID: "item-sesame-oil", VendorID: "vendor-foods-001", ItemName: "Cold-pressed sesame oil", VariantName: "500ml", UnitPrice: commerce.Money{AmountMinor: 24000, Currency: "INR"}, Available: true, Stock: 24, MaxPerOrder: 5},
		"variant-sesame-oil-1l":    {VariantID: "variant-sesame-oil-1l", ItemID: "item-sesame-oil", VendorID: "vendor-foods-001", ItemName: "Cold-pressed sesame oil", VariantName: "1L", UnitPrice: commerce.Money{AmountMinor: 45000, Currency: "INR"}, Available: true, Stock: 12, MaxPerOrder: 5},
	}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	commerceHandler, err := commerce.NewHandler(service)
	if err != nil {
		return nil, nil, nil, err
	}
	inventoryService, err := inventory.NewService([]inventory.SeedStock{
		{Scope: inventory.Scope{TenantID: syntheticTenant, Country: "IN"}, VariantID: "variant-milk-1l", Quantity: 50},
		{Scope: inventory.Scope{TenantID: syntheticTenant, Country: "IN"}, VariantID: "variant-groceries-weekly", Quantity: 20},
		{Scope: inventory.Scope{TenantID: syntheticTenant, Country: "IN"}, VariantID: "variant-sesame-oil-500ml", Quantity: 24},
		{Scope: inventory.Scope{TenantID: syntheticTenant, Country: "IN"}, VariantID: "variant-sesame-oil-1l", Quantity: 12},
	}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	walletService, err := wallet.NewServiceWithProgram(clock, wallet.RewardPolicy{DailyDeviceCap: 100, Cooldown: time.Minute, ReferralSenderPoints: 500, ReferralRecipientPoints: 250, ReferralExpiry: 365 * 24 * time.Hour}, wallet.Program{
		ReferralBaseURL: "https://planext4u.net/referral",
		RefillOffers: []wallet.RefillOffer{
			{ID: "refill-5000", Country: "IN", Points: 5000, Price: wallet.Money{AmountMinor: 5000, Currency: "INR"}, PaymentMethods: []string{"RAZORPAY"}, ExpiresAfter: 365 * 24 * time.Hour},
			{ID: "refill-10000", Country: "IN", Points: 10000, BonusPoints: 500, Price: wallet.Money{AmountMinor: 10000, Currency: "INR"}, PaymentMethods: []string{"RAZORPAY"}, ExpiresAfter: 365 * 24 * time.Hour},
		},
		Campaigns: []wallet.RewardCampaign{{ID: "first-local-order", Country: "IN", Title: "First local order", Description: "Earn bonus points after your first completed local order.", Points: 250, EndsAt: clock().UTC().AddDate(1, 0, 0)}},
	})
	if err != nil {
		return nil, nil, nil, err
	}
	walletScope := wallet.Scope{TenantID: syntheticTenant, Country: "IN", CustomerID: "customer-synthetic-001"}
	if _, _, err = walletService.Credit(walletScope, "synthetic-wallet-seed-0001", "WELCOME_REWARD", "synthetic-launch-001", 25000, clock().UTC().AddDate(1, 0, 0)); err != nil {
		return nil, nil, nil, err
	}
	providerSecret := []byte("synthetic-provider-secret-32-bytes-minimum")
	paymentService, err := payment.NewService(clock, map[payment.Method][]byte{payment.MethodRazorpay: providerSecret, payment.MethodPaystack: providerSecret})
	if err != nil {
		return nil, nil, nil, err
	}
	orderService, err := order.NewServiceWithNotifier(clock, notifier)
	if err != nil {
		return nil, nil, nil, err
	}
	now := clock().UTC()
	checkoutService, err := checkout.NewService(checkout.Dependencies{Cart: service, Inventory: inventoryService, Wallet: walletService, Payment: paymentService, Orders: orderService}, checkout.Configuration{
		Addresses: []checkout.Address{{ID: "address-home-001", Label: "Home", Line1: "12 Marina Road", PostalCode: "600001", Locality: "Chennai", Serviceable: true, Default: true, Revision: 1, TenantID: syntheticTenant, Country: "IN", CustomerID: "customer-synthetic-001"}},
		Slots: []checkout.DeliverySlot{
			{ID: "slot-standard-001", Country: "IN", WindowStart: now.Add(24 * time.Hour), WindowEnd: now.Add(28 * time.Hour), Fee: checkout.Money{AmountMinor: 3000, Currency: "INR"}, Capacity: 100},
			{ID: "slot-express-001", Country: "IN", WindowStart: now.Add(2 * time.Hour), WindowEnd: now.Add(4 * time.Hour), Fee: checkout.Money{AmountMinor: 9000, Currency: "INR"}, Capacity: 25},
		},
		Promotions:  []checkout.Promotion{{Code: "LOCAL10", Country: "IN", MinimumSubtotal: 10000, DiscountBasisPts: 1000, MaximumDiscount: 10000, StartsAt: now.Add(-24 * time.Hour), EndsAt: now.AddDate(0, 1, 0)}},
		Policies:    []checkout.PricingPolicy{{Version: "pricing-2026-01", Country: "IN", TaxBasisPoints: 500, PlatformFeeMinor: 500, WalletPointValueMinor: 1, WalletMode: checkout.WalletHybrid, QuoteTTL: 10 * time.Minute, ReservationTTL: 15 * time.Minute}},
		PostalZones: []checkout.PostalZone{{Country: "IN", PostalCode: "600001", Locality: "Chennai"}},
	}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	transactionHandler, err := checkout.NewHandler(checkoutService)
	if err != nil {
		return nil, nil, nil, err
	}
	bookingService, err := booking.NewService(paymentService, walletService, booking.Configuration{
		Offerings: []booking.Offering{{
			ID: "service-home-cleaning", ProviderID: "provider-clean-001", ProviderName: "Clean Chennai", CategoryID: "home-cleaning",
			Name: "Home deep cleaning", Summary: "Verified two-person cleaning team with supplies", DurationMinutes: 120,
			Price: booking.Money{AmountMinor: 20000, Currency: "INR"}, Advance: booking.Money{AmountMinor: 5000, Currency: "INR"}, PaymentMode: booking.PaymentAdvance,
			VerifiedProvider: true, RatingAverage: 4.8, CompletedBookings: 241, LiveEngagements: 3,
			ServicePostalCodes: []string{"600001", "600002", "600003"}, CancellationPolicyRef: "service-cancel-v1", ReschedulePolicyRef: "service-reschedule-v1", Active: true,
		}},
		Slots: []booking.Slot{
			{ID: "slot-cleaning-morning-001", OfferingID: "service-home-cleaning", ProviderID: "provider-clean-001", StartsAt: now.Add(24 * time.Hour), EndsAt: now.Add(26 * time.Hour), TimeZone: "Asia/Kolkata", Capacity: 2, BufferMinutes: 30, Price: booking.Money{AmountMinor: 20000, Currency: "INR"}, Advance: booking.Money{AmountMinor: 5000, Currency: "INR"}, PolicyVersion: "service-slot-v1", ServiceDate: now.Add(24 * time.Hour).Format("2006-01-02"), ProviderVersion: 1},
			{ID: "slot-cleaning-evening-001", OfferingID: "service-home-cleaning", ProviderID: "provider-clean-001", StartsAt: now.Add(32 * time.Hour), EndsAt: now.Add(34 * time.Hour), TimeZone: "Asia/Kolkata", Capacity: 2, BufferMinutes: 30, Price: booking.Money{AmountMinor: 20000, Currency: "INR"}, Advance: booking.Money{AmountMinor: 5000, Currency: "INR"}, PolicyVersion: "service-slot-v1", ServiceDate: now.Add(32 * time.Hour).Format("2006-01-02"), ProviderVersion: 1},
		},
		Policies: []booking.Policy{{Version: "service-policy-v1", Country: "IN", HoldTTL: 5 * time.Minute, CancellationCutoff: 2 * time.Hour, MaximumFreeReschedules: 1, StartOTPValidity: 30 * time.Minute, CompletionConfirmWindow: 24 * time.Hour, WalletPointValueMinor: 1}},
	}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	bookingHandler, err := booking.NewHandler(bookingService)
	if err != nil {
		return nil, nil, nil, err
	}
	return commerceHandler, transactionHandler, bookingHandler, nil
}

func configurationHandler(clock func() time.Time) (http.Handler, error) {
	snapshot := configcms.Snapshot{
		TenantID: syntheticTenant, Country: "IN", Revision: 1, PublishedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
		MinimumVersions:  map[configcms.Platform]string{configcms.PlatformAndroid: "0.1.0", configcms.PlatformIOS: "0.1.0"},
		LatestVersions:   map[configcms.Platform]string{configcms.PlatformAndroid: "0.1.0", configcms.PlatformIOS: "0.1.0"},
		SupportedLocales: []string{"en", "ta"}, DefaultLocale: "en",
		ConsentPolicies: []configcms.ConsentPolicy{
			{Purpose: "ESSENTIAL", PolicyVersion: "privacy-2026-01", Required: true},
			{Purpose: "LOCATION_SERVICEABILITY", PolicyVersion: "location-2026-01", Required: true},
		},
		Flags: map[string]bool{"customer_home": true, "catalog_read": true, "service_booking": true, "food_ordering": true, "vendor_operations": true, "rider_fulfillment": true, "order_chat": true, "settlements": true},
		HomeSections: []configcms.HomeSection{
			{ID: "featured", Kind: "FEATURED_ITEMS", TitleKey: "home.featured", Enabled: true, Priority: 10},
			{ID: "categories", Kind: "CATEGORY_GRID", TitleKey: "home.categories", Enabled: true, Priority: 20},
			{ID: "recommended", Kind: "RECOMMENDATIONS", TitleKey: "home.recommended", Enabled: true, Priority: 30},
			{ID: "leaders", Kind: "LEADERBOARD", TitleKey: "home.local_leaders", Enabled: true, Priority: 40},
			{ID: "help", Kind: "HELP_SHORTCUTS", TitleKey: "home.help", Enabled: true, Priority: 50},
		},
	}
	repository, err := configcms.NewMemoryRepository(snapshot)
	if err != nil {
		return nil, err
	}
	service, err := configcms.NewService(repository, clock)
	if err != nil {
		return nil, err
	}
	return configcms.NewHandler(service)
}

func customerCatalogHandler(clock func() time.Time) (http.Handler, error) {
	repository, err := catalog.NewMemoryRepository(
		[]catalog.Category{{ID: "daily-needs", Name: "Daily needs", Priority: 10}, {ID: "local-services", Name: "Local services", Priority: 20}},
		[]catalog.Item{
			{ID: "item-milk", CategoryID: "daily-needs", Name: "Fresh milk", Summary: "One litre", Price: catalog.Money{AmountMinor: 6500, Currency: "INR"}, Available: true, SellerName: "Coimbatore Dairy", VerifiedLocalSeller: true, Description: "Fresh local milk delivered chilled.", RatingAverage: 4.7, ReviewCount: 86, Variants: []catalog.Variant{{ID: "variant-milk-1l", Label: "1 litre", Price: catalog.Money{AmountMinor: 6500, Currency: "INR"}, Available: true, StockQuantity: 50, MaxPerOrder: 10}}, SearchTerms: []string{"milk", "dairy"}},
			{ID: "item-groceries", CategoryID: "daily-needs", Name: "Weekly groceries", Summary: "Essential grocery bundle", Price: catalog.Money{AmountMinor: 120000, Currency: "INR"}, Available: true, SellerName: "Neighbourhood Mart", VerifiedLocalSeller: true, Variants: []catalog.Variant{{ID: "variant-groceries-weekly", Label: "Essential bundle", Price: catalog.Money{AmountMinor: 120000, Currency: "INR"}, Available: true, StockQuantity: 20, MaxPerOrder: 3}}, SearchTerms: []string{"grocery", "essentials"}},
			{ID: "item-sesame-oil", CategoryID: "daily-needs", Name: "Cold-pressed sesame oil", Summary: "Traditional wood-pressed local oil", Price: catalog.Money{AmountMinor: 24000, Currency: "INR"}, Available: true, SellerName: "Annam Local Foods", VerifiedLocalSeller: true, Description: "Small-batch sesame oil cold pressed from locally sourced seeds.", Specifications: map[string]string{"Origin": "Tamil Nadu", "Method": "Wood pressed", "Shelf life": "9 months"}, RatingAverage: 4.8, ReviewCount: 124, DeliveryEstimate: "In stock • earliest delivery tomorrow", Reviews: []catalog.Review{{ID: "review-sesame-001", AuthorDisplayName: "Verified customer", Score: 5, Body: "Fresh aroma and secure local packaging.", VerifiedPurchase: true, CreatedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)}}, Questions: []catalog.Question{{ID: "question-sesame-001", Question: "Is this suitable for traditional cooking?", AskedBy: "Planext4u customer", AskedAt: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC), Answer: "Yes. It is cold pressed and intended for everyday cooking.", AnsweredBy: "Annam Local Foods", AnsweredAt: pointerTime(time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC))}}, RelatedItemIDs: []string{"item-groceries"}, Variants: []catalog.Variant{{ID: "variant-sesame-oil-500ml", Label: "500ml", Price: catalog.Money{AmountMinor: 24000, Currency: "INR"}, CompareAtPrice: &catalog.Money{AmountMinor: 28000, Currency: "INR"}, Available: true, StockQuantity: 24, MaxPerOrder: 5}, {ID: "variant-sesame-oil-1l", Label: "1L", Price: catalog.Money{AmountMinor: 45000, Currency: "INR"}, CompareAtPrice: &catalog.Money{AmountMinor: 52000, Currency: "INR"}, Available: true, StockQuantity: 12, MaxPerOrder: 5}}, SearchTerms: []string{"oil", "sesame", "gingelly", "cold pressed"}},
		},
	)
	if err != nil {
		return nil, err
	}
	service, err := catalog.NewService(repository, []catalog.Zone{{
		ID: "chennai-core", Country: "IN", Locality: "Chennai", PostalCodes: []string{"600001", "600002", "600003"},
		MinimumLatitude: 12.75, MaximumLatitude: 13.35, MinimumLongitude: 79.90, MaximumLongitude: 80.50,
	}}, 5*time.Minute, clock)
	if err != nil {
		return nil, err
	}
	return catalog.NewHandler(service)
}

func pointerTime(value time.Time) *time.Time { return &value }
