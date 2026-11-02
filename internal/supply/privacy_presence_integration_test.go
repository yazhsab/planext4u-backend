//go:build integration

package supply

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This test uses only the disposable SUPPLY_DATABASE_TEST_URL database. The
// opt-in owner scrub is never run against the persistent local runtime.
func TestSupplyPrivacyScrubWithdrawsVendorPresenceWithoutFinancialMutation(t *testing.T) {
	url := os.Getenv("SUPPLY_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("SUPPLY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, schema := range []string{"supply", "identity"} {
		if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, schema := range []string{"supply", "identity"} {
			_, _ = pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		}
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000002_durable_vendor_runtime.up.sql",
		"../../migrations/supply/000004_application_submissions.up.sql",
		"../../migrations/supply/000006_privacy_application_export.up.sql",
		"../../migrations/supply/000007_rework_profile_history.up.sql",
		"../../migrations/supply/000008_privacy_onboarding_scrub.up.sql",
		"../../migrations/supply/000012_country_scoped_onboarding_scrub.up.sql",
		"../../migrations/supply/000013_vendor_presence_privacy.up.sql",
		"../../migrations/identity/000001_identity.up.sql",
		"../../migrations/identity/000004_account_lifecycle.up.sql",
		"../../migrations/identity/000013_privacy_export_authority.up.sql",
		"../../migrations/identity/000014_deletion_completion_guard.up.sql",
		"../../migrations/identity/000015_deletion_owner_tasks.up.sql",
		"../../migrations/identity/000016_owner_erasure_capabilities.up.sql",
		"../../migrations/identity/000017_deletion_governance_scope.up.sql",
		"../../migrations/identity/000020_owner_erasure_country_scope.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	const (
		tenant       = "11111111-1111-4111-8111-111111111111"
		otherTenant  = "22222222-2222-4222-8222-222222222222"
		subject      = "33333333-3333-4333-8333-333333333333"
		otherSubject = "44444444-4444-4444-8444-444444444444"
		requestID    = "55555555-5555-4555-8555-555555555555"
		inApp        = "66666666-6666-4666-8666-666666666666"
		ngApp        = "77777777-7777-4777-8777-777777777777"
		otherApp     = "88888888-8888-4888-8888-888888888888"
	)
	now := time.Now().UTC()
	for _, row := range []struct{ id, tenant, provider string }{
		{subject, tenant, "supply-privacy-owner"},
		{otherSubject, otherTenant, "supply-privacy-foreign"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
		 VALUES($1,$2,'local',$3,$4)`, row.id, row.tenant, row.provider, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
		(id,identity_id,status,reason,requested_at,effective_at,country)
		VALUES($1,$2,'SCHEDULED','synthetic disposable test',$3,$4,'IN')`,
		requestID, subject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
		(request_id,owner,status,updated_at) VALUES($1,'supply','READY',$2)`, requestID, now); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, tenant, country, subject string }{
		{inApp, tenant, "IN", subject},
		{ngApp, tenant, "NG", subject},
		{otherApp, otherTenant, "IN", otherSubject},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_applications
			(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,
			 contact_name,bank_reference,bank_last4,bank_status,verified,created_at,updated_at)
			VALUES($1,$2,$3,$4,1,'APPROVED','Synthetic shop','RETAIL',
			 'Private contact','private-bank','1234','VERIFIED',true,$5,$5)`,
			row.id, row.tenant, row.country, row.subject, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO supply.service_zones
			(id,vendor_identity_id,tenant_id,country,postal_codes,latitude,longitude,radius_km,policy_version)
			VALUES(gen_random_uuid(),$1,$2,$3,ARRAY['600001'],13.0827,80.2707,5,'v1')`,
			row.subject, row.tenant, row.country); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO supply.catalog_items
			(id,tenant_id,country,vendor_identity_id,revision,kind,name,description,sku,
			 price_minor,currency,stock,approval_status,active,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,1,'PRODUCT','Synthetic item','Retained catalog',
			 $4,12900,'INR',5,'APPROVED',true,$5)`,
			row.tenant, row.country, row.subject, row.id, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_documents
		(id,application_id,kind,private_asset_id,ocr_status,created_at)
		VALUES(gen_random_uuid(),$1,'OWNER_IDENTITY',gen_random_uuid(),'READY',$2)`,
		inApp, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.application_submissions
		(application_id,revision,documents,submitted_by,submitted_at,contact_name)
		VALUES($1,1,'[{"kind":"OWNER_IDENTITY","asset_id":"private"}]'::jsonb,$2,$3,'Private contact')`,
		inApp, subject, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.work_items
		(id,tenant_id,country,vendor_identity_id,reference_type,reference_id,status,
		 total_minor,currency,customer_label,updated_at)
		VALUES(gen_random_uuid(),$1,'IN',$2,'MARKETPLACE_ORDER',gen_random_uuid(),
		 'COMPLETED',54321,'INR','Customer',$3)`, tenant, subject, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT * FROM supply.privacy_scrub_onboarding($1)`, requestID); err == nil {
		t.Fatal("supply privacy scrub ran without a country retention policy")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
		(tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
		VALUES($1,'IN','supply',1,'supply-v1',interval '0',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
		(request_id,owner,reason_reference,recorded_at)
		VALUES($1,'supply','synthetic hold',$2)`, requestID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT * FROM supply.privacy_scrub_onboarding($1)`, requestID); err == nil {
		t.Fatal("supply privacy scrub crossed an unresolved legal hold")
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.deletion_legal_holds
		SET released_at=clock_timestamp() WHERE request_id=$1`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT * FROM supply.privacy_scrub_vendor_presence($1)`, requestID); err == nil {
		t.Fatal("vendor presence scrub ran before onboarding redaction")
	}
	var outcome, evidence string
	var applications, documents, submissions int
	if err := pool.QueryRow(ctx, `SELECT outcome,evidence_reference,application_rows,document_rows,submission_rows
		FROM supply.privacy_scrub_onboarding($1)`, requestID).
		Scan(&outcome, &evidence, &applications, &documents, &submissions); err != nil {
		t.Fatal(err)
	}
	if outcome != "RETAINED" || evidence != "supply-onboarding:"+requestID ||
		applications != 1 || documents != 1 || submissions != 1 {
		t.Fatalf("partial scrub outcome=%s evidence=%s rows=%d/%d/%d",
			outcome, evidence, applications, documents, submissions)
	}
	var zones, catalogs, presenceRuns, onboardingRuns int
	if err := pool.QueryRow(ctx, `SELECT p.service_zone_rows,p.catalog_deactivated_rows,
		(SELECT count(*) FROM supply.privacy_presence_erasure_runs),
		(SELECT count(*) FROM supply.privacy_erasure_runs)
		FROM supply.privacy_presence_erasure_runs p WHERE p.request_id=$1`, requestID).
		Scan(&zones, &catalogs, &presenceRuns, &onboardingRuns); err != nil {
		t.Fatal(err)
	}
	if zones != 1 || catalogs != 1 || presenceRuns != 1 || onboardingRuns != 1 {
		t.Fatalf("presence audit rows=%d/%d audit=%d/%d", zones, catalogs, presenceRuns, onboardingRuns)
	}
	var ownerZones, foreignZones, activeOwner, activeForeign, ownerVerified bool
	var ownerRevision int64
	var ownerBank *string
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supply.service_zones WHERE tenant_id=$1
		AND country='IN' AND vendor_identity_id=$2),
		EXISTS(SELECT 1 FROM supply.service_zones WHERE tenant_id=$1 AND country='NG'
		AND vendor_identity_id=$2),
		EXISTS(SELECT 1 FROM supply.service_zones WHERE tenant_id=$3 AND country='IN'
		AND vendor_identity_id=$4)`, tenant, subject, otherTenant, otherSubject).
		Scan(&ownerZones, &foreignZones, &activeForeign); err != nil {
		t.Fatal(err)
	}
	if ownerZones || !foreignZones || !activeForeign {
		t.Fatal("country or tenant boundary was violated by service-zone scrub")
	}
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT active FROM supply.catalog_items WHERE tenant_id=$1 AND country='IN'),
		(SELECT active FROM supply.catalog_items WHERE tenant_id=$1 AND country='NG'),
		(SELECT active FROM supply.catalog_items WHERE tenant_id=$2 AND country='IN'),
		(SELECT revision FROM supply.vendor_applications WHERE id=$3),
		(SELECT verified FROM supply.vendor_applications WHERE id=$3),
		(SELECT bank_reference FROM supply.vendor_applications WHERE id=$3)`,
		tenant, otherTenant, inApp).
		Scan(&activeOwner, &foreignZones, &activeForeign, &ownerRevision, &ownerVerified, &ownerBank); err != nil {
		t.Fatal(err)
	}
	if activeOwner || !foreignZones || !activeForeign || ownerVerified || ownerBank != nil || ownerRevision != 2 {
		t.Fatalf("vendor scope/status was not safely retired: active=%t foreign=%t/%t verified=%t bank=%v revision=%d",
			activeOwner, foreignZones, activeForeign, ownerVerified, ownerBank, ownerRevision)
	}
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT total_minor FROM supply.work_items
		WHERE tenant_id=$1 AND country='IN' AND vendor_identity_id=$2`,
		tenant, subject).Scan(&amount); err != nil || amount != 54321 {
		t.Fatalf("financial work amount changed: %d, %v", amount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT outcome,evidence_reference,application_rows,document_rows,submission_rows
		FROM supply.privacy_scrub_onboarding($1)`, requestID).
		Scan(&outcome, &evidence, &applications, &documents, &submissions); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if applications != 1 || documents != 1 || submissions != 1 {
		t.Fatalf("retry changed original evidence: %d/%d/%d", applications, documents, submissions)
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.privacy_presence_erasure_runs
		SET service_zone_rows=0 WHERE request_id=$1`, requestID); err == nil {
		t.Fatal("presence erasure audit row was mutable")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE supply.privacy_presence_erasure_runs`); err == nil {
		t.Fatal("presence erasure audit row was truncatable")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.service_zones
		(id,vendor_identity_id,tenant_id,country,postal_codes,latitude,longitude,radius_km,policy_version)
		VALUES(gen_random_uuid(),$1,$2,'IN',ARRAY['600001'],13.0,80.0,5,'v1')`,
		subject, tenant); err == nil {
		t.Fatal("deleted service geography was reintroduced")
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.catalog_items SET active=true
		WHERE tenant_id=$1 AND country='IN' AND vendor_identity_id=$2`, tenant, subject); err == nil {
		t.Fatal("withdrawn vendor catalog was reactivated")
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.vendor_applications SET verified=true WHERE id=$1`, inApp); err == nil {
		t.Fatal("scrubbed vendor was reverified")
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.vendor_applications SET bank_reference='restored-bank'
		WHERE id=$1`, inApp); err == nil {
		t.Fatal("scrubbed vendor bank reference was restored")
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.vendor_applications SET status='REJECTED'
		WHERE id=$1`, inApp); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.vendor_applications SET status='APPROVED'
		WHERE id=$1`, inApp); err == nil {
		t.Fatal("scrubbed vendor approval status was restored")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_documents
		(id,application_id,kind,private_asset_id,ocr_status,created_at)
		VALUES(gen_random_uuid(),$1,'OWNER_IDENTITY',gen_random_uuid(),'READY',clock_timestamp())`,
		inApp); err == nil {
		t.Fatal("scrubbed private document was restored")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.application_submissions
		(application_id,revision,documents,submitted_by,submitted_at)
		VALUES($1,2,'[]'::jsonb,$2,clock_timestamp())`, inApp, subject); err == nil {
		t.Fatal("scrubbed onboarding submission was restored")
	}
	var retained int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_receipts
		WHERE request_id=$1 AND owner='supply' AND outcome='RETAINED'`, requestID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("partial receipt count=%d, %v", retained, err)
	}
	// A receipt created by the prior migration may exist without the new
	// presence audit. It must reject stale private fields, then backfill
	// presence and retire verified approval without rewriting the old row.
	const oldRequest = "99999999-9999-4999-8999-999999999999"
	if _, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
		(id,identity_id,status,reason,requested_at,effective_at,country)
		VALUES($1,$2,'SCHEDULED','synthetic old scrub',$3,$4,'IN')`,
		oldRequest, otherSubject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
		(request_id,owner,status,updated_at) VALUES($1,'supply','READY',$2)`, oldRequest, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
		(tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
		VALUES($1,'IN','supply',1,'old-supply-v1',interval '0',$2)`, otherTenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.privacy_erasure_runs
		(request_id,tenant_id,subject_id,policy_version,countries,
		 application_rows,document_rows,submission_rows,occurred_at)
		VALUES($1,$2,$3,'old-supply-v1',ARRAY['IN'],1,0,0,$4)`,
		oldRequest, otherTenant, otherSubject, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT * FROM supply.privacy_scrub_onboarding($1)`, oldRequest); err == nil {
		t.Fatal("stale prior scrub evidence hid restored private fields")
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.vendor_applications
		SET contact_name='',bank_reference=NULL,bank_last4=NULL,bank_status=NULL,
		    bank_holder_name=NULL,bank_ifsc=NULL WHERE id=$1`, otherApp); err != nil {
		t.Fatal(err)
	}
	scrubTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scrubTx.Rollback(ctx) }()
	if err := scrubTx.QueryRow(ctx, `SELECT outcome,evidence_reference,application_rows,document_rows,submission_rows
		FROM supply.privacy_scrub_onboarding($1)`, oldRequest).
		Scan(&outcome, &evidence, &applications, &documents, &submissions); err != nil {
		t.Fatalf("prior scrub presence backfill: %v", err)
	}
	child, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Release()
	var childPID int
	if err := child.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&childPID); err != nil {
		t.Fatal(err)
	}
	childResult := make(chan error, 1)
	go func() {
		_, insertErr := child.Exec(ctx, `INSERT INTO supply.vendor_documents
			(id,application_id,kind,private_asset_id,ocr_status,created_at)
			VALUES(gen_random_uuid(),$1,'OWNER_IDENTITY',gen_random_uuid(),'READY',clock_timestamp())`,
			otherApp)
		childResult <- insertErr
	}()
	locked := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		select {
		case insertErr := <-childResult:
			t.Fatalf("child evidence did not wait for the scrub: %v", insertErr)
		default:
		}
		var waitType *string
		if err := pool.QueryRow(ctx, `SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1`,
			childPID).Scan(&waitType); err != nil {
			t.Fatal(err)
		}
		if waitType != nil && *waitType == "Lock" {
			locked = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !locked {
		t.Fatal("child evidence insert did not queue behind the scrub lock")
	}
	if err := scrubTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case insertErr := <-childResult:
		if insertErr == nil || !strings.Contains(insertErr.Error(), "deleted vendor private evidence") {
			t.Fatalf("queued child insert crossed the committed scrub: %v", insertErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued child insert did not resolve after scrub commit")
	}
	if outcome != "RETAINED" || applications != 1 || documents != 0 || submissions != 0 {
		t.Fatalf("prior receipt changed during backfill: %s %d/%d/%d", outcome, applications, documents, submissions)
	}
	var backfillVerified, backfillActive bool
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT verified FROM supply.vendor_applications WHERE id=$1),
		(SELECT active FROM supply.catalog_items WHERE tenant_id=$2 AND country='IN')`,
		otherApp, otherTenant).Scan(&backfillVerified, &backfillActive); err != nil {
		t.Fatal(err)
	}
	if backfillVerified || backfillActive {
		t.Fatal("prior scrub backfill left an active vendor or catalog")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM supply.vendor_applications WHERE id=$1`, otherApp); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_applications
		(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,
		 contact_name,verified,created_at,updated_at)
		VALUES($1,$2,'IN',$3,1,'APPROVED','Recreated shop','RETAIL','',false,$4,$4)`,
		otherApp, otherTenant, otherSubject, now); err == nil {
		t.Fatal("scrubbed vendor application was recreated after deletion")
	}
	downMigration, err := os.ReadFile("../../migrations/supply/000013_vendor_presence_privacy.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(downMigration)); err == nil {
		t.Fatal("rollback discarded recorded presence-erasure evidence")
	}
	var evidenceStillPresent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supply.privacy_presence_erasure_runs)`).
		Scan(&evidenceStillPresent); err != nil || !evidenceStillPresent {
		t.Fatalf("failed rollback did not preserve privacy evidence: %t, %v", evidenceStillPresent, err)
	}
}
