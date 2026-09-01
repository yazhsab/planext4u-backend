//go:build integration

package adminops

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublishedControlPlaneUpdatesRuntimePoliciesGovernanceAndModeration(t *testing.T) {
	databaseURL := os.Getenv("ADMIN_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("ADMIN_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, schema := range []string{"admin", "booking", "food", "fulfillment", "social", "local_verticals", "emergency", "governance"} {
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for _, schema := range []string{"admin", "booking", "food", "fulfillment", "social", "local_verticals", "emergency", "governance"} {
			_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		}
	})
	paths := []string{
		"../../migrations/platform/000004_booking_roles.up.sql", "../../migrations/platform/000005_phase4_roles.up.sql", "../../migrations/platform/000006_phase5_roles.up.sql", "../../migrations/platform/000008_admin_roles.up.sql", "../../migrations/platform/000016_governance_roles.up.sql",
		"../../migrations/admin/000001_admin_control_plane.up.sql", "../../migrations/booking/000001_booking.up.sql", "../../migrations/booking/000002_durable_runtime.up.sql", "../../migrations/booking/000003_admin_publisher_grant.up.sql", "../../migrations/food/000001_food.up.sql", "../../migrations/food/000002_durable_order_runtime.up.sql", "../../migrations/food/000003_admin_publisher_grant.up.sql", "../../migrations/fulfillment/000001_fulfillment.up.sql", "../../migrations/fulfillment/000002_durable_driver_runtime.up.sql", "../../migrations/fulfillment/000003_admin_publisher_grant.up.sql", "../../migrations/social/000001_social_trust.up.sql", "../../migrations/social/000002_engagement_media_messaging.up.sql", "../../migrations/social/000003_durable_social_runtime.up.sql", "../../migrations/social/000004_shares_reward_leases_admin_grant.up.sql", "../../migrations/local_verticals/000001_local_verticals.up.sql", "../../migrations/local_verticals/000002_durable_marketplace_runtime.up.sql", "../../migrations/local_verticals/000003_admin_publisher_grant.up.sql", "../../migrations/emergency/000001_emergency.up.sql", "../../migrations/emergency/000002_durable_emergency_runtime.up.sql", "../../migrations/emergency/000003_admin_publisher_grant.up.sql", "../../migrations/governance/000001_governance_dashboard.up.sql", "../../migrations/platform/000017_admin_publisher_grants.up.sql",
	}
	for _, path := range paths {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `INSERT INTO social.profiles (identity_id,tenant_id,country,handle,display_name,created_at,updated_at) VALUES ($1,$2,'IN','publisher','Publisher',$3,$3)`, postgresAdminSubjectOne, postgresAdminTenantOne, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social.posts (id,tenant_id,country,author_identity_id,revision,body,status,ranking_version,created_at,updated_at) VALUES ($1,$2,'IN',$3,1,'Moderate me','PUBLISHED','old-model',$4,$4)`, "779ad22e-20f3-4aa4-aab7-833c5c49e4d7", postgresAdminTenantOne, postgresAdminSubjectOne, now); err != nil {
		t.Fatal(err)
	}
	executor, err := NewPostgresControlPlaneExecutor(pool, func() time.Time { return now })
	if err != nil || executor.Ready(ctx) != nil {
		t.Fatalf("executor err=%v", err)
	}
	principal := postgresAdminPrincipal(postgresAdminSubjectOne, postgresAdminTenantOne, now)
	principal.Capabilities[CapabilityPolicy] = true
	principal.Capabilities[CapabilityCountry] = true
	principal.Capabilities[CapabilityIntelligence] = true
	principal.Capabilities[CapabilityContent] = true

	socialPolicy := Change{ID: "change-control-social-0001", TenantID: principal.TenantID, Country: principal.Country, Status: StatusPending, Command: Command{Domain: DomainPolicy, Action: ActionPolicyPublish, TargetID: "social-policy-v2", CorrelationID: "control-social-0001", Payload: map[string]any{"vertical": "SOCIAL", "policy": map[string]any{"version": "social-v2", "ranking_model": "socio-feed-v2", "review_terms": []string{"unsafe"}, "story_ttl_seconds": 86400, "reel_ttl_seconds": 2592000, "media_retention_seconds": 2592000, "presence_ttl_seconds": 120, "call_ttl_seconds": 120, "like_reward_points": 12, "follow_reward_points": 22, "share_reward_points": 17, "reward_expiry_seconds": 31536000}}}}
	if err := executor.Execute(principal, socialPolicy); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(principal, socialPolicy); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	var version, ranking string
	var likePoints int
	if err := pool.QueryRow(ctx, `SELECT version,ranking_model,like_reward_points FROM social.policies WHERE tenant_id=$1 AND country='IN'`, principal.TenantID).Scan(&version, &ranking, &likePoints); err != nil || version != "social-v2" || ranking != "socio-feed-v2" || likePoints != 12 {
		t.Fatalf("social policy version=%s ranking=%s points=%d err=%v", version, ranking, likePoints, err)
	}

	country := Change{ID: "change-control-country-0001", TenantID: principal.TenantID, Country: principal.Country, Status: StatusPending, Command: Command{Domain: DomainCountry, Action: ActionCountryUpdate, TargetID: "country-IN", CorrelationID: "control-country-0001", Payload: map[string]any{"currency": "INR", "locales": []string{"en", "ta"}, "feature_flags": map[string]bool{"socio": true, "homes": true, "emergency": true}, "policy_version": "country-v1", "expected_revision": 0}}}
	if err := executor.Execute(principal, country); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM governance.country_controls WHERE tenant_id=$1 AND country='IN'`, principal.TenantID).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("country revision=%d err=%v", revision, err)
	}

	insight := Change{ID: "change-control-insight-0001", TenantID: principal.TenantID, Country: principal.Country, Status: StatusPending, Command: Command{Domain: DomainIntelligence, Action: ActionIntelligencePublish, TargetID: "demand-window", CorrelationID: "control-insight-0001", Payload: map[string]any{"title": "Demand window", "summary": "Demand peaks during the evening service window.", "confidence": "HIGH", "evidence": []string{"30-day bookings"}, "generated_at": now}}}
	if err := executor.Execute(principal, insight); err != nil {
		t.Fatal(err)
	}
	var published bool
	if err := pool.QueryRow(ctx, `SELECT published FROM governance.insights WHERE tenant_id=$1 AND country='IN' AND id='demand-window'`, principal.TenantID).Scan(&published); err != nil || !published {
		t.Fatalf("insight published=%t err=%v", published, err)
	}

	moderation := Change{ID: "change-control-content-0001", TenantID: principal.TenantID, Country: principal.Country, Status: StatusPending, Command: Command{Domain: DomainContent, Action: ActionContentModerate, TargetID: "779ad22e-20f3-4aa4-aab7-833c5c49e4d7", CorrelationID: "control-content-0001", Payload: map[string]any{"resource_type": "SOCIAL_POST", "decision": "REMOVE", "reason": "Confirmed against the published content policy"}}}
	if err := executor.Execute(principal, moderation); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM social.posts WHERE id=$1`, moderation.Command.TargetID).Scan(&status); err != nil || status != "REMOVED" {
		t.Fatalf("content status=%s err=%v", status, err)
	}

	invalid := socialPolicy
	invalid.ID = "change-control-invalid-0001"
	invalid.Command.Payload["unexpected"] = true
	if err := executor.Execute(principal, invalid); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown-field err=%v", err)
	}
	wrongTenant := principal
	wrongTenant.TenantID = postgresAdminTenantTwo
	if err := executor.Execute(wrongTenant, socialPolicy); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant err=%v", err)
	}
	var executionCount, recordCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM admin.domain_executions),(SELECT count(*) FROM admin.domain_records)`).Scan(&executionCount, &recordCount); err != nil || executionCount != 4 || recordCount != 4 {
		t.Fatalf("executions=%d records=%d err=%v", executionCount, recordCount, err)
	}
}
