package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCatalogPaginationSearchAndItem(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(syntheticCategories(), syntheticItems())
	service, _ := NewService(repository, nil, 15*time.Minute, func() time.Time { return now })

	first, err := service.Items(context.Background(), "tenant-synthetic", "IN", "", "", "", 2)
	if err != nil || len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" || first.ProjectionStatus != ProjectionFresh {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := service.Items(context.Background(), "tenant-synthetic", "IN", "", "", first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 || second.HasMore || second.Items[0].ID == first.Items[1].ID {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	search, err := service.Items(context.Background(), "tenant-synthetic", "IN", "", "filter", "", 20)
	if err != nil || len(search.Items) != 1 || search.Items[0].ID != "item-water-filter" {
		t.Fatalf("search = %#v, %v", search, err)
	}
	item, status, err := service.Item(context.Background(), "tenant-synthetic", "IN", "item-milk")
	if err != nil || item.Name != "Fresh milk" || status != ProjectionFresh {
		t.Fatalf("item = %#v %s %v", item, status, err)
	}
	if _, err := service.Items(context.Background(), "tenant-synthetic", "IN", "", "", "not-a-cursor", 20); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cursor error = %v", err)
	}
	trending, _, err := service.Suggestions(context.Background(), "tenant-synthetic", "IN", "")
	if err != nil || len(trending) == 0 || trending[0].Type != "TRENDING" {
		t.Fatalf("trending=%#v err=%v", trending, err)
	}
	suggestions, _, err := service.Suggestions(context.Background(), "tenant-synthetic", "IN", "filter")
	if err != nil || len(suggestions) == 0 || suggestions[0].ItemID != "item-water-filter" {
		t.Fatalf("suggestions=%#v err=%v", suggestions, err)
	}
}

func TestCustomerQuestionIsAuthenticatedDataAndRateBounded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(syntheticCategories(), syntheticItems())
	service, _ := NewService(repository, nil, 15*time.Minute, func() time.Time { return now })
	created, err := service.AskQuestion(context.Background(), "tenant-synthetic", "IN", "customer-001", "item-milk", "command-001", "Is this delivered chilled?")
	if err != nil || created.Question != "Is this delivered chilled?" || created.AskedByID != "customer-001" || created.AskedAt != now {
		t.Fatalf("created question = %#v err=%v", created, err)
	}
	item, _, err := service.Item(context.Background(), "tenant-synthetic", "IN", "item-milk")
	if err != nil || len(item.Questions) != 1 || item.Questions[0].ID != created.ID {
		t.Fatalf("item questions = %#v err=%v", item.Questions, err)
	}
	replayed, err := service.AskQuestion(context.Background(), "tenant-synthetic", "IN", "customer-001", "item-milk", "command-001", "Is this delivered chilled?")
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("idempotent replay = %#v err=%v", replayed, err)
	}
	for index := 1; index < 5; index++ {
		if _, err := service.AskQuestion(context.Background(), "tenant-synthetic", "IN", "customer-001", "item-milk", fmt.Sprintf("command-%03d", index+1), fmt.Sprintf("Customer question number %d?", index+1)); err != nil {
			t.Fatalf("question %d error = %v", index+1, err)
		}
	}
	if _, err := service.AskQuestion(context.Background(), "tenant-synthetic", "IN", "customer-001", "item-milk", "command-006", "One question too many?"); !errors.Is(err, ErrQuestionLimit) {
		t.Fatalf("limit error = %v", err)
	}
}

func TestProjectionTransitionsFreshStaleDegraded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	clockValue := now
	repository, _ := NewMemoryRepository(syntheticCategories(), syntheticItems())
	service, _ := NewService(repository, nil, 15*time.Minute, func() time.Time { return clockValue })

	fresh, _ := service.Home(context.Background(), "tenant-synthetic", "IN")
	repository.SetError(errors.New("synthetic dependency outage"))
	clockValue = now.Add(5 * time.Minute)
	stale, _ := service.Home(context.Background(), "tenant-synthetic", "IN")
	clockValue = now.Add(20 * time.Minute)
	degraded, _ := service.Home(context.Background(), "tenant-synthetic", "IN")

	if fresh.ProjectionStatus != ProjectionFresh || stale.ProjectionStatus != ProjectionStale || len(stale.FeaturedItems) == 0 ||
		degraded.ProjectionStatus != ProjectionDegraded || len(degraded.FeaturedItems) != 0 || len(degraded.Categories) != 0 {
		t.Fatalf("states = fresh:%#v stale:%#v degraded:%#v", fresh, stale, degraded)
	}
}

