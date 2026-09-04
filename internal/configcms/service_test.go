package configcms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
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
		result, callErr := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformAndroid, test.version, "", "unsupported")
		if callErr != nil {
			t.Fatal(callErr)
		}
		if result.UpdateGate != test.want || result.Locale != "en" || !result.Maintenance || result.MaintenanceUntil == nil {
			t.Fatalf("bootstrap(%s) = %#v", test.version, result)
		}
		result.Flags["customer_home"] = false
	}
	result, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformAndroid, "1.2.0", "", "gu")
	if err != nil || !result.Flags["customer_home"] || result.Locale != "gu" || len(result.SupportedLocales) != 9 {
		t.Fatalf("snapshot was mutated or locale failed: %#v, %v", result, err)
	}
}

func TestWebBootstrapUsesDeploymentIdentityAndReloadSemantics(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	repository, err := NewMemoryRepository(validSnapshot(now))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithWebDeployment(repository, func() time.Time { return now }, "web-2026.09.02-001")
	if err != nil {
		t.Fatal(err)
	}

	current, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformWeb, "1.2.0", "web-2026.09.02-001", "en")
	if err != nil || current.Platform != PlatformWeb || current.UpdateGate != UpdateNone || current.UpdateAction != UpdateActionNone ||
		current.ClientVersion != "1.2.0" || current.ClientDeploymentID != "web-2026.09.02-001" || current.LatestDeploymentID != "web-2026.09.02-001" {
		t.Fatalf("current web bootstrap=%#v err=%v", current, err)
	}

	stale, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformWeb, "1.2.0", "web-2026.09.01-009", "en")
	if err != nil || stale.UpdateGate != UpdateRequired || stale.UpdateAction != UpdateActionReload {
		t.Fatalf("stale deployment bootstrap=%#v err=%v", stale, err)
	}
	oldVersion, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformWeb, "0.9.9", "web-2026.09.02-001", "en")
	if err != nil || oldVersion.UpdateGate != UpdateRequired || oldVersion.UpdateAction != UpdateActionReload {
		t.Fatalf("old version bootstrap=%#v err=%v", oldVersion, err)
	}
	if _, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformWeb, "1.2.0", "", "en"); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("missing deployment identifier error=%v", err)
	}
	if _, err := service.Bootstrap(context.Background(), "tenant-synthetic", "IN", PlatformWeb, "1.2.0", "bad deployment", "en"); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unsafe deployment identifier error=%v", err)
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
		func(value *Snapshot) { value.SupportedLocales = append(value.SupportedLocales, "fr") },
		func(value *Snapshot) { value.SupportedLocales = append(value.SupportedLocales, "en") },
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

func TestPublishedPagesAreScopedSortedAndDeepCopied(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	snapshot := validSnapshot(now)
	snapshot.Pages = []Page{
		{ID: "customer-home", Route: "/home", TitleKey: "page.home", Audience: []string{"CUSTOMER"}, Enabled: true, Blocks: []PageBlock{
			{ID: "disabled", Kind: "BANNER", Enabled: false, Priority: 5, Content: map[string]any{"title": "Hidden"}},
			{ID: "categories", Kind: "CATEGORY_RAIL", Enabled: true, Priority: 20, Content: map[string]any{"title": "Categories", "items": []any{"shop", "services"}}},
			{ID: "hero", Kind: "HERO", Enabled: true, Priority: 10, Content: map[string]any{"title": "Local first"}},
		}},
		{ID: "disabled-page", Route: "/disabled", TitleKey: "page.disabled", Audience: []string{"CUSTOMER"}, Enabled: false, Blocks: []PageBlock{}},
	}
	repository, err := NewMemoryRepository(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(repository, func() time.Time { return now })
	document, err := service.Page(context.Background(), snapshot.TenantID, snapshot.Country, "customer-home", "ta")
	if err != nil || document.Locale != "ta" || len(document.Page.Blocks) != 2 || document.Page.Blocks[0].ID != "hero" {
		t.Fatalf("page=%#v err=%v", document, err)
	}
	document.Page.Blocks[0].Content["title"] = "mutated"
	again, _ := service.Page(context.Background(), snapshot.TenantID, snapshot.Country, "customer-home", "ta")
	if again.Page.Blocks[0].Content["title"] != "Local first" {
		t.Fatalf("page content alias leaked: %#v", again.Page.Blocks[0].Content)
	}
	bootstrap, _ := service.Bootstrap(context.Background(), snapshot.TenantID, snapshot.Country, PlatformAndroid, "1.2.0", "", "en")
	if len(bootstrap.Pages) != 1 || bootstrap.Pages[0].ID != "customer-home" {
		t.Fatalf("bootstrap pages=%#v", bootstrap.Pages)
	}
}

func TestPageBlockRejectsNestedSecrets(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot(time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC))
	snapshot.Pages = []Page{{ID: "customer-home", Route: "/home", TitleKey: "page.home", Audience: []string{"CUSTOMER"}, Enabled: true, Blocks: []PageBlock{{ID: "hero", Kind: "HERO", Enabled: true, Content: map[string]any{"provider": map[string]any{"access_token": "must-not-publish"}}}}}}
	if _, err := NewMemoryRepository(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("nested secret validation error=%v", err)
	}
}

