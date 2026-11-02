//go:build integration

package media

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMediaLifecycleIsDurableTenantScopedAndVersioned(t *testing.T) {
	databaseURL := os.Getenv("MEDIA_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("MEDIA_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	preUpgradeTenant := "cfde93c8-8b2b-443e-bc7d-fddcab3d6a5a"
	var preUpgradeAsset Asset
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS media CASCADE; DROP SCHEMA IF EXISTS supply CASCADE; DROP SCHEMA IF EXISTS fulfillment CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS media CASCADE; DROP SCHEMA IF EXISTS supply CASCADE; DROP SCHEMA IF EXISTS fulfillment CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/media/000001_media.up.sql",
		"../../migrations/media/000002_media_lifecycle.up.sql",
		"../../migrations/media/000003_browser_presentations.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000003_private_evidence_reference.up.sql",
		"../../migrations/fulfillment/000001_fulfillment.up.sql",
		"../../migrations/fulfillment/000007_private_evidence_reference.up.sql",
		"../../migrations/media/000004_identity_evidence_claims.up.sql",
		"../../migrations/media/000005_upload_grant_provenance.up.sql",
		"../../migrations/media/000006_object_purge_coordination.up.sql",
		"../../migrations/media/000007_scoped_purge_state_lock.up.sql",
	} {
		if strings.HasSuffix(path, "000007_scoped_purge_state_lock.up.sql") {
			// Simulate the deployed 000006 state: the forward repair must preserve
			// an already-tombstoned asset and its pending purge row.
			beforeRepository, createErr := NewPostgresRepository(pool)
			if createErr != nil {
				t.Fatal(createErr)
			}
			beforeService, createErr := NewService(beforeRepository, postgresTestSigner{},
				&postgresTestObjects{metadata: map[string]ObjectMetadata{}, deleted: map[string]bool{}},
				postgresTestScanner{}, 10*time.Minute, time.Now)
			if createErr != nil {
				t.Fatal(createErr)
			}
			beforeGrant, createErr := beforeService.Presign(ctx, preUpgradeTenant, "IN", "owner-before-upgrade", validRequest())
			if createErr != nil {
				t.Fatal(createErr)
			}
			preUpgradeAsset = beforeGrant.Asset
			if _, createErr := beforeRepository.Tombstone(ctx, preUpgradeTenant, "IN", "owner-before-upgrade", preUpgradeAsset.ID); createErr != nil {
				t.Fatal(createErr)
			}
		}
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	var preservedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media.object_purge_state WHERE asset_id=$1 AND purged_at IS NULL`, preUpgradeAsset.ID).Scan(&preservedCount); err != nil || preservedCount != 1 {
		t.Fatalf("forward migration lost an existing tombstone: count=%d err=%v", preservedCount, err)
	}
	now := time.Date(2026, 8, 31, 13, 0, 0, 123456000, time.UTC)
	objects := &postgresTestObjects{metadata: map[string]ObjectMetadata{}, deleted: map[string]bool{}}
	service, err := NewService(repository, postgresTestSigner{}, objects, postgresTestScanner{}, 10*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	verifyPrivateEvidenceRetention(t, ctx, pool, service, objects)
	tenantID := "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f"
	grant, err := service.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil || grant.Asset.Version != 1 {
		t.Fatalf("grant=%#v err=%v", grant, err)
	}
	var unknownScheme string
	var unknownExpiry *time.Time
	if err := pool.QueryRow(ctx, `SELECT grant_provenance,grant_valid_until FROM media.upload_sessions WHERE asset_id=$1`, grant.Asset.ID).Scan(&unknownScheme, &unknownExpiry); err != nil ||
		unknownScheme != GrantProvenanceUnknown || unknownExpiry != nil {
		t.Fatalf("unproved signer provenance scheme=%q expiry=%v err=%v", unknownScheme, unknownExpiry, err)
	}
	conditionalService, err := NewService(repository, conditionalPostgresTestSigner{}, objects, postgresTestScanner{}, 10*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	conditionalGrant, err := conditionalService.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	var scheme string
	var signedExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT grant_provenance,grant_valid_until FROM media.upload_sessions WHERE asset_id=$1`, conditionalGrant.Asset.ID).Scan(&scheme, &signedExpiry); err != nil ||
		scheme != GrantProvenanceAWSConditionalSigV4 || !signedExpiry.Equal(now.Truncate(time.Second).Add(15*time.Minute)) ||
		!signedExpiry.After(conditionalGrant.ExpiresAt) {
		t.Fatalf("signed grant provenance scheme=%q expiry=%v asset-expiry=%v err=%v", scheme, signedExpiry, conditionalGrant.ExpiresAt, err)
	}
	var sessionProvenanceUpdate, sessionDelete, assetScopeUpdate bool
	if err := pool.QueryRow(ctx, `SELECT
		has_column_privilege('planext4u_media_runtime','media.upload_sessions','grant_provenance','UPDATE'),
		has_table_privilege('planext4u_media_runtime','media.upload_sessions','DELETE'),
		has_column_privilege('planext4u_media_runtime','media.assets','object_key','UPDATE')`).Scan(
		&sessionProvenanceUpdate, &sessionDelete, &assetScopeUpdate); err != nil ||
		sessionProvenanceUpdate || sessionDelete || assetScopeUpdate {
		t.Fatalf("media runtime provenance/scope privileges: %v %v %v %v", sessionProvenanceUpdate, sessionDelete, assetScopeUpdate, err)
	}
	limitedConfig := pool.Config()
	limitedConfig.ConnConfig.RuntimeParams["role"] = "planext4u_media_runtime"
	limited, err := pgxpool.NewWithConfig(ctx, limitedConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if _, err := limited.Exec(ctx, `UPDATE media.upload_sessions SET grant_provenance='UNKNOWN' WHERE asset_id=$1`, conditionalGrant.Asset.ID); err == nil {
		t.Fatal("media runtime changed signed grant provenance")
	}
	if _, err := limited.Exec(ctx, `DELETE FROM media.upload_sessions WHERE asset_id=$1`, conditionalGrant.Asset.ID); err == nil {
		t.Fatal("media runtime deleted signed grant provenance")
	}
	if _, err := limited.Exec(ctx, `UPDATE media.assets SET object_key='other' WHERE id=$1`, conditionalGrant.Asset.ID); err == nil {
		t.Fatal("media runtime changed object scope")
	}
	if _, err := pool.Exec(ctx, `UPDATE media.upload_sessions SET grant_valid_until=grant_valid_until + interval '1 second' WHERE asset_id=$1`, conditionalGrant.Asset.ID); err == nil {
		t.Fatal("owner-side mutation bypassed immutable grant trigger")
	}
	runtimeRepository, err := NewPostgresRepository(limited)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeRepository.Ready(ctx); err != nil {
		t.Fatalf("media runtime schema readiness: %v", err)
	}
	runtimeRequest := validRequest()
	runtimeRequest.Purpose, runtimeRequest.AltText, runtimeRequest.Width, runtimeRequest.Height = PurposeIdentityDocument, "", 0, 0
	runtimeGrant, err := conditionalService.Presign(ctx, tenantID, "IN", "owner-postgres", runtimeRequest)
	if err != nil {
		t.Fatal(err)
	}
	runtimeService, err := NewService(runtimeRepository, conditionalPostgresTestSigner{}, objects, postgresTestScanner{}, 10*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeService.DeleteScoped(ctx, preUpgradeTenant, "IN", "owner-before-upgrade", preUpgradeAsset.ID); err != nil || !objects.deleted[preUpgradeAsset.ObjectKey] {
		t.Fatalf("forward repair could not clean an existing tombstone: err=%v deleted=%v", err, objects.deleted[preUpgradeAsset.ObjectKey])
	}
	if err := runtimeService.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", runtimeGrant.Asset.ID); err != nil {
		_, tombstoneErr := runtimeRepository.Tombstone(ctx, tenantID, "IN", "owner-postgres", runtimeGrant.Asset.ID)
		cleanupErr := runtimeRepository.DeleteTombstonedObject(ctx, tenantID, "IN", "owner-postgres", runtimeGrant.Asset.ID, objects.Delete)
		t.Fatalf("runtime role could not delete unsubmitted private media: err=%v tombstone=%v cleanup=%v", err, tombstoneErr, cleanupErr)
	}
	if !objects.deleted[runtimeGrant.Asset.ObjectKey] {
		t.Fatal("runtime role did not reach current-object deletion")
	}
	var directPurged *time.Time
	if err := limited.QueryRow(ctx, `SELECT purged_at FROM media.object_purge_state WHERE asset_id=$1 FOR UPDATE`, runtimeGrant.Asset.ID).Scan(&directPurged); err == nil {
		t.Fatal("media runtime obtained direct purge-state row-lock privilege")
	}
	for _, scope := range []struct{ tenant, country, owner string }{
		{"23bf7434-3643-49df-9928-c9169011f69d", "IN", "owner-postgres"},
		{tenantID, "NG", "owner-postgres"},
		{tenantID, "IN", "other-owner"},
	} {
		if err := limited.QueryRow(ctx, `SELECT purged_at FROM media.lock_object_purge_state($1,$2,$3,$4)`,
			runtimeGrant.Asset.ID, scope.tenant, scope.country, scope.owner).Scan(&directPurged); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("scoped purge-state lock exposed foreign asset: scope=%+v err=%v", scope, err)
		}
	}
	deleteStarted, allowDelete, runtimeDeleteDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		runtimeDeleteDone <- runtimeRepository.DeleteTombstonedObject(ctx, tenantID, "IN", "owner-postgres", runtimeGrant.Asset.ID,
			func(_ context.Context, key string) error {
				if key != runtimeGrant.Asset.ObjectKey {
					return errors.New("runtime delete key changed")
				}
				close(deleteStarted)
				<-allowDelete
				return nil
			})
	}()
	select {
	case <-deleteStarted:
	case err := <-runtimeDeleteDone:
		t.Fatalf("runtime did not enter object deletion: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	markAfterDelete := make(chan error, 1)
	go func() {
		_, markErr := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, runtimeGrant.Asset.ID)
		markAfterDelete <- markErr
	}()
	select {
	case markErr := <-markAfterDelete:
		t.Fatalf("purge mark bypassed an in-flight runtime object deletion: %v", markErr)
	case <-time.After(75 * time.Millisecond):
	}
	close(allowDelete)
	if err := <-runtimeDeleteDone; err != nil {
		t.Fatalf("runtime object deletion failed: %v", err)
	}
	if markErr := <-markAfterDelete; markErr != nil {
		t.Fatalf("purge mark did not resume after runtime deletion: %v", markErr)
	}
	targetGrant := conditionalGrant
	stored, err := repository.Get(ctx, tenantID, "owner-postgres", targetGrant.Asset.ID)
	if err != nil || stored.Country != "IN" || stored.Purpose != PurposeCatalogImage || stored.State != StatePendingUpload {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	// Browser presentation columns arrive with migration 000003; the schema is
	// only ready when they are applied, so assert they persist and round-trip.
	if stored.AltText != "Fresh milk bottle" || stored.Width != 1200 || stored.Height != 1200 {
		t.Fatalf("stored presentation alt=%q width=%d height=%d", stored.AltText, stored.Width, stored.Height)
	}
	var storedAltText string
	var storedWidth, storedHeight int
	if err := pool.QueryRow(ctx, `
		SELECT alt_text, presentation_width, presentation_height
		FROM media.assets
		WHERE tenant_id = $1 AND id = $2`, tenantID, targetGrant.Asset.ID).Scan(&storedAltText, &storedWidth, &storedHeight); err != nil {
		t.Fatal(err)
	}
	if storedAltText != "Fresh milk bottle" || storedWidth != 1200 || storedHeight != 1200 {
		t.Fatalf("presentation columns alt=%q width=%d height=%d", storedAltText, storedWidth, storedHeight)
	}
	if _, err := repository.Get(ctx, "23bf7434-3643-49df-9928-c9169011f69d", "owner-postgres", targetGrant.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant read error=%v", err)
	}
	objects.metadata[targetGrant.Asset.ObjectKey] = ObjectMetadata{
		ContentType: targetGrant.Asset.ContentType, SizeBytes: targetGrant.Asset.SizeBytes, SHA256: targetGrant.Asset.SHA256,
	}
	ready, err := service.Complete(ctx, tenantID, "owner-postgres", targetGrant.Asset.ID)
	if err != nil || ready.State != StateReady || ready.Version != 3 || ready.ReadyAt == nil {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	service.objects = inspectingDeletion{ObjectStore: objects, remove: func(context.Context, string) error {
		return errors.New("synthetic object-store outage")
	}}
	if err := service.Delete(ctx, tenantID, "owner-postgres", ready.ID); !errors.Is(err, ErrDependency) {
		t.Fatalf("failed object cleanup was reported complete: %v", err)
	}
	if _, err := repository.Get(ctx, tenantID, "owner-postgres", ready.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed cleanup reopened media access: %v", err)
	}
	if err := service.DeleteScoped(ctx, tenantID, "NG", "owner-postgres", ready.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-country cleanup retry reached storage: %v", err)
	}
	service.objects = objects
	if err := service.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", ready.ID); err != nil {
		t.Fatalf("tombstoned object cleanup did not recover: %v", err)
	}
	var queued bool
	var purgedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT true,purged_at FROM media.object_purge_state WHERE asset_id=$1`, ready.ID).Scan(&queued, &purgedAt); err != nil || !queued || purgedAt != nil {
		t.Fatalf("tombstone did not enqueue unproved version cleanup: queued=%v purged=%v err=%v", queued, purgedAt, err)
	}
	var runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete, runtimeLockExecute bool
	if err := pool.QueryRow(ctx, `SELECT
		has_table_privilege('planext4u_media_runtime','media.object_purge_state','SELECT'),
		has_table_privilege('planext4u_media_runtime','media.object_purge_state','INSERT'),
		has_table_privilege('planext4u_media_runtime','media.object_purge_state','UPDATE'),
		has_table_privilege('planext4u_media_runtime','media.object_purge_state','DELETE'),
		has_function_privilege('planext4u_media_runtime','media.lock_object_purge_state(uuid,uuid,text,text)','EXECUTE')`).Scan(
		&runtimeSelect, &runtimeInsert, &runtimeUpdate, &runtimeDelete, &runtimeLockExecute); err != nil || runtimeSelect || runtimeInsert || runtimeUpdate || runtimeDelete || !runtimeLockExecute {
		t.Fatalf("media runtime purge-state privileges: read=%v insert=%v update=%v delete=%v lock=%v err=%v", runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete, runtimeLockExecute, err)
	}
	if _, err := limited.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, ready.ID); err == nil {
		t.Fatal("media runtime forged a completed version purge")
	}
	// Simulate a future cleanup worker holding the same asset lock while it
	// records a verified zero-version result. A normal retry must wait and then
	// skip DeleteObject rather than adding a new versioned-bucket marker.
	guardTx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guardTx.Rollback(ctx) }()
	if err := guardTx.QueryRow(ctx, `SELECT true FROM media.object_purge_state WHERE asset_id=$1 FOR UPDATE`, ready.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := guardTx.QueryRow(ctx, `SELECT true FROM media.assets WHERE id=$1 FOR UPDATE`, ready.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, ready.ID); err != nil {
		t.Fatal(err)
	}
	var laterDeletes atomic.Int32
	service.objects = inspectingDeletion{ObjectStore: objects, remove: func(context.Context, string) error {
		laterDeletes.Add(1)
		return nil
	}}
	retryDone := make(chan error, 1)
	go func() { retryDone <- service.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", ready.ID) }()
	select {
	case retryErr := <-retryDone:
		t.Fatalf("normal retry bypassed the asset purge lock: %v", retryErr)
	case <-time.After(75 * time.Millisecond):
	}
	if err := guardTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if retryErr := <-retryDone; retryErr != nil || laterDeletes.Load() != 0 {
		t.Fatalf("retry added an object marker after purge lock: err=%v deletes=%d", retryErr, laterDeletes.Load())
	}
	if err := service.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", ready.ID); err != nil || laterDeletes.Load() != 0 {
		t.Fatalf("later retry added an object marker: err=%v deletes=%d", err, laterDeletes.Load())
	}
	if _, err := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=NULL WHERE asset_id=$1`, ready.ID); err == nil {
		t.Fatal("completed purge state was cleared")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media.object_purge_state WHERE asset_id=$1`, ready.ID); err == nil {
		t.Fatal("completed purge state was deleted")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE media.object_purge_state`); err == nil {
		t.Fatal("object purge evidence was truncated")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media.upload_sessions
		(id,asset_id,expires_at,created_at,grant_provenance)
		VALUES ($1,$2,now()+interval '10 minutes',now(),'UNKNOWN')`,
		"15e51ad4-12e1-4b25-9ef8-ccddcf17b235", ready.ID); err == nil {
		t.Fatal("new upload grant was inserted after a completed purge")
	}
	if _, err := limited.Exec(ctx, `INSERT INTO media.upload_sessions
		(id,asset_id,expires_at,created_at,grant_provenance)
		VALUES ($1,$2,now()+interval '10 minutes',now(),'UNKNOWN')`,
		"e903915e-083d-49df-8d87-a74e30ae6422", ready.ID); err == nil {
		t.Fatal("media runtime inserted an upload grant after a completed purge")
	}
	if err := service.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", grant.Asset.ID); err != nil {
		t.Fatalf("unknown-grant tombstone failed: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, grant.Asset.ID); err == nil {
		t.Fatal("unknown historical upload grant was marked physically purged")
	}
	liveService, err := NewService(repository, liveConditionalPostgresTestSigner{}, objects, postgresTestScanner{}, 10*time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	liveGrant, err := liveService.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := liveService.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", liveGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, liveGrant.Asset.ID); err == nil {
		t.Fatal("unexpired signed upload grant was marked physically purged")
	}
	multiGrant, err := conditionalService.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media.upload_sessions
		(id,asset_id,expires_at,created_at,grant_provenance)
		VALUES ($1,$2,now()+interval '10 minutes',now(),'UNKNOWN')`,
		"5ec74611-49f7-4f68-8842-27037f306f58", multiGrant.Asset.ID); err != nil {
		t.Fatalf("active asset rejected a second historical-style session: %v", err)
	}
	if err := conditionalService.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", multiGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, multiGrant.Asset.ID); err == nil {
		t.Fatal("one expired signed grant hid another UNKNOWN session")
	}
	noGrant, err := conditionalService.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := conditionalService.DeleteScoped(ctx, tenantID, "IN", "owner-postgres", noGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media.upload_sessions WHERE asset_id=$1`, noGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, noGrant.Asset.ID); err == nil {
		t.Fatal("asset with no surviving grant provenance was marked purged")
	}
	racingGrant, err := conditionalService.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media.object_purge_state(asset_id) VALUES($1)`, racingGrant.Asset.ID); err == nil {
		t.Fatal("active asset entered object purge coordination before tombstone")
	}
	deletionTx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = deletionTx.Rollback(ctx) }()
	if err := deletionTx.QueryRow(ctx, `SELECT true FROM media.assets WHERE id=$1 FOR UPDATE`, racingGrant.Asset.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if _, err := deletionTx.Exec(ctx, `UPDATE media.assets SET status='DELETED',lifecycle_state='DELETED',version=version+1 WHERE id=$1`, racingGrant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	insertDone := make(chan error, 1)
	go func() {
		_, insertErr := pool.Exec(ctx, `INSERT INTO media.upload_sessions
			(id,asset_id,expires_at,created_at,grant_provenance)
			VALUES ($1,$2,now()+interval '10 minutes',now(),'UNKNOWN')`,
			"7b56e1a1-dc10-46d4-a07b-0c9fb0f84818", racingGrant.Asset.ID)
		insertDone <- insertErr
	}()
	select {
	case insertErr := <-insertDone:
		t.Fatalf("upload grant insert bypassed the tombstone lock: %v", insertErr)
	case <-time.After(75 * time.Millisecond):
	}
	if err := deletionTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if insertErr := <-insertDone; insertErr == nil {
		t.Fatal("queued upload grant appeared after tombstone commit")
	}
	// A purge-state row is not visible until the tombstone commits, so no mark
	// can hold it while the initial transition still owns the asset row.
	if _, err := pool.Exec(ctx, `UPDATE media.object_purge_state SET purged_at=now() WHERE asset_id=$1`, racingGrant.Asset.ID); err != nil {
		t.Fatalf("purge mark after tombstone commit failed: %v", err)
	}
	coordinationRollback, err := os.ReadFile("../../migrations/media/000006_object_purge_coordination.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(coordinationRollback)); err == nil {
		t.Fatal("object purge coordination rollback discarded a tombstone")
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatalf("refused object purge rollback damaged media readiness: %v", err)
	}
	stale := ready
	stale.State, stale.Version = StateDeleted, ready.Version+1
	if err := repository.Update(ctx, stale, ready.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if _, err := repository.Get(ctx, tenantID, "owner-postgres", ready.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted asset read error=%v", err)
	}
	var legacyStatus, lifecycle string
	var version int64
	var completedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT asset.status, asset.lifecycle_state, asset.version, session.completed_at
		FROM media.assets AS asset
		JOIN media.upload_sessions AS session ON session.asset_id = asset.id
		WHERE asset.id = $1`, ready.ID).Scan(&legacyStatus, &lifecycle, &version, &completedAt); err != nil {
		t.Fatal(err)
	}
	if legacyStatus != "DELETED" || lifecycle != string(StateDeleted) || version != 4 || completedAt == nil {
		t.Fatalf("legacy=%s lifecycle=%s version=%d completed=%v", legacyStatus, lifecycle, version, completedAt)
	}
	if _, err := pool.Exec(ctx, `UPDATE media.assets SET status='READY',lifecycle_state='READY' WHERE id=$1`, ready.ID); err == nil {
		t.Fatal("direct write resurrected a deleted media asset")
	}
	if _, err := pool.Exec(ctx, `UPDATE media.upload_sessions SET completed_at=NULL WHERE asset_id=$1`, ready.ID); err == nil {
		t.Fatal("direct write cleared upload completion evidence")
	}
	rollbackSQL, err := os.ReadFile("../../migrations/media/000005_upload_grant_provenance.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(rollbackSQL)); err == nil {
		t.Fatal("grant provenance rollback discarded existing upload sessions")
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatalf("refused rollback damaged media readiness: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media.upload_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(rollbackSQL)); err == nil {
		t.Fatal("grant provenance rollback reopened broad runtime writes while media assets remain")
	}
}

type postgresTestSigner struct{}

type conditionalPostgresTestSigner struct{ postgresTestSigner }

type liveConditionalPostgresTestSigner struct{ postgresTestSigner }

func (liveConditionalPostgresTestSigner) PresignPut(_ context.Context, key string, metadata ObjectMetadata, expiresAt time.Time) (string, map[string]string, error) {
	signedAt := expiresAt.Add(-10 * time.Minute).UTC().Format("20060102T150405Z")
	return "https://uploads.example.test/" + key +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=" + signedAt +
		"&X-Amz-Expires=900&X-Amz-SignedHeaders=host%3Bif-none-match&X-Amz-Signature=" +
		strings.Repeat("a", 64), map[string]string{"content-type": metadata.ContentType, "if-none-match": "*"}, nil
}

func (conditionalPostgresTestSigner) PresignPut(_ context.Context, key string, metadata ObjectMetadata, _ time.Time) (string, map[string]string, error) {
	return "https://uploads.example.test/" + key +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20260831T130000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host%3Bif-none-match&X-Amz-Signature=" +
		strings.Repeat("a", 64), map[string]string{"content-type": metadata.ContentType, "if-none-match": "*"}, nil
}

func (postgresTestSigner) Present(_ context.Context, key, _ string, expiresAt time.Time) (string, error) {
	return "https://media.example.test/" + key + "?expires=" + strconv.FormatInt(expiresAt.Unix(), 10), nil
}

func (postgresTestSigner) PresignPut(_ context.Context, key string, metadata ObjectMetadata, _ time.Time) (string, map[string]string, error) {
	return "https://uploads.example.test/" + key, map[string]string{
		"content-type": metadata.ContentType, "x-amz-checksum-sha256": metadata.SHA256,
	}, nil
}

type postgresTestObjects struct {
	metadata map[string]ObjectMetadata
	deleted  map[string]bool
}

func (objects *postgresTestObjects) Inspect(_ context.Context, key string) (ObjectMetadata, error) {
	value, exists := objects.metadata[key]
	if !exists {
		return ObjectMetadata{}, errors.New("object not found")
	}
	return value, nil
}

func (objects *postgresTestObjects) Delete(_ context.Context, key string) error {
	objects.deleted[key] = true
	return nil
}

type postgresTestScanner struct{}

func (postgresTestScanner) Scan(context.Context, string) (ScanResult, error) {
	return ScanResult{Clean: true}, nil
}
