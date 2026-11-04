//go:build integration

package media

import (
	"context"
	"errors"
	urlpkg "net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type proofPurger struct {
	mu      sync.Mutex
	calls   int
	key     string
	result  versionPurgeSummary
	err     error
	started chan struct{}
	release chan struct{}
}

func (p *proofPurger) purgeExactKey(_ context.Context, key string) (versionPurgeSummary, error) {
	p.mu.Lock()
	p.calls++
	p.key = key
	p.mu.Unlock()
	if p.started != nil {
		close(p.started)
		<-p.release
	}
	return p.result, p.err
}

func (p *proofPurger) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// This fixture intentionally destroys schemas and is restricted to an
// explicitly disposable database. It never touches a persistent local stack.
func dueVersionFixture(t *testing.T) (context.Context, *pgxpool.Pool, *pgxpool.Pool, *pgxpool.Pool, string, string, string) {
	t.Helper()
	url := os.Getenv("MEDIA_DATABASE_TEST_URL")
	if url == "" || os.Getenv("MEDIA_PURGE_TEST_DISPOSABLE") != "1" {
		t.Skip("disposable media purge PostgreSQL fixture is not enabled")
	}
	parsed, err := urlpkg.Parse(url)
	if err != nil || (parsed.Path != "/planext4u_phase0_verification" && parsed.Path != "/planext4u_media_test") ||
		(parsed.Hostname() != "postgres" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1") {
		t.Fatal("media purge integration requires an explicitly disposable local verification database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"media", "identity", "supply", "fulfillment"} {
		if _, err := owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, schema := range []string{"media", "identity", "supply", "fulfillment"} {
			_, _ = owner.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		}
		owner.Close()
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/platform/000021_media_cleanup_role.up.sql",
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000003_private_evidence_reference.up.sql",
		"../../migrations/supply/000004_application_submissions.up.sql",
		"../../migrations/fulfillment/000001_fulfillment.up.sql",
		"../../migrations/fulfillment/000007_private_evidence_reference.up.sql",
		"../../migrations/fulfillment/000008_rider_rework.up.sql",
		"../../migrations/identity/000001_identity.up.sql",
		"../../migrations/identity/000014_deletion_completion_guard.up.sql",
		"../../migrations/identity/000015_deletion_owner_tasks.up.sql",
		"../../migrations/identity/000016_owner_erasure_capabilities.up.sql",
		"../../migrations/identity/000017_deletion_governance_scope.up.sql",
		"../../migrations/identity/000026_media_erasure_scope.up.sql",
		"../../migrations/media/000001_media.up.sql",
		"../../migrations/media/000002_media_lifecycle.up.sql",
		"../../migrations/media/000003_browser_presentations.up.sql",
		"../../migrations/media/000004_identity_evidence_claims.up.sql",
		"../../migrations/media/000005_upload_grant_provenance.up.sql",
		"../../migrations/media/000006_object_purge_coordination.up.sql",
		"../../migrations/media/000007_scoped_purge_state_lock.up.sql",
		"../../migrations/supply/000014_media_cleanup_reference_check.up.sql",
		"../../migrations/fulfillment/000014_media_cleanup_reference_check.up.sql",
		"../../migrations/media/000008_due_version_cleanup.up.sql",
	} {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	if _, err := owner.Exec(ctx, `CREATE ROLE planext4u_media_cleanup_login LOGIN PASSWORD 'synthetic-cleanup-only'`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `GRANT planext4u_media_cleanup TO planext4u_media_cleanup_login`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = owner.Exec(cleanupCtx, `DROP ROLE IF EXISTS planext4u_media_cleanup_login`)
	})
	rolePool := func(role string) *pgxpool.Pool {
		config, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET ROLE "+role)
			return err
		}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	cleanupURL, err := urlpkg.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	cleanupURL.User = urlpkg.UserPassword("planext4u_media_cleanup_login", "synthetic-cleanup-only")
	cleanupConfig, err := pgxpool.ParseConfig(cleanupURL.String())
	if err != nil {
		t.Fatal(err)
	}
	cleanupConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE planext4u_media_cleanup`)
		return err
	}
	cleanup, err := pgxpool.NewWithConfig(ctx, cleanupConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup.Close)
	runtime := rolePool("planext4u_media_runtime")
	tenant, subject, request := uuid.NewString(), uuid.NewString(), uuid.NewString()
	otherTenant, otherSubject, otherRequest := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ tenant, subject, request, country string }{
		{tenant, subject, request, "IN"}, {otherTenant, otherSubject, otherRequest, "NG"},
	} {
		if _, err := owner.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
		 VALUES($1,$2,'local',$3,clock_timestamp())`, row.subject, row.tenant, row.subject); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, `INSERT INTO identity.account_deletion_requests(id,identity_id,status,reason,requested_at,effective_at,country)
		 VALUES($1,$2,'SCHEDULED','disposable purge proof',clock_timestamp()-interval '3 hours',clock_timestamp()-interval '2 hours',$3)`, row.request, row.subject, row.country); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks(request_id,owner,status,updated_at)
		 VALUES($1,'media','READY',clock_timestamp())`, row.request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `INSERT INTO identity.deletion_country_policies
	 (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
	 VALUES($1,'IN','media',1,'disposable-media-v1',interval '1 day',clock_timestamp())`, tenant); err != nil {
		t.Fatal(err)
	}
	return ctx, owner, cleanup, runtime, tenant, subject, request
}

func insertDueMediaAsset(t *testing.T, ctx context.Context, owner *pgxpool.Pool, tenant, subject, country, purpose, provenance string, grantUntil time.Time) string {
	t.Helper()
	id := uuid.NewString()
	key := objectKey(tenant, subject, id)
	if _, err := owner.Exec(ctx, `INSERT INTO media.assets
	 (id,tenant_id,country,owner_id,object_key,content_type,size_bytes,checksum_sha256,status,classification,
	  created_at,updated_at,purpose,lifecycle_state,upload_expires_at,version)
	 VALUES($1,$2,$3,$4,$5,'application/pdf',16,$6,'PENDING','RESTRICTED',
	  clock_timestamp()-interval '1 hour',clock_timestamp()-interval '1 hour',$7,'PENDING_UPLOAD',
	  clock_timestamp()+interval '10 minutes',1)`, id, tenant, country, subject, key, strings.Repeat("a", 64), purpose); err != nil {
		t.Fatal(err)
	}
	var grantValue any
	if provenance == "AWS_SIGV4_IF_NONE_MATCH" {
		grantValue = grantUntil
	}
	if _, err := owner.Exec(ctx, `INSERT INTO media.upload_sessions
	 (id,asset_id,expires_at,created_at,grant_provenance,grant_valid_until)
	 VALUES($1,$2,clock_timestamp()+interval '10 minutes',clock_timestamp()-interval '1 hour',$3,$4)`,
		uuid.NewString(), id, provenance, grantValue); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE media.assets SET status='DELETED',lifecycle_state='DELETED',updated_at=clock_timestamp()
	 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertReadyMediaAsset(t *testing.T, ctx context.Context, owner *pgxpool.Pool, tenant, subject string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := owner.Exec(ctx, `INSERT INTO media.assets
	 (id,tenant_id,country,owner_id,object_key,content_type,size_bytes,checksum_sha256,status,classification,
	  created_at,updated_at,purpose,lifecycle_state,upload_expires_at,ready_at,version)
	 VALUES($1,$2,'IN',$3,$4,'application/pdf',16,$5,'READY','RESTRICTED',
	  clock_timestamp()-interval '3 hours',clock_timestamp()-interval '1 hour','IDENTITY_DOCUMENT','READY',
	  clock_timestamp()-interval '2 hours',clock_timestamp()-interval '1 hour',1)`,
		id, tenant, subject, objectKey(tenant, subject, id), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO media.upload_sessions
	 (id,asset_id,expires_at,created_at,grant_provenance,grant_valid_until,completed_at)
	 VALUES($1,$1,clock_timestamp()-interval '2 hours',clock_timestamp()-interval '3 hours',
	  'AWS_SIGV4_IF_NONE_MATCH',clock_timestamp()-interval '2 hours',clock_timestamp()-interval '1 hour')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDueVersionCleanerDisposableScopeAndEvidence(t *testing.T) {
	ctx, owner, cleanup, runtime, tenant, subject, request := dueVersionFixture(t)
	closed := time.Now().Add(-15 * time.Minute)
	asset := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", closed)
	proof := &proofPurger{result: versionPurgeSummary{versions: 2, deleteMarkers: 1}}
	cleaner := &DueVersionCleaner{pool: cleanup, purger: proof}
	if _, err := cleaner.PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("retention gate entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := owner.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'
	 WHERE tenant_id=$1 AND country='IN' AND owner='media'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO identity.deletion_legal_holds(request_id,owner,reason_reference,recorded_at)
	 VALUES($1,'media','synthetic hold',clock_timestamp())`, request); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("legal hold entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := owner.Exec(ctx, `UPDATE identity.deletion_legal_holds SET released_at=clock_timestamp()
	 WHERE request_id=$1 AND released_at IS NULL`, request); err != nil {
		t.Fatal(err)
	}
	// A previously valid login must lose cleanup authority if provisioning
	// grants it an additional runtime role after the pool was established.
	if _, err := owner.Exec(ctx, `GRANT planext4u_media_runtime TO planext4u_media_cleanup_login`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("privilege-drifted cleanup login entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := owner.Exec(ctx, `REVOKE planext4u_media_runtime FROM planext4u_media_cleanup_login`); err != nil {
		t.Fatal(err)
	}
	// Drift on the assumed role itself also expands the login's effective
	// privileges even when the login has no other direct membership.
	if _, err := owner.Exec(ctx, `GRANT planext4u_media_runtime TO planext4u_media_cleanup`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = owner.Exec(cleanupCtx, `REVOKE planext4u_media_runtime FROM planext4u_media_cleanup`)
	})
	if _, err := cleaner.PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("privilege-drifted cleanup role entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := owner.Exec(ctx, `REVOKE planext4u_media_runtime FROM planext4u_media_cleanup`); err != nil {
		t.Fatal(err)
	}
	var otherRequest string
	if err := owner.QueryRow(ctx, `SELECT d.id FROM identity.account_deletion_requests d WHERE d.id<>$1 LIMIT 1`, request).Scan(&otherRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, otherRequest, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("foreign tenant/country/subject request entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := (&DueVersionCleaner{pool: owner, purger: proof}).PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("admin login impersonated cleanup role: calls=%d err=%v", proof.callCount(), err)
	}
	impostorConfig := owner.Config()
	impostorConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE planext4u_media_cleanup`)
		return err
	}
	impostor, err := pgxpool.NewWithConfig(ctx, impostorConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(impostor.Close)
	if _, err := (&DueVersionCleaner{pool: impostor, purger: proof}).PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 0 {
		t.Fatalf("migration login with SET ROLE entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := runtime.Exec(ctx, `SELECT media.prepare_due_version_purge($1,$2)`, request, asset); err == nil {
		t.Fatal("ordinary media runtime executed cleanup preparation")
	}
	if _, err := runtime.Exec(ctx, `SELECT media.finish_due_version_purge($1,$2,'bad-key','bad-policy',0,0)`, request, asset); err == nil {
		t.Fatal("ordinary media runtime executed cleanup mark")
	}
	for _, path := range []struct {
		name, purpose, provenance string
		until                     time.Time
	}{
		{"legacy grant", "IDENTITY_DOCUMENT", "UNKNOWN", time.Time{}},
		{"live signed grant", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", time.Now().Add(time.Minute)},
		{"other purpose", "CATALOG_IMAGE", "AWS_SIGV4_IF_NONE_MATCH", closed},
	} {
		id := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", path.purpose, path.provenance, path.until)
		if _, err := cleaner.PurgeDueAsset(ctx, request, id); err == nil || proof.callCount() != 0 {
			t.Fatalf("%s entered object store: calls=%d err=%v", path.name, proof.callCount(), err)
		}
	}
	claimed := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", closed)
	if _, err := owner.Exec(ctx, `INSERT INTO media.identity_evidence_claims
	 (tenant_id,country,asset_id,consumer,resource_id,owner_id)
	 VALUES($1,'IN',$2,'VENDOR_APPLICATION','synthetic-claim',$3)`, tenant, claimed, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, claimed); err == nil || proof.callCount() != 0 {
		t.Fatalf("claimed evidence entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	legacyVendor := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", closed)
	vendor := uuid.NewString()
	if _, err := owner.Exec(ctx, `INSERT INTO supply.vendor_applications
	 (id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at)
	 VALUES($1,$2,'IN',$3,1,'SUBMITTED','synthetic','RETAIL','synthetic',clock_timestamp(),clock_timestamp())`, vendor, tenant, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO supply.vendor_documents
	 (id,application_id,kind,private_asset_id,ocr_status,created_at)
	 VALUES($1,$2,'IDENTITY',$3,'READY',clock_timestamp())`, uuid.NewString(), vendor, legacyVendor); err == nil {
		t.Fatal("new vendor document referenced a tombstoned media asset")
	}
	// Reconstruct a reference that predates the new write barrier. It must
	// still prevent physical deletion until its owner clears retention.
	if _, err := owner.Exec(ctx, `ALTER TABLE supply.vendor_documents DISABLE TRIGGER vendor_document_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO supply.vendor_documents
	 (id,application_id,kind,private_asset_id,ocr_status,created_at)
	 VALUES($1,$2,'IDENTITY',$3,'READY',clock_timestamp())`, uuid.NewString(), vendor, legacyVendor); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE supply.vendor_documents ENABLE TRIGGER vendor_document_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, legacyVendor); err == nil || proof.callCount() != 0 {
		t.Fatalf("vendor legacy reference entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := owner.Exec(ctx, `UPDATE supply.vendor_applications SET country='NG' WHERE id=$1`, vendor); err == nil {
		t.Fatal("vendor scope changed to hide a retained private document")
	}
	legacyVendorHistory := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", closed)
	if _, err := owner.Exec(ctx, `INSERT INTO supply.application_submissions
	 (application_id,revision,documents,submitted_by,submitted_at)
	 VALUES($1,1,jsonb_build_array(jsonb_build_object('asset_id',$2::text)),$3,clock_timestamp())`, vendor, legacyVendorHistory, subject); err == nil {
		t.Fatal("new vendor history referenced a tombstoned media asset")
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE supply.application_submissions DISABLE TRIGGER vendor_submission_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO supply.application_submissions
	 (application_id,revision,documents,submitted_by,submitted_at)
	 VALUES($1,1,jsonb_build_array(jsonb_build_object('asset_id',$2::text)),$3,clock_timestamp())`, vendor, legacyVendorHistory, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE supply.application_submissions ENABLE TRIGGER vendor_submission_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, legacyVendorHistory); err == nil || proof.callCount() != 0 {
		t.Fatalf("vendor historical reference entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	legacyRider := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", closed)
	riderProfile := uuid.NewString()
	if _, err := owner.Exec(ctx, `INSERT INTO fulfillment.rider_profiles
	 (id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,
	  bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at)
	 VALUES($1,$2,'IN',$3,1,'SUBMITTED','synthetic','masked','BIKE','TEST','synthetic','PENDING',ARRAY['zone'],1,
	  jsonb_build_array(jsonb_build_object('asset_id',$4::text)),clock_timestamp(),clock_timestamp())`,
		riderProfile, tenant, subject, legacyRider); err == nil {
		t.Fatal("new rider profile referenced a tombstoned media asset")
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE fulfillment.rider_profiles DISABLE TRIGGER rider_profile_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO fulfillment.rider_profiles
	 (id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,
	  bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at)
	 VALUES($1,$2,'IN',$3,1,'SUBMITTED','synthetic','masked','BIKE','TEST','synthetic','PENDING',ARRAY['zone'],1,
	  jsonb_build_array(jsonb_build_object('asset_id',$4::text)),clock_timestamp(),clock_timestamp())`,
		riderProfile, tenant, subject, legacyRider); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE fulfillment.rider_profiles ENABLE TRIGGER rider_profile_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, legacyRider); err == nil || proof.callCount() != 0 {
		t.Fatalf("rider legacy reference entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := owner.Exec(ctx, `UPDATE fulfillment.rider_profiles SET country='NG' WHERE id=$1`, riderProfile); err == nil {
		t.Fatal("rider scope changed to hide a retained private document")
	}
	legacyRiderHistory := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", closed)
	if _, err := owner.Exec(ctx, `INSERT INTO fulfillment.rider_submissions
	 (profile_id,revision,documents,submitted_by,submitted_at)
	 VALUES($1,1,jsonb_build_array(jsonb_build_object('asset_id',$2::text)),$3,clock_timestamp())`, riderProfile, legacyRiderHistory, subject); err == nil {
		t.Fatal("new rider history referenced a tombstoned media asset")
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE fulfillment.rider_submissions DISABLE TRIGGER rider_submission_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO fulfillment.rider_submissions
	 (profile_id,revision,documents,submitted_by,submitted_at)
	 VALUES($1,1,jsonb_build_array(jsonb_build_object('asset_id',$2::text)),$3,clock_timestamp())`, riderProfile, legacyRiderHistory, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE fulfillment.rider_submissions ENABLE TRIGGER rider_submission_media_reference_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, legacyRiderHistory); err == nil || proof.callCount() != 0 {
		t.Fatalf("rider historical reference entered object store: calls=%d err=%v", proof.callCount(), err)
	}
	if _, err := cleanup.Exec(ctx, `SELECT media.finish_due_version_purge($1,$2,'forged-key','disposable-media-v1',0,0)`, request, asset); err == nil {
		t.Fatal("forged worker key was accepted")
	}
	proof.err = errors.New("synthetic S3 interruption")
	if _, err := cleaner.PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 1 {
		t.Fatalf("provider failure reported success: calls=%d err=%v", proof.callCount(), err)
	}
	var marked *time.Time
	if err := owner.QueryRow(ctx, `SELECT purged_at FROM media.object_purge_state WHERE asset_id=$1`, asset).Scan(&marked); err != nil || marked != nil {
		t.Fatalf("provider failure marked purge: at=%v err=%v", marked, err)
	}
	proof.err = nil
	result, err := cleaner.PurgeDueAsset(ctx, request, asset)
	if err != nil || result.AlreadyPurged || result.VersionsDeleted != 2 || result.MarkersDeleted != 1 || proof.key != objectKey(tenant, subject, asset) {
		t.Fatalf("verified cleanup result=%+v key=%q err=%v", result, proof.key, err)
	}
	var evidenceCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM media.object_purge_evidence e
	 JOIN media.object_purge_state s ON s.asset_id=e.asset_id
	 WHERE e.asset_id=$1 AND e.request_id=$2 AND e.policy_version='disposable-media-v1'
	 AND e.deleted_versions=2 AND e.deleted_markers=1 AND e.recorded_at=s.purged_at`, asset, request).Scan(&evidenceCount); err != nil || evidenceCount != 1 {
		t.Fatalf("durable purge evidence missing: count=%d err=%v", evidenceCount, err)
	}
	if _, err := owner.Exec(ctx, `DELETE FROM media.object_purge_evidence WHERE asset_id=$1`, asset); err == nil {
		t.Fatal("purge evidence was mutable")
	}
	retry, err := cleaner.PurgeDueAsset(ctx, request, asset)
	if err != nil || !retry.AlreadyPurged || proof.callCount() != 2 {
		t.Fatalf("completed purge retry touched S3: retry=%+v calls=%d err=%v", retry, proof.callCount(), err)
	}
}

func TestDueVersionCleanerDisposableRetryAndNormalDeleteRace(t *testing.T) {
	ctx, owner, cleanup, runtime, tenant, subject, request := dueVersionFixture(t)
	if _, err := owner.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'
	 WHERE tenant_id=$1 AND country='IN' AND owner='media'`, tenant); err != nil {
		t.Fatal(err)
	}
	asset := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", time.Now().Add(-15*time.Minute))
	proof := &proofPurger{result: versionPurgeSummary{versions: 1, deleteMarkers: 1}}
	cleaner := &DueVersionCleaner{pool: cleanup, purger: proof}
	if _, err := owner.Exec(ctx, `CREATE FUNCTION media.fail_disposable_purge_mark() RETURNS trigger
	 LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic DB failure after S3 zero proof'; END $$;
	 CREATE TRIGGER fail_disposable_purge_mark BEFORE UPDATE ON media.object_purge_state
	 FOR EACH ROW EXECUTE FUNCTION media.fail_disposable_purge_mark()`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.PurgeDueAsset(ctx, request, asset); err == nil || proof.callCount() != 1 {
		t.Fatalf("DB failure after S3 success was accepted: calls=%d err=%v", proof.callCount(), err)
	}
	var marked *time.Time
	if err := owner.QueryRow(ctx, `SELECT purged_at FROM media.object_purge_state WHERE asset_id=$1`, asset).Scan(&marked); err != nil || marked != nil {
		t.Fatalf("DB failure left a purge mark: at=%v err=%v", marked, err)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER fail_disposable_purge_mark ON media.object_purge_state;
	 DROP FUNCTION media.fail_disposable_purge_mark()`); err != nil {
		t.Fatal(err)
	}
	proof.started, proof.release = make(chan struct{}), make(chan struct{})
	workerDone := make(chan error, 1)
	go func() { _, err := cleaner.PurgeDueAsset(ctx, request, asset); workerDone <- err }()
	select {
	case <-proof.started:
	case err := <-workerDone:
		t.Fatalf("worker never reached exact-key S3 proof: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	repository, err := NewPostgresRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var deleteCalls int
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- repository.DeleteTombstonedObject(ctx, tenant, "IN", subject, asset,
			func(context.Context, string) error { deleteCalls++; return nil })
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("normal delete bypassed version-purge locks: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(proof.release)
	if err := <-workerDone; err != nil {
		t.Fatalf("worker failed after S3 proof: %v", err)
	}
	if err := <-deleteDone; err != nil || deleteCalls != 0 {
		t.Fatalf("normal delete added a marker after purge: calls=%d err=%v", deleteCalls, err)
	}
	if proof.callCount() != 2 {
		t.Fatalf("DB failure was not retried: calls=%d", proof.callCount())
	}
}

func TestDueVersionCleanerDisposableCrossOwnerReferenceRaces(t *testing.T) {
	ctx, owner, cleanup, mediaRuntime, tenant, subject, request := dueVersionFixture(t)
	if _, err := owner.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'
	 WHERE tenant_id=$1 AND country='IN' AND owner='media'`, tenant); err != nil {
		t.Fatal(err)
	}
	vendor := uuid.NewString()
	if _, err := owner.Exec(ctx, `INSERT INTO supply.vendor_applications
	 (id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at)
	 VALUES($1,$2,'IN',$3,1,'DRAFT','synthetic','RETAIL','synthetic',clock_timestamp(),clock_timestamp())`, vendor, tenant, subject); err != nil {
		t.Fatal(err)
	}
	ready := insertReadyMediaAsset(t, ctx, owner, tenant, subject)
	writer, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if _, err := writer.Exec(ctx, `SET LOCAL ROLE planext4u_supply_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `INSERT INTO supply.vendor_documents
	 (id,application_id,kind,private_asset_id,ocr_status,created_at)
	 VALUES($1,$2,'IDENTITY',$3,'READY',clock_timestamp())`, uuid.NewString(), vendor, ready); err != nil {
		t.Fatalf("current READY document was rejected: %v", err)
	}
	mediaRepo, err := NewPostgresRepository(mediaRuntime)
	if err != nil {
		t.Fatal(err)
	}
	tombstoneDone := make(chan error, 1)
	go func() { _, err := mediaRepo.Tombstone(ctx, tenant, "IN", subject, ready); tombstoneDone <- err }()
	select {
	case err := <-tombstoneDone:
		t.Fatalf("tombstone bypassed pending vendor reference: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-tombstoneDone; !errors.Is(err, ErrRetained) {
		t.Fatalf("writer-first reference did not retain media: %v", err)
	}
	// A cleanup that has locked the tombstone wins the opposite ordering.
	// A direct source-role write must wait, then reject the deleted asset.
	deleted := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", time.Now().Add(-15*time.Minute))
	proof := &proofPurger{result: versionPurgeSummary{versions: 1}, started: make(chan struct{}), release: make(chan struct{})}
	cleaner := &DueVersionCleaner{pool: cleanup, purger: proof}
	cleanupDone := make(chan error, 1)
	go func() { _, err := cleaner.PurgeDueAsset(ctx, request, deleted); cleanupDone <- err }()
	select {
	case <-proof.started:
	case err := <-cleanupDone:
		t.Fatalf("cleaner did not hold asset during S3 proof: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	writeDone := make(chan error, 1)
	go func() {
		tx, err := owner.Begin(ctx)
		if err != nil {
			writeDone <- err
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = tx.Exec(ctx, `SET LOCAL ROLE planext4u_supply_runtime`); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO supply.vendor_documents
			 (id,application_id,kind,private_asset_id,ocr_status,created_at)
			 VALUES($1,$2,'IDENTITY',$3,'READY',clock_timestamp())`, uuid.NewString(), vendor, deleted)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("reference write bypassed cleaner's asset lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(proof.release)
	if err := <-cleanupDone; err != nil {
		t.Fatalf("cleaner-first purge failed: %v", err)
	}
	if err := <-writeDone; err == nil {
		t.Fatal("reference write succeeded after physical purge")
	}
	var rowCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM supply.vendor_documents
	 WHERE application_id=$1 AND private_asset_id=$2`, vendor, deleted).Scan(&rowCount); err != nil || rowCount != 0 {
		t.Fatalf("reference survived cleaner-first race: rows=%d err=%v", rowCount, err)
	}
}
