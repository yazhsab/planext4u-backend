//go:build integration

// Package testsupport contains fixtures for explicitly disposable integration
// databases only. It is absent from production builds.
package testsupport

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

func PrivateIdentityMedia(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, owner string, ids []string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS media CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS media CASCADE`) })
	for _, name := range []string{"media/000001_media", "media/000002_media_lifecycle", "media/000003_browser_presentations", "media/000004_identity_evidence_claims"} {
		data, err := os.ReadFile("../../migrations/" + name + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	AddPrivateIdentityMedia(t, ctx, pool, tenant, owner, ids)
}

func AddPrivateIdentityMedia(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, owner string, ids []string) {
	t.Helper()
	for _, id := range ids {
		_, err := pool.Exec(ctx, `INSERT INTO media.assets(id,tenant_id,country,owner_id,object_key,content_type,size_bytes,checksum_sha256,status,classification,created_at,updated_at,purpose,lifecycle_state,upload_expires_at,ready_at,version) VALUES($1::uuid,$2,'IN',$3,$1::text,'image/png',1,repeat('a',64),'READY','RESTRICTED',now(),now(),'IDENTITY_DOCUMENT','READY',now()+interval '10 minutes',now(),2)`, id, tenant, owner)
		if err != nil {
			t.Fatal(err)
		}
	}
}
