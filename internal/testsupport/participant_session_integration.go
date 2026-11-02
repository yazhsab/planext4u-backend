//go:build integration

package testsupport

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

// ParticipantSession installs only missing dependencies in a disposable test
// database and creates an actual revocable participant source session.
func ParticipantSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, subject, role string) string {
	t.Helper()
	for _, migration := range []struct{ check, file string }{
		{`SELECT to_regclass('identity.sessions') IS NOT NULL`, "identity/000001_identity"},
		{`SELECT to_regprocedure('identity.lock_participant_session(text,character,text,text,text)') IS NOT NULL`, "identity/000011_participant_session_authority"},
		{`SELECT coalesce(position('account_deletion_requests' in pg_get_functiondef(to_regprocedure('identity.lock_participant_session(text,character,text,text,text)')))>0,false)`, "identity/000012_due_deletion_participant_barrier"},
	} {
		var exists bool
		if err := pool.QueryRow(ctx, migration.check).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			data, err := os.ReadFile("../../migrations/" + migration.file + ".up.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, string(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	source := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at) VALUES($1,$2,'test',$1,clock_timestamp()) ON CONFLICT DO NOTHING`, subject, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.identity_roles(identity_id,role,granted_at) VALUES($1,$2,clock_timestamp()) ON CONFLICT DO NOTHING`, subject, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.sessions(id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at) VALUES($1,$2,$3,'IN','synthetic-test-device','synthetic-test-device',clock_timestamp()+interval '1 hour',clock_timestamp(),clock_timestamp())`, source, subject, tenant); err != nil {
		t.Fatal(err)
	}
	return source
}