func TestPageDraftAuthoringUsesOptimisticRevisionAndSafeCopies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	repository := NewMemoryDraftRepository()
	service, _ := NewAuthoringService(repository, func() time.Time { return now })
	page := Page{ID: "vendor-home", Route: "/vendor/home", TitleKey: "page.vendor.home", Audience: []string{"VENDOR"}, Enabled: true, Blocks: []PageBlock{{ID: "orders", Kind: "ORDER_QUEUE", Enabled: true, Content: map[string]any{"title": "Orders"}}}}
	draft, err := service.Save(context.Background(), "tenant-synthetic", "IN", "admin-content-001", page, 0)
	if err != nil || draft.Revision != 1 {
		t.Fatalf("draft=%#v err=%v", draft, err)
	}
	if _, err := service.Save(context.Background(), "tenant-synthetic", "IN", "admin-content-002", page, 0); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale draft error=%v", err)
	}
	draft.Page.Blocks[0].Content["title"] = "mutated"
	values, err := service.List(context.Background(), "tenant-synthetic", "IN")
	if err != nil || len(values) != 1 || values[0].Page.Blocks[0].Content["title"] != "Orders" {
		t.Fatalf("draft values=%#v err=%v", values, err)
	}
}

func TestApprovedCMSOperationPublishesDraftAndAuditExactlyOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	initial := validSnapshot(now.Add(-time.Hour))
	snapshots, _ := NewMemoryRepository(initial)
	drafts := NewMemoryDraftRepository()
	authoring, _ := NewAuthoringService(drafts, clock)
	page := Page{ID: "customer-home", Route: "/home", TitleKey: "page.home", Audience: []string{"CUSTOMER"}, Enabled: true, Blocks: []PageBlock{{ID: "hero", Kind: "HERO", Enabled: true, Content: map[string]any{"title": "Approved content"}}}}
	if _, err := authoring.Save(context.Background(), initial.TenantID, initial.Country, "admin-requester", page, 0); err != nil {
		t.Fatal(err)
	}
	cache, _ := NewCachedRepository(snapshots, time.Minute, clock)
	audit := &MemoryAuditSink{}
	publisher, _ := NewPublisher(snapshots, cache, audit, clock)
	publication, _ := NewPublicationService(snapshots, drafts, publisher, clock)
	cmsExecutor, _ := NewAdminOperationExecutor(publication)
	fallback, _ := adminops.NewMemoryDomainExecutor(clock)
	router, _ := adminops.NewRoutingExecutor(fallback, map[adminops.Domain]adminops.Executor{adminops.DomainCMS: cmsExecutor})
	operations, _ := adminops.NewServiceWithExecutor(clock, router)
	requester := adminops.Principal{TenantID: initial.TenantID, Country: initial.Country, SubjectID: "admin-requester", Capabilities: map[string]bool{adminops.CapabilityCMS: true}, AuthMethods: []string{"webauthn"}, AuthenticatedAt: now}
	change, err := operations.Submit(requester, adminops.Command{Domain: adminops.DomainCMS, Action: adminops.ActionCMSPublish, TargetID: page.ID, Reason: "Publish independently approved home page", CorrelationID: "corr-cms-publish-001", Payload: map[string]any{"expected_draft_revision": int64(1), "expected_snapshot_revision": initial.Revision}})
	if err != nil || change.Status != adminops.StatusPending {
		t.Fatalf("change=%#v err=%v", change, err)
	}
	approver := requester
	approver.SubjectID = "admin-approver"
	approved, err := operations.Approve(approver, change.ID, change.Revision)
	if err != nil || approved.Status != adminops.StatusExecuted {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	stored, err := snapshots.Get(context.Background(), initial.TenantID, initial.Country)
	if err != nil || stored.Revision != 2 || len(stored.Pages) != 1 || stored.Pages[0].Blocks[0].Content["title"] != "Approved content" || len(audit.Records()) != 1 {
		t.Fatalf("stored=%#v audit=%#v err=%v", stored, audit.Records(), err)
	}
	if err := cmsExecutor.Execute(approver, change); err != nil || len(audit.Records()) != 1 {
		t.Fatalf("idempotent execution err=%v audit=%#v", err, audit.Records())
	}
}

