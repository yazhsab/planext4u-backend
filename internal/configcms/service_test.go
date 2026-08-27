package configcms

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBootstrapVersionLocaleMaintenanceAndSafeCopies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	snapshot := validSnapshot(now)
	snapshot.MaintenanceWindow = &MaintenanceWindow{
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), Message: "Synthetic maintenance",
	}
	repository, err := NewMemoryRepository(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		version string
		want    UpdateGate
	}{
		{version: "0.9.9", want: UpdateRequired},
		{version: "1.0.0", want: UpdateOptional},
		{version: "1.2.0", want: UpdateNone},
	}
	for _, test := range tests {
		result, callErr := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformAndroid, test.version, "unsupported")
		if callErr != nil {
			t.Fatal(callErr)
		}
		if result.UpdateGate != test.want || result.Locale != "en" || !result.Maintenance || result.MaintenanceUntil == nil {
			t.Fatalf("bootstrap(%s) = %#v", test.version, result)
		}
		result.Flags["customer_home"] = false
	}
	result, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformAndroid, "1.2.0", "ta")
	if err != nil || !result.Flags["customer_home"] || result.Locale != "ta" {
		t.Fatalf("snapshot was mutated or locale failed: %#v, %v", result, err)
	}
}

func TestPublishInvalidatesCacheAndAppendsAudit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	initial := validSnapshot(now)
	repository, _ := NewMemoryRepository(initial)
	cache, _ := NewCachedRepository(repository, time.Hour, func() time.Time { return now })
	if _, err := cache.Get(context.Background(), initial.TenantID, initial.Country); err != nil {
		t.Fatal(err)
	}
	audit := &MemoryAuditSink{}
	publisher, _ := NewPublisher(repository, cache, audit, func() time.Time { return now })
	next := validSnapshot(now.Add(time.Minute))
	next.Revision = 2
	next.Flags["new_feature"] = true

	if err := publisher.Publish(context.Background(), next, 1, "admin-synthetic", "approved release"); err != nil {
		t.Fatal(err)
	}
	cached, err := cache.Get(context.Background(), next.TenantID, next.Country)
	if err != nil || cached.Revision != 2 || !cached.Flags["new_feature"] {
		t.Fatalf("cache = %#v, %v", cached, err)
	}
	records := audit.Records()
	if len(records) != 1 || records[0].PreviousRevision != 1 || records[0].NewRevision != 2 || records[0].ActorID != "admin-synthetic" {
		t.Fatalf("audit = %#v", records)
	}
	if err := publisher.Publish(context.Background(), next, 1, "admin-synthetic", "retry"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestSnapshotValidationFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	tests := []func(*Snapshot){
		func(value *Snapshot) { value.MinimumVersions[PlatformAndroid] = "2.0.0" },
		func(value *Snapshot) { value.DefaultLocale = "fr" },
		func(value *Snapshot) { value.HomeSections = append(value.HomeSections, value.HomeSections[0]) },
		func(value *Snapshot) {
			value.MaintenanceWindow = &MaintenanceWindow{StartsAt: now, EndsAt: now, Message: "bad"}
		},
	}
	for _, mutate := range tests {
		value := validSnapshot(now)
		mutate(&value)
		if _, err := NewMemoryRepository(value); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("validation error = %v", err)
		}
	}
}

func validSnapshot(now time.Time) Snapshot {
	return Snapshot{
		TenantID: "tenant-synthetic", Country: "IN", Revision: 1, PublishedAt: now,
		MinimumVersions:  map[Platform]string{PlatformAndroid: "1.0.0", PlatformIOS: "1.0.0"},
		LatestVersions:   map[Platform]string{PlatformAndroid: "1.2.0", PlatformIOS: "1.2.0"},
		SupportedLocales: []string{"en", "ta"}, DefaultLocale: "en",
		ConsentPolicies: []ConsentPolicy{{Purpose: "ANALYTICS", PolicyVersion: "privacy-2026-01", Required: false}},
		Flags:           map[string]bool{"customer_home": true, "catalog_search": false},
		HomeSections: []HomeSection{
			{ID: "categories", Kind: "CATEGORY_RAIL", TitleKey: "home.categories", Enabled: true, Priority: 20},
			{ID: "hero", Kind: "HERO", TitleKey: "home.hero", Enabled: true, Priority: 10},
		},
	}
}
