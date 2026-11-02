//go:build integration

package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func verifyPrivateEvidenceRetention(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *Service, objects *postgresTestObjects) {
	t.Helper()
	const tenant = "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f"
	const owner = "60000000-0000-4000-8000-000000000001"
	input := validRequest()
	input.Purpose = PurposeIdentityDocument
	input.AltText = ""
	input.Width = 0
	input.Height = 0
	grant, err := service.Presign(ctx, tenant, "IN", owner, input)
	if err != nil {
		t.Fatal(err)
	}
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{ContentType: input.ContentType, SizeBytes: input.SizeBytes, SHA256: input.SHA256}
	if _, err := service.Complete(ctx, tenant, owner, grant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	config.ConnConfig.RuntimeParams["role"] = "planext4u_fulfillment_runtime"
	limited, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	claimSQL := `SELECT media.claim_rider_identity_evidence($1,$2,$3,'test-rider-application',$4::uuid[])`
	for _, scope := range [][3]string{{tenant, "SG", owner}, {tenant, "IN", "someone-else"}, {"d1f47ba2-1ad1-46bf-aa23-2969a9ea6561", "IN", owner}} {
		var claimed bool
		if err := limited.QueryRow(ctx, claimSQL, scope[0], scope[1], scope[2], []string{grant.Asset.ID}).Scan(&claimed); err != nil || claimed {
			t.Fatalf("foreign evidence claim %v: %v", scope, err)
		}
	}
	var canRead, canInsert, canChange bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_fulfillment_runtime','media.assets','SELECT'),has_table_privilege('planext4u_media_runtime','media.identity_evidence_claims','INSERT'),has_table_privilege('planext4u_media_runtime','media.identity_evidence_claims','UPDATE,DELETE,TRUNCATE')`).Scan(&canRead, &canInsert, &canChange); err != nil || canRead || canInsert || canChange {
		t.Fatalf("retention privileges: %v %v %v %v", canRead, canInsert, canChange, err)
	}
	// A business rollback must not retain an unsubmitted document.
	tx, err := limited.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var claimed bool
	if err := tx.QueryRow(ctx, claimSQL, tenant, "IN", owner, []string{grant.Asset.ID}).Scan(&claimed); err != nil || !claimed {
		t.Fatalf("private claim: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media.identity_evidence_claims WHERE asset_id=$1`, grant.Asset.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback retained orphan: %v %d", err, count)
	}
	tx, err = limited.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, claimSQL, tenant, "IN", owner, []string{grant.Asset.ID}).Scan(&claimed); err != nil || !claimed {
		t.Fatalf("claim before commit: %v", err)
	}
	result := make(chan error, 1)
	go func() { result <- service.DeleteScoped(ctx, tenant, "IN", owner, grant.Asset.ID) }()
	select {
	case err := <-result:
		t.Fatalf("delete raced uncommitted application: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrRetained) {
			t.Fatalf("delete ignored accepted claim: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if objects.deleted[grant.Asset.ObjectKey] {
		t.Fatal("retained private bytes were deleted")
	}
	if _, err := service.GetScoped(ctx, tenant, "IN", owner, grant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	// The DB trigger also protects against an accidental direct runtime update.
	if _, err := pool.Exec(ctx, `UPDATE media.assets SET lifecycle_state='DELETED',status='DELETED' WHERE id=$1`, grant.Asset.ID); err == nil {
		t.Fatal("retained evidence bypassed database guard")
	}
}
