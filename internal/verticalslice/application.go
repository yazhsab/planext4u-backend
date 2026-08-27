package verticalslice

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/catalog"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/gateway"
)

type Config struct {
	SigningKey []byte
	Clock      func() time.Time
	Logger     *slog.Logger
	Readiness  func() error
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

	upstream := http.NewServeMux()
	upstream.Handle("/v1/auth/exchange", authHandler{tokens: tokens, clock: config.Clock})
	upstream.Handle("/v1/bootstrap", configuration)
	upstream.Handle("/v1/home", catalogHandler)
	upstream.Handle("/v1/catalog/", catalogHandler)
	upstream.Handle("/v1/serviceability/check", catalogHandler)

	gatewayConfig := gateway.DefaultConfig(nil)
	gatewayConfig.UpstreamHandler = upstream
	gatewayConfig.Readiness = func(context.Context) error { return nil }
	if config.Readiness != nil {
		gatewayConfig.Readiness = func(context.Context) error { return config.Readiness() }
	}
	return gateway.NewHandler(gatewayConfig, tokens, config.Logger)
}

func Route(request *http.Request) string {
	switch request.URL.Path {
	case "/healthz", "/readyz", "/health/ready", "/v1/auth/exchange", "/v1/bootstrap", "/v1/home", "/v1/catalog/categories", "/v1/catalog/items", "/v1/catalog/search", "/v1/serviceability/check":
		return request.URL.Path
	default:
		if strings.HasPrefix(request.URL.Path, "/v1/catalog/items/") {
			return "/v1/catalog/items/{item_id}"
		}
		return "unmatched"
	}
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
		Flags: map[string]bool{"customer_home": true, "catalog_read": true},
		HomeSections: []configcms.HomeSection{
			{ID: "featured", Kind: "FEATURED_ITEMS", TitleKey: "home.featured", Enabled: true, Priority: 10},
			{ID: "categories", Kind: "CATEGORY_GRID", TitleKey: "home.categories", Enabled: true, Priority: 20},
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
			{ID: "item-milk", CategoryID: "daily-needs", Name: "Fresh milk", Summary: "One litre", Price: catalog.Money{AmountMinor: 6500, Currency: "INR"}, Available: true, SearchTerms: []string{"milk", "dairy"}},
			{ID: "item-groceries", CategoryID: "daily-needs", Name: "Weekly groceries", Summary: "Essential grocery bundle", Price: catalog.Money{AmountMinor: 120000, Currency: "INR"}, Available: true, SearchTerms: []string{"grocery", "essentials"}},
		},
	)
	if err != nil {
		return nil, err
	}
	service, err := catalog.NewService(repository, []catalog.Zone{{
		ID: "chennai-core", Country: "IN", Locality: "Chennai",
		MinimumLatitude: 12.75, MaximumLatitude: 13.35, MinimumLongitude: 79.90, MaximumLongitude: 80.50,
	}}, 5*time.Minute, clock)
	if err != nil {
		return nil, err
	}
	return catalog.NewHandler(service)
}