func TestServiceabilityRequiresPurposeAndMatchesCountryZone(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(syntheticCategories(), syntheticItems())
	service, _ := NewService(repository, []Zone{{
		TenantID: "tenant-synthetic", ID: "zone-chennai", Country: "IN", Locality: "Chennai", MinimumLatitude: 12.8, MaximumLatitude: 13.3,
		MinimumLongitude: 80.0, MaximumLongitude: 80.4, PostalCodes: []string{"600001"},
	}}, 15*time.Minute, func() time.Time { return now })
	point := GeoPoint{Latitude: 13.08, Longitude: 80.27, AccuracyMetres: 12, CapturedAt: now, Purpose: "LOCATION_SERVICEABILITY"}
	result, err := service.CheckServiceability("tenant-synthetic", "IN", point)
	if err != nil || !result.Serviceable || result.ZoneID != "zone-chennai" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	point.Purpose = "TRACKING"
	if _, err := service.CheckServiceability("tenant-synthetic", "IN", point); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("purpose error = %v", err)
	}
	candidates, err := service.Geocode("tenant-synthetic", "IN", "600")
	if err != nil || len(candidates) != 1 || candidates[0].PostalCode != "600001" || candidates[0].Locality != "Chennai" {
		t.Fatalf("geocode = %#v err=%v", candidates, err)
	}
}

func TestProductDetailSnapshotsAreValidatedAndDefensivelyCloned(t *testing.T) {
	t.Parallel()
	compare := Money{AmountMinor: 52000, Currency: "INR"}
	items := []Item{{
		ID: "item-oil", CategoryID: "daily-needs", Name: "Sesame oil", Summary: "Cold pressed",
		Price: Money{AmountMinor: 45000, Currency: "INR"}, Available: true, RatingAverage: 4.8, ReviewCount: 12,
		Specifications: map[string]string{"Origin": "Tamil Nadu"},
		Variants:       []Variant{{ID: "variant-oil-1l", Label: "1L", Price: Money{AmountMinor: 45000, Currency: "INR"}, CompareAtPrice: &compare, Available: true, StockQuantity: 5, MaxPerOrder: 2}},
	}}
	repository, err := NewMemoryRepository(syntheticCategories(), items)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(repository, nil, 15*time.Minute, func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) })
	first, _, _ := service.Item(context.Background(), "tenant-synthetic", "IN", "item-oil")
	first.Specifications["Origin"] = "mutated"
	first.Variants[0].CompareAtPrice.AmountMinor = 1
	second, _, _ := service.Item(context.Background(), "tenant-synthetic", "IN", "item-oil")
	if second.Specifications["Origin"] != "Tamil Nadu" || second.Variants[0].CompareAtPrice.AmountMinor != 52000 {
		t.Fatalf("catalog snapshot was mutated: %#v", second)
	}

	invalid := items
	invalid[0].Variants[0].CompareAtPrice = &Money{AmountMinor: 44000, Currency: "INR"}
	if _, err := NewMemoryRepository(syntheticCategories(), invalid); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid comparison price error = %v", err)
	}
}

func TestPublicCatalogProjectionsExposeTypedMediaAndDropExpiredPresentations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	future, expired := now.Add(5*time.Minute), now.Add(-time.Second)
	icon := MediaPresentation{AssetID: "asset-category", URL: "https://media.example/category.webp", ContentType: "image/webp", Width: 256, Height: 256, AltText: "Daily needs", Variants: []ResponsiveMediaVariant{}, ExpiresAt: &future}
	active := MediaPresentation{AssetID: "asset-milk", URL: "/media/public/milk.webp", ContentType: "image/webp", Width: 1200, Height: 1200, AltText: "Fresh milk bottle", Variants: []ResponsiveMediaVariant{{URL: "https://media.example/milk-600.webp", Width: 600, Height: 600}}, ExpiresAt: &future}
	stale := active
	stale.AssetID, stale.URL, stale.ExpiresAt = "asset-stale", "https://media.example/stale.webp", &expired
	categories := []Category{{ID: "daily-needs", Name: "Daily needs", Priority: 10, Icon: &icon}}
	items := []Item{{ID: "item-milk", CategoryID: "daily-needs", Name: "Fresh milk", Summary: "One litre", Price: Money{AmountMinor: 6500, Currency: "INR"}, Available: true, Media: []MediaPresentation{active, stale}}}
	repository, err := NewMemoryRepository(categories, items)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(repository, nil, 15*time.Minute, func() time.Time { return now })

	home, err := service.Home(context.Background(), "tenant-synthetic", "IN")
	if err != nil || home.Categories[0].Icon == nil || home.Categories[0].Icon.AltText != "Daily needs" ||
		len(home.FeaturedItems[0].Media) != 1 || home.FeaturedItems[0].Media[0].AssetID != "asset-milk" || home.FeaturedItems[0].Media[0].AltText != "Fresh milk bottle" {
		t.Fatalf("home media=%#v err=%v", home, err)
	}
	item, _, err := service.Item(context.Background(), "tenant-synthetic", "IN", "item-milk")
	if err != nil || len(item.Media) != 1 || item.Media[0].URL != "/media/public/milk.webp" {
		t.Fatalf("item media=%#v err=%v", item.Media, err)
	}

	unsafe := items
	unsafe[0].Media = []MediaPresentation{{AssetID: "asset-unsafe", URL: "javascript:alert(1)", ContentType: "image/webp", Width: 10, Height: 10, AltText: "Unsafe", Variants: []ResponsiveMediaVariant{}}}
	if _, err := NewMemoryRepository(categories, unsafe); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe media URL error=%v", err)
	}
}

