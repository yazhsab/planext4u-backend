//go:build integration

package configcms

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresConfigurationPublicationIsAtomicAuditedAndRevisionSafe(t *testing.T) {
	databaseURL := os.Getenv("CONFIGURATION_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("CONFIGURATION_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS configuration CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS configuration CASCADE`)
	})
	rolesMigration, err := os.ReadFile("../../migrations/platform/000001_service_roles.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(rolesMigration)); err != nil {
		t.Fatalf("apply service role migration: %v", err)
	}
	migration, err := os.ReadFile("../../migrations/configuration/000001_configuration.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply configuration migration: %v", err)
	}
	draftMigration, err := os.ReadFile("../../migrations/configuration/000002_page_drafts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(draftMigration)); err != nil {
		t.Fatalf("apply configuration draft migration: %v", err)
	}
	workspaceMigration, err := os.ReadFile("../../migrations/configuration/000003_workspace_drafts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(workspaceMigration)); err != nil {
		t.Fatalf("apply configuration workspace migration: %v", err)
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	snapshot := validSnapshot(now)
	snapshot.TenantID = "f4f48d51-82f1-4b4e-9fb4-b83d8dc78f2a"
	snapshot.Pages = []Page{{ID: "customer-home", Route: "/home", TitleKey: "page.home", Audience: []string{"CUSTOMER"}, Enabled: true, Blocks: []PageBlock{{ID: "hero", Kind: "HERO", Enabled: true, Content: map[string]any{"title": "Published"}}}}}
	authoring, _ := NewAuthoringService(repository, func() time.Time { return now })
	draft, err := authoring.Save(ctx, snapshot.TenantID, snapshot.Country, "admin-integration-001", snapshot.Pages[0], 0)
	if err != nil || draft.Revision != 1 {
		t.Fatalf("draft=%#v err=%v", draft, err)
	}
	drafts, err := authoring.List(ctx, snapshot.TenantID, snapshot.Country)
	if err != nil || len(drafts) != 1 || drafts[0].Page.ID != "customer-home" {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	cache, _ := NewCachedRepository(repository, time.Minute, func() time.Time { return now })
	publisher, err := NewPublisher(repository, cache, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	workspaceAuthoring, _ := NewWorkspaceAuthoringService(repository, func() time.Time { return now })
	workspaceDraft, err := workspaceAuthoring.Save(ctx, snapshot.TenantID, snapshot.Country, "admin-integration-001", workspaceFromSnapshot(snapshot), 0)
	if err != nil || workspaceDraft.Revision != 1 {
		t.Fatalf("workspace draft=%#v err=%v", workspaceDraft, err)
	}
	publication, _ := NewPublicationServiceWithWorkspace(repository, repository, repository, publisher, func() time.Time { return now })
	if err := publication.PublishWorkspace(ctx, snapshot.TenantID, snapshot.Country, 1, 0, "admin-integration-002", "Publish initial controlled app workspace"); err != nil {
		t.Fatal(err)
	}
	if err := publication.PublishWorkspace(ctx, snapshot.TenantID, snapshot.Country, 1, 0, "admin-integration-002", "Publish initial controlled app workspace"); err != nil {
		t.Fatalf("idempotent workspace publication: %v", err)
	}
	if err := publication.PublishPage(ctx, snapshot.TenantID, snapshot.Country, snapshot.Pages[0].ID, 1, 1, "admin-integration-003", "Publish approved customer home page"); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Get(ctx, snapshot.TenantID, snapshot.Country)
	if err != nil || stored.Revision != 2 || len(stored.Pages) != 1 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	var history int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM configuration.publish_history WHERE tenant_id = $1 AND country = $2`, snapshot.TenantID, snapshot.Country).Scan(&history); err != nil || history != 2 {
		t.Fatalf("history=%d err=%v", history, err)
	}
	snapshot.Revision, snapshot.PublishedAt = 3, now.Add(time.Minute)
	if err := publisher.Publish(ctx, snapshot, 0, "admin-integration-002", "Stale publication must fail"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale publication error=%v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM configuration.publish_history WHERE tenant_id = $1 AND country = $2`, snapshot.TenantID, snapshot.Country).Scan(&history); err != nil || history != 2 {
		t.Fatalf("history after stale publish=%d err=%v", history, err)
	}
}
