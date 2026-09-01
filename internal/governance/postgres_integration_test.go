//go:build integration

package governance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	governanceTenantOne = "14000000-0000-4000-8000-000000000001"
	governanceTenantTwo = "14000000-0000-4000-8000-000000000002"
	governanceAdmin     = "24000000-0000-4000-8000-000000000001"
)

func TestPostgresGovernancePublishedCountryScopeMaskingAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("GOVERNANCE_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("GOVERNANCE_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS governance CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS governance CASCADE`)
	})
	for _, path := range []string{"../../migrations/platform/000008_admin_roles.up.sql", "../../migrations/platform/000016_governance_roles.up.sql", "../../migrations/governance/000001_governance_dashboard.up.sql"} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	for _, control := range []struct{ tenant, country, currency string }{{governanceTenantOne, "IN", "INR"}, {governanceTenantOne, "NG", "NGN"}, {governanceTenantTwo, "IN", "INR"}} {
		_, err := pool.Exec(ctx, `INSERT INTO governance.country_controls (tenant_id,country,currency,locales,feature_flags,policy_version,revision,published_at,published_by_identity_id) VALUES ($1,$2,$3,ARRAY['en'],$4,'governance-v1',1,$5,$6)`, control.tenant, control.country, control.currency, []byte(`{"socio":true,"homes":true,"classifieds":true,"emergency":true}`), now, governanceAdmin)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = pool.Exec(ctx, `INSERT INTO governance.report_cards (tenant_id,country,id,title,domain,metric,value,unit,freshness,published) VALUES
		($1,'IN','orders-completed','Orders','commerce','completed',1234,'count',$2,true),
		($1,'IN','revenue-net','Revenue','finance','net',884400,'minor_currency',$2,true),
		($1,'IN','draft-hidden','Draft','finance','draft',1,'count',$2,false),
		($1,'NG','orders-ng','Nigeria orders','commerce','completed',99,'count',$2,true)`, governanceTenantOne, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO governance.map_cells (tenant_id,country,region_code,label,count,intensity) VALUES ($1,'IN','IN-TN-CHN','Chennai',281,82)`, governanceTenantOne); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO governance.leaderboard_entries (tenant_id,country,board,rank,masked_label,score,badge) VALUES ($1,'IN','community',1,'Loc***ide',980,'Community guide')`, governanceTenantOne); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO governance.insights (tenant_id,country,id,title,summary,confidence,evidence,generated_at) VALUES ($1,'IN','evening-demand','Evening demand','Local-service demand peaks between 18:00 and 20:00.','HIGH',ARRAY['30-day bookings','minimum cohort 100'],$2)`, governanceTenantOne, now); err != nil {
		t.Fatal(err)
	}
	service, err := NewPostgresService(pool, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	actor := Actor{TenantID: governanceTenantOne, Country: "IN", Subject: governanceAdmin, Roles: []string{"COUNTRY_ADMIN"}, MFAVerified: true}
	view, err := service.Dashboard(actor)
	if err != nil || view.PrivacyMode != "aggregate_and_masked" || len(view.Countries) != 1 || len(view.Reports) != 2 || len(view.MapCells) != 1 || len(view.Leaderboard) != 1 || len(view.Insights) != 1 {
		t.Fatalf("dashboard=%#v err=%v", view, err)
	}
	if !view.Reports[0].Masked || view.Reports[0].ExportPolicy != "MFA_AND_AUDIT_REQUIRED" || view.MapCells[0].Precision != "aggregate_region" || !view.Leaderboard[0].PIIMasked {
		t.Fatalf("privacy controls missing: %#v", view)
	}
	super := actor
	super.Roles = []string{"SUPER_ADMIN"}
	view, err = service.Dashboard(super)
	if err != nil || len(view.Countries) != 2 || len(view.Reports) != 2 {
		t.Fatalf("super dashboard=%#v err=%v", view, err)
	}
	withoutMFA := actor
	withoutMFA.MFAVerified = false
	if _, err := service.Dashboard(withoutMFA); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("MFA err=%v", err)
	}
	wrongTenant := actor
	wrongTenant.TenantID = governanceTenantTwo
	wrongTenant.Country = "NG"
	if _, err := service.Dashboard(wrongTenant); !errors.Is(err, ErrForbidden) {
		t.Fatalf("tenant isolation err=%v", err)
	}
}