func TestCMSServiceCollectionProjectionKeepsPriceAndServiceabilityServerOwned(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(5 * time.Minute)
	media := MediaPresentation{AssetID: "asset-tv-repair", URL: "https://media.example/tv-repair.webp", ContentType: "image/webp", Width: 1200, Height: 900, AltText: "Technician repairing a television", Variants: []ResponsiveMediaVariant{}, ExpiresAt: &expiresAt}
	projections := []ServiceCollectionProjection{{
		Collection: ServiceCollection{CollectionID: "popular-services", Title: "Popular services", Items: []ServiceCollectionItem{
			{ServiceID: "service-tv-repair", ProviderID: "provider-tv-001", Title: "TV repair", Summary: "Verified local technician", Media: &media, Price: Money{AmountMinor: 49900, Currency: "INR"}, PriceDisplay: "From ₹499.00", Trust: ServiceTrustSummary{VerifiedProvider: true, RatingAverage: 4.8, CompletedBookings: 241}, NavigationTarget: "/app/services/service-tv-repair"},
			{ServiceID: "service-ac-repair", ProviderID: "provider-ac-001", Title: "AC repair", Summary: "Inspection and repair", Price: Money{AmountMinor: 69900, Currency: "INR"}, PriceDisplay: "From ₹699.00", Trust: ServiceTrustSummary{VerifiedProvider: true, RatingAverage: 4.7, CompletedBookings: 180}, NavigationTarget: "/app/services/service-ac-repair"},
		}},
		ServicePostalCodes: map[string][]string{"service-tv-repair": {"641001"}, "service-ac-repair": {"600001"}},
	}}
	repository, err := NewMemoryRepositoryWithServiceCollections(syntheticCategories(), syntheticItems(), projections)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(repository, nil, 15*time.Minute, func() time.Time { return now })

	home, err := service.Home(context.Background(), "tenant-synthetic", "IN", "641001")
	collection, found := home.ServiceCollections["popular-services"]
	if err != nil || !found || len(collection.Items) != 2 {
		t.Fatalf("service collections=%#v err=%v", home.ServiceCollections, err)
	}
	if collection.Items[0].Price.AmountMinor != 49900 || collection.Items[0].PriceDisplay != "From ₹499.00" || !collection.Items[0].Serviceable || collection.Items[1].Serviceable {
		t.Fatalf("service authority=%#v", collection.Items)
	}
	if _, invented := home.ServiceCollections["unknown-cms-collection"]; invented {
		t.Fatal("unknown CMS collection was invented")
	}
	if _, err := service.Home(context.Background(), "tenant-synthetic", "IN", "unsafe postal code"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid postal code error=%v", err)
	}
}

func syntheticCategories() []Category {
	return []Category{{ID: "daily-needs", Name: "Daily needs", Priority: 10}, {ID: "home-services", Name: "Home services", Priority: 20}}
}

func syntheticItems() []Item {
	return []Item{
		{ID: "item-milk", CategoryID: "daily-needs", Name: "Fresh milk", Summary: "One litre", Price: Money{AmountMinor: 6500, Currency: "INR"}, Available: true},
		{ID: "item-plumber", CategoryID: "home-services", Name: "Plumber visit", Summary: "Inspection", Price: Money{AmountMinor: 29900, Currency: "INR"}, Available: true},
		{ID: "item-water-filter", CategoryID: "home-services", Name: "Water purifier service", Summary: "Filter replacement", Price: Money{AmountMinor: 79900, Currency: "INR"}, Available: false, SearchTerms: []string{"filter"}},
	}
}
