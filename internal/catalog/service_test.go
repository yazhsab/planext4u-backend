package catalog

import (
	"context"
	"errors"
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
		ID: "zone-chennai", Country: "IN", Locality: "Chennai", MinimumLatitude: 12.8, MaximumLatitude: 13.3,
		MinimumLongitude: 80.0, MaximumLongitude: 80.4,
	}}, 15*time.Minute, func() time.Time { return now })
	point := GeoPoint{Latitude: 13.08, Longitude: 80.27, AccuracyMetres: 12, CapturedAt: now, Purpose: "LOCATION_SERVICEABILITY"}
	result, err := service.CheckServiceability("IN", point)
	if err != nil || !result.Serviceable || result.ZoneID != "zone-chennai" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	point.Purpose = "TRACKING"
	if _, err := service.CheckServiceability("IN", point); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("purpose error = %v", err)
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
