//go:build integration

package testsupport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

// ParticipantReviewer creates a current, synthetic country reviewer only in the
// disposable integration database supplied by the owning test.
func ParticipantReviewer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, subject string) string {
	t.Helper()
	for _, migration := range []struct{ table, file string }{{"admin.sessions", "000001_admin_control_plane"}, {"admin.operator_accounts", "000004_operator_accounts"}, {"admin.operator_events", "000005_operator_administration"}} {
		var found bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, migration.table).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			data, err := os.ReadFile("../../migrations/admin/" + migration.file + ".up.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, string(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var found bool
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('admin.lock_participant_reviewer(uuid,character,uuid,uuid,bigint)') IS NOT NULL`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if !found {
		data, err := os.ReadFile("../../migrations/admin/000014_participant_review_authority.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, migration := range []struct{ check, file string }{
		{`SELECT to_regclass('admin.organization_nodes') IS NOT NULL`, "000003_organization"},
		{`SELECT to_regprocedure('admin.lock_fulfillment_operator(uuid,character,uuid,uuid,bigint)') IS NOT NULL`, "000008_fulfillment_operator_session"},
		{`SELECT to_regclass('admin.participant_territory_bindings') IS NOT NULL`, "000015_participant_territories"},
		{`SELECT to_regprocedure('admin.lock_participant_field_access(uuid,character,uuid,uuid,bigint,uuid)') IS NOT NULL`, "000019_participant_field_authority"},
	} {
		var exists bool
		if err := pool.QueryRow(ctx, migration.check).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			data, err := os.ReadFile("../../migrations/admin/" + migration.file + ".up.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, string(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	source := uuid.NewString()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	if _, err := pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Synthetic participant reviewer',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']) ON CONFLICT(tenant_id,subject_id) DO UPDATE SET roles=EXCLUDED.roles,allowed_countries=EXCLUDED.allowed_countries,active=true`, tenant, subject, "review-fixture-"+subject); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) SELECT $1,$2,$3,$4,'Synthetic participant reviewer',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']::char(2)[],'IN',clock_timestamp(),ARRAY['pwd','totp'],repeat('x',32),clock_timestamp()+interval '1 hour',clock_timestamp(),a.revision FROM admin.operator_accounts a WHERE a.tenant_id=$4 AND a.subject_id=$3`, digest, source, subject, tenant); err != nil {
		t.Fatal(err)
	}
	return source
}