func TestApprovedCMSWorkspaceOperationCreatesFirstPublishedSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	snapshots, _ := NewMemoryRepository()
	pages := NewMemoryDraftRepository()
	workspaces := NewMemoryWorkspaceDraftRepository()
	workspaceAuthoring, _ := NewWorkspaceAuthoringService(workspaces, clock)
	seed := validSnapshot(now)
	if _, err := workspaceAuthoring.Save(context.Background(), seed.TenantID, seed.Country, "admin-requester", workspaceFromSnapshot(seed), 0); err != nil {
		t.Fatal(err)
	}
	cache, _ := NewCachedRepository(snapshots, time.Minute, clock)
	auditSink := &MemoryAuditSink{}
	publisher, _ := NewPublisher(snapshots, cache, auditSink, clock)
	publication, _ := NewPublicationServiceWithWorkspace(snapshots, pages, workspaces, publisher, clock)
	cmsExecutor, _ := NewAdminOperationExecutor(publication)
	fallback, _ := adminops.NewMemoryDomainExecutor(clock)
	router, _ := adminops.NewRoutingExecutor(fallback, map[adminops.Domain]adminops.Executor{adminops.DomainCMS: cmsExecutor})
	operations, _ := adminops.NewServiceWithExecutor(clock, router)
	requester := adminops.Principal{TenantID: seed.TenantID, Country: seed.Country, SubjectID: "admin-requester", Capabilities: map[string]bool{adminops.CapabilityCMS: true}, AuthMethods: []string{"webauthn"}, AuthenticatedAt: now}
	change, err := operations.Submit(requester, adminops.Command{
		Domain: adminops.DomainCMS, Action: adminops.ActionCMSPublish, TargetID: "workspace",
		Reason: "Publish the approved initial mobile workspace", CorrelationID: "corr-cms-workspace-001",
		Payload: map[string]any{"publication_type": "WORKSPACE", "expected_draft_revision": int64(1), "expected_snapshot_revision": int64(0)},
	})
	if err != nil || change.Status != adminops.StatusPending {
		t.Fatalf("change=%#v err=%v", change, err)
	}
	approver := requester
	approver.SubjectID = "admin-approver"
	if _, err := operations.Approve(approver, change.ID, change.Revision); err != nil {
		t.Fatal(err)
	}
	published, err := snapshots.Get(context.Background(), seed.TenantID, seed.Country)
	if err != nil || published.Revision != 1 || len(published.Pages) != 0 || !published.Flags["customer_home"] || len(auditSink.Records()) != 1 {
		t.Fatalf("published=%#v audit=%#v err=%v", published, auditSink.Records(), err)
	}
}

func validSnapshot(now time.Time) Snapshot {
	return Snapshot{
		TenantID: "tenant-synthetic", Country: "IN", Revision: 1, PublishedAt: now,
		MinimumVersions:  map[Platform]string{PlatformAndroid: "1.0.0", PlatformIOS: "1.0.0", PlatformWeb: "1.0.0"},
		LatestVersions:   map[Platform]string{PlatformAndroid: "1.2.0", PlatformIOS: "1.2.0", PlatformWeb: "1.2.0"},
		SupportedLocales: []string{"en", "ta", "hi", "te", "kn", "ml", "mr", "bn", "gu"}, DefaultLocale: "en",
		ConsentPolicies: []ConsentPolicy{{Purpose: "ANALYTICS", PolicyVersion: "privacy-2026-01", Required: false}},
		Flags:           map[string]bool{"customer_home": true, "catalog_search": false},
		HomeSections: []HomeSection{
			{ID: "categories", Kind: "CATEGORY_RAIL", TitleKey: "home.categories", Enabled: true, Priority: 20},
			{ID: "hero", Kind: "HERO", TitleKey: "home.hero", Enabled: true, Priority: 10},
		},
	}
}
