//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPrivacyExportV4UsesRestrictedOwnerFunctions(t *testing.T) {
	url := os.Getenv("IDENTITY_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("IDENTITY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	// This environment variable points at the disposable identity integration
	// database. Do not run this test against a shared or production database.
	for _, schema := range []string{"identity", "supply", "fulfillment", "admin"} {
		if _, err = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, schema := range []string{"identity", "supply", "fulfillment", "admin"} {
			_, _ = pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		}
		pool.Close()
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/platform/000008_admin_roles.up.sql",
		"../../migrations/platform/000019_customer_web_roles.up.sql",
		"../../migrations/platform/000020_bankverification_roles.up.sql",
		"../../migrations/admin/000001_admin_control_plane.up.sql",
		"../../migrations/admin/000004_operator_accounts.up.sql",
		"../../migrations/admin/000005_operator_administration.up.sql",
		"../../migrations/admin/000014_participant_review_authority.up.sql",
		"../../migrations/admin/000017_deletion_governor_authority.up.sql",
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000002_durable_vendor_runtime.up.sql",
		"../../migrations/supply/000004_application_submissions.up.sql",
		"../../migrations/supply/000006_privacy_application_export.up.sql",
		"../../migrations/supply/000007_rework_profile_history.up.sql",
		"../../migrations/supply/000008_privacy_onboarding_scrub.up.sql",
		"../../migrations/supply/000012_country_scoped_onboarding_scrub.up.sql",
		"../../migrations/supply/000013_vendor_presence_privacy.up.sql",
		"../../migrations/fulfillment/000001_fulfillment.up.sql",
		"../../migrations/fulfillment/000005_scoped_recovery.up.sql",
		"../../migrations/fulfillment/000008_rider_rework.up.sql",
		"../../migrations/fulfillment/000010_privacy_rider_export.up.sql",
		"../../migrations/fulfillment/000011_privacy_onboarding_scrub.up.sql",
		"../../migrations/fulfillment/000012_country_scoped_onboarding_scrub.up.sql",
		"../../migrations/fulfillment/000013_ephemeral_rider_location_scrub.up.sql",
		"../../migrations/identity/000001_identity.up.sql",
		"../../migrations/identity/000002_profile_contacts.up.sql",
		"../../migrations/identity/000004_account_lifecycle.up.sql",
		"../../migrations/identity/000006_scoped_consent_decisions.up.sql",
		"../../migrations/identity/000008_contact_matching.up.sql",
		"../../migrations/identity/000009_profile_occupations.up.sql",
		"../../migrations/identity/000013_privacy_export_authority.up.sql",
		"../../migrations/identity/000014_deletion_completion_guard.up.sql",
		"../../migrations/identity/000015_deletion_owner_tasks.up.sql",
		"../../migrations/identity/000016_owner_erasure_capabilities.up.sql",
		"../../migrations/identity/000017_deletion_governance_scope.up.sql",
		"../../migrations/identity/000018_deletion_governance_commands.up.sql",
		"../../migrations/identity/000019_deletion_governance_review.up.sql",
		"../../migrations/identity/000020_owner_erasure_country_scope.up.sql",
		"../../migrations/identity/000021_terminal_task_ack_gate.up.sql",
		"../../migrations/identity/000022_bankverification_erasure_scope.up.sql",
		"../../migrations/identity/000023_notification_erasure_scope.up.sql",
		"../../migrations/identity/000024_customer_web_erasure_scope.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	tenant := "11111111-1111-4111-8111-111111111111"
	foreignTenant := "22222222-2222-4222-8222-222222222222"
	subject := "33333333-3333-4333-8333-333333333333"
	foreign := "44444444-4444-4444-8444-444444444444"
	session := "55555555-5555-4555-8555-555555555555"
	now := time.Now().UTC()
	for _, account := range []struct{ id, tenant, provider string }{{subject, tenant, "owner"}, {foreign, foreignTenant, "foreign"}} {
		if _, err = pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at) VALUES($1,$2,'local',$3,$4)`, account.id, account.tenant, account.provider, now); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO identity.profiles(identity_id,display_name,updated_at) VALUES($1,'Owner',$2)`, account.id, now); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO identity.identity_roles(identity_id,role,granted_at) VALUES($1,'CUSTOMER',$2)`, account.id, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.sessions(id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at)
	 VALUES($1,$2,$3,'IN','private-device','safe-device',$4,$5,$5)`, session, subject, tenant, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, tenant, country, owner, marker string }{
		{"66666666-6666-4666-8666-666666666666", tenant, "IN", subject, "owner-vendor-in"},
		{"77777777-7777-4777-8777-777777777777", tenant, "NG", subject, "owner-vendor-ng"},
		{"88888888-8888-4888-8888-888888888888", foreignTenant, "IN", foreign, "foreign-vendor"},
		{"13131313-1313-4131-8131-131313131313", foreignTenant, "NG", foreign, "foreign-vendor-ng"},
	} {
		if _, err = pool.Exec(ctx, `INSERT INTO supply.vendor_applications(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,bank_reference,bank_last4,bank_status,created_at,updated_at)
		 VALUES($1,$2,$3,$4,1,'SUBMITTED',$5,'RETAIL','Contact','secret-bank','1234','PENDING',$6,$6)`, row.id, row.tenant, row.country, row.owner, row.marker, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, tenant, country, owner, marker string }{
		{"99999999-9999-4999-8999-999999999999", tenant, "IN", subject, "owner-rider-in"},
		{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", tenant, "NG", subject, "owner-rider-ng"},
		{"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", foreignTenant, "IN", foreign, "foreign-rider"},
		{"14141414-1414-4141-8141-141414141414", foreignTenant, "NG", foreign, "foreign-rider-ng"},
	} {
		if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_profiles(id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at)
		 VALUES($1,$2,$3,$4,1,'SUBMITTED',$5,'secret-phone','BIKE','TN01AA1234','secret-rider-bank','PENDING',ARRAY['zone-a'],1,'[]'::jsonb,$6,$6)`, row.id, row.tenant, row.country, row.owner, row.marker, now); err != nil {
			t.Fatal(err)
		}
	}
	foreignVendorApp := "88888888-8888-4888-8888-888888888888"
	foreignRiderProfile := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, err = pool.Exec(ctx, `INSERT INTO supply.vendor_documents(id,application_id,kind,private_asset_id,ocr_status,extracted_fields,review_reason,created_at)
	 VALUES('dddddddd-dddd-4ddd-8ddd-dddddddddddd',$1,'IDENTITY','eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee','READY','{"secret":"ocr-name"}'::jsonb,'private-review',$2)`, foreignVendorApp, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO supply.application_submissions(application_id,revision,documents,submitted_by,submitted_at,contact_name)
	 VALUES($1,1,'[{"kind":"IDENTITY","asset_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}]'::jsonb,$2,$3,'private-submitted-contact')`, foreignVendorApp, foreign, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.rider_profiles
	 SET documents='[{"kind":"IDENTITY","asset_id":"ffffffff-ffff-4fff-8fff-ffffffffffff"}]'::jsonb,
	 review_reason='private-review' WHERE id=$1`, foreignRiderProfile); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_submissions(profile_id,revision,documents,submitted_by,submitted_at)
	 VALUES($1,1,'[{"kind":"IDENTITY","asset_id":"ffffffff-ffff-4fff-8fff-ffffffffffff"}]'::jsonb,$2,$3)`, foreignRiderProfile, foreign, now); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE planext4u_identity_runtime`)
		return err
	}
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var broadPrivilege bool
	if err = runtime.QueryRow(ctx, `SELECT has_table_privilege(current_user,'supply.vendor_applications','SELECT')
	 OR has_table_privilege(current_user,'fulfillment.rider_profiles','SELECT')
	 OR has_table_privilege(current_user,'identity.deletion_retention_policies','INSERT')
	 OR has_table_privilege(current_user,'identity.deletion_owner_receipts','INSERT')
	 OR has_table_privilege(current_user,'identity.deletion_legal_holds','INSERT')`).Scan(&broadPrivilege); err != nil || broadPrivilege {
		t.Fatalf("identity runtime has broad owner/privacy table grants: %v, %v", broadPrivilege, err)
	}
	repository, err := NewPostgresRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	trusted := TrustedIdentity{Subject: subject, TenantID: tenant, Session: session, Country: "IN"}
	value, err := repository.ExportSnapshotV4(ctx, trusted, now)
	if err != nil {
		t.Fatal(err)
	}
	if value.SchemaVersion != 4 || len(*value.VendorApplications) != 2 || len(*value.RiderProfiles) != 2 || (*value.VendorApplications)[1].Country != "NG" || (*value.RiderProfiles)[1].Country != "NG" {
		t.Fatalf("missing owner cross-country rows: %#v", value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"foreign-vendor", "foreign-rider", "secret-bank", "secret-rider-bank", "secret-phone", "private-device"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("owner export leaked %s", secret)
		}
	}
	var foreignRows int
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM supply.privacy_export_vendor_applications($1,$2,'wrong-session','IN')`, tenant, subject).Scan(&foreignRows); err != nil || foreignRows != 0 {
		t.Fatalf("unbound source session exposed rows: %d, %v", foreignRows, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$2 WHERE id=$1`, session, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ExportSnapshotV4(ctx, trusted, now); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked source export = %v", err)
	}
	requestID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if _, err = pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests(id,identity_id,status,reason,requested_at,effective_at,country)
	 VALUES($1,$2,'SCHEDULED','test fixture',$3,$4,'IN')`, requestID, subject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=NULL WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ExportSnapshotV4(ctx, trusted, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("due deletion export = %v", err)
	}
	if err = repository.DeletionReady(ctx); err != nil {
		t.Fatal(err)
	}
	processed, err := repository.PrepareDeletionTasks(ctx, maxDeletionSweep)
	if err != nil || processed != 1 {
		t.Fatalf("prepare due deletion tasks: processed=%d err=%v", processed, err)
	}
	var waiting, total int
	if err = runtime.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='WAITING_POLICY'),count(*)
	 FROM identity.deletion_owner_tasks WHERE request_id=$1`, requestID).Scan(&waiting, &total); err != nil || waiting != 24 || total != 24 {
		t.Fatalf("unconfigured owner task inventory: waiting=%d total=%d err=%v", waiting, total, err)
	}
	processed, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep)
	if err != nil || processed != 1 {
		t.Fatalf("resumed task preparation: processed=%d err=%v", processed, err)
	}
	var changed int
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_tasks
	 WHERE request_id=$1 AND revision<>1`, requestID).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("idempotent sweep changed task revision: %d, %v", changed, err)
	}
	complete := `UPDATE identity.account_deletion_requests SET status='COMPLETED' WHERE id=$1`
	if _, err = pool.Exec(ctx, complete, requestID); err == nil {
		t.Fatal("deletion completed without configured policies and owner receipts")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies(tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
	 SELECT $1,'IN',owner,1,'synthetic-test-policy',interval '0',$2 FROM identity.deletion_owner_requirements`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_owner_receipts(request_id,owner,outcome,policy_version,evidence_reference,attested_at)
	 SELECT $1,owner,'NO_DATA','synthetic-test-policy','synthetic-test-receipt',$2 FROM identity.deletion_owner_requirements`, requestID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, complete, requestID); err == nil {
		t.Fatal("deletion completed before owner receipts were acknowledged by the task coordinator")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '1 day'
	 WHERE tenant_id=$1 AND owner='identity'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_tasks
	 WHERE request_id=$1 AND owner='identity' AND status='WAITING_RETENTION'`, requestID).Scan(&waiting); err != nil || waiting != 1 {
		t.Fatalf("retention task did not wait: %d, %v", waiting, err)
	}
	if _, err = pool.Exec(ctx, complete, requestID); err == nil {
		t.Fatal("deletion completed before the configured retention interval elapsed")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'
	 WHERE tenant_id=$1 AND owner='identity'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds(request_id,owner,reason_reference,recorded_at)
	 VALUES($1,'identity','synthetic-test-hold',$2)`, requestID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_tasks
	 WHERE request_id=$1 AND status='BLOCKED_HOLD'`, requestID).Scan(&waiting); err != nil || waiting != total {
		t.Fatalf("legal hold did not block every owner task: %d, %v", waiting, err)
	}
	if _, err = pool.Exec(ctx, complete, requestID); err == nil {
		t.Fatal("deletion completed with unresolved legal hold")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_legal_holds SET released_at=$2 WHERE request_id=$1`, requestID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_tasks
	 WHERE request_id=$1 AND status='ACKNOWLEDGED'`, requestID).Scan(&waiting); err != nil || waiting != total {
		t.Fatalf("verified receipts were not acknowledged: %d, %v", waiting, err)
	}
	var requestStatus string
	if err = runtime.QueryRow(ctx, `SELECT status FROM identity.account_deletion_requests WHERE id=$1`, requestID).Scan(&requestStatus); err != nil || requestStatus != "SCHEDULED" {
		t.Fatalf("task sweep marked request completed: %s, %v", requestStatus, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_owner_tasks
	 SET checked_at=$2 WHERE request_id=$1 AND owner='identity'`, requestID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, complete, requestID); err == nil {
		t.Fatal("deletion completed with owner acknowledgement older than its receipt")
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, complete, requestID); err != nil {
		t.Fatalf("synthetic completed deletion rejected after every guard passed: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.account_deletion_requests SET status='CANCELLED' WHERE id=$1`, requestID); err == nil {
		t.Fatal("completed deletion was changed back to cancelled")
	}
	foreignRequest := "12121212-1212-4121-8121-121212121212"
	if _, err = pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests(id,identity_id,status,reason,requested_at,effective_at,country)
	 VALUES($1,$2,'SCHEDULED','synthetic owner scrub',$3,$4,'IN')`, foreignRequest, foreign, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ScrubSupplyOnboarding(ctx, foreignRequest); err == nil {
		t.Fatal("supply scrub ran without an explicit retention policy")
	}
	if _, err = repository.ScrubFulfillmentOnboarding(ctx, foreignRequest); err == nil {
		t.Fatal("fulfillment scrub ran without an explicit retention policy")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies(tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
	 VALUES($1,'IN','supply',1,'synthetic-supply-policy',interval '1 day',$2),
	       ($1,'IN','fulfillment',1,'synthetic-fulfillment-policy',interval '1 day',$2)`, foreignTenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ScrubSupplyOnboarding(ctx, foreignRequest); err == nil {
		t.Fatal("supply scrub ran before configured retention elapsed")
	}
	if _, err = repository.ScrubFulfillmentOnboarding(ctx, foreignRequest); err == nil {
		t.Fatal("fulfillment scrub ran before configured retention elapsed")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'
	 WHERE tenant_id=$1 AND owner IN ('supply','fulfillment')`, foreignTenant); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds(request_id,owner,reason_reference,recorded_at)
	 VALUES($1,'supply','synthetic-owner-hold',$2)`, foreignRequest, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ScrubSupplyOnboarding(ctx, foreignRequest); err == nil {
		t.Fatal("supply scrub ran with an unresolved legal hold")
	}
	if _, err = repository.ScrubFulfillmentOnboarding(ctx, foreignRequest); err == nil {
		t.Fatal("fulfillment scrub ran with a cross-owner legal hold")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_legal_holds SET released_at=$2 WHERE request_id=$1`, foreignRequest, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareDeletionTasks(ctx, maxDeletionSweep); err != nil {
		t.Fatal(err)
	}
	supplyScrub, err := repository.ScrubSupplyOnboarding(ctx, foreignRequest)
	if err != nil || supplyScrub.Outcome != "RETAINED" || supplyScrub.ApplicationRows != 1 || supplyScrub.DocumentRows != 1 || supplyScrub.SubmissionRows != 1 {
		t.Fatalf("supply scrub evidence = %#v, %v", supplyScrub, err)
	}
	fulfillmentScrub, err := repository.ScrubFulfillmentOnboarding(ctx, foreignRequest)
	if err != nil || fulfillmentScrub.Outcome != "RETAINED" || fulfillmentScrub.ProfileRows != 1 || fulfillmentScrub.SubmissionRows != 1 {
		t.Fatalf("fulfillment scrub evidence = %#v, %v", fulfillmentScrub, err)
	}
	supplyRetry, err := repository.ScrubSupplyOnboarding(ctx, foreignRequest)
	if err != nil || supplyRetry != supplyScrub {
		t.Fatalf("supply scrub retry = %#v, %v", supplyRetry, err)
	}
	fulfillmentRetry, err := repository.ScrubFulfillmentOnboarding(ctx, foreignRequest)
	if err != nil || fulfillmentRetry != fulfillmentScrub {
		t.Fatalf("fulfillment scrub retry = %#v, %v", fulfillmentRetry, err)
	}
	var ownerAuditRows int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM supply.privacy_erasure_runs WHERE request_id=$1)
	 +(SELECT count(*) FROM fulfillment.privacy_erasure_runs WHERE request_id=$1)`, foreignRequest).Scan(&ownerAuditRows); err != nil || ownerAuditRows != 2 {
		t.Fatalf("owner scrub retry duplicated audit: %d, %v", ownerAuditRows, err)
	}
	var scrubbedBank *string
	if err = pool.QueryRow(ctx, `SELECT bank_reference FROM supply.vendor_applications WHERE id=$1`, foreignVendorApp).Scan(&scrubbedBank); err != nil || scrubbedBank != nil {
		t.Fatalf("foreign vendor bank reference survived: %v, %v", scrubbedBank, err)
	}
	var retainedCountryVendorBank, retainedCountryRiderName string
	if err = pool.QueryRow(ctx, `SELECT bank_reference FROM supply.vendor_applications WHERE id='13131313-1313-4131-8131-131313131313'`).Scan(&retainedCountryVendorBank); err != nil || retainedCountryVendorBank != "secret-bank" {
		t.Fatalf("different-country vendor was scrubbed: %q, %v", retainedCountryVendorBank, err)
	}
	if err = pool.QueryRow(ctx, `SELECT full_name FROM fulfillment.rider_profiles WHERE id='14141414-1414-4141-8141-141414141414'`).Scan(&retainedCountryRiderName); err != nil || retainedCountryRiderName != "foreign-rider-ng" {
		t.Fatalf("different-country rider was scrubbed: %q, %v", retainedCountryRiderName, err)
	}
	var supplyCountries, fulfillmentCountries []string
	if err = pool.QueryRow(ctx, `SELECT countries FROM supply.privacy_erasure_runs WHERE request_id=$1`, foreignRequest).Scan(&supplyCountries); err != nil || len(supplyCountries) != 1 || supplyCountries[0] != "IN" {
		t.Fatalf("supply erasure audit crossed country: %v, %v", supplyCountries, err)
	}
	if err = pool.QueryRow(ctx, `SELECT countries FROM fulfillment.privacy_erasure_runs WHERE request_id=$1`, foreignRequest).Scan(&fulfillmentCountries); err != nil || len(fulfillmentCountries) != 1 || fulfillmentCountries[0] != "IN" {
		t.Fatalf("fulfillment erasure audit crossed country: %v, %v", fulfillmentCountries, err)
	}
	var ownerBank string
	if err = pool.QueryRow(ctx, `SELECT bank_reference FROM supply.vendor_applications WHERE id='66666666-6666-4666-8666-666666666666'`).Scan(&ownerBank); err != nil || ownerBank != "secret-bank" {
		t.Fatalf("other tenant was scrubbed: %q, %v", ownerBank, err)
	}
	var retained int
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_receipts
	 WHERE request_id=$1 AND owner IN ('supply','fulfillment') AND outcome='RETAINED'`, foreignRequest).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("partial owner receipts = %d, %v", retained, err)
	}
	if _, err = pool.Exec(ctx, complete, foreignRequest); err == nil {
		t.Fatal("partial owner scrubs falsely completed account deletion")
	}
	verifyDeletionGovernance(ctx, t, url, pool, runtime, repository, foreignTenant, foreignRequest)
}

func verifyDeletionGovernance(ctx context.Context, t *testing.T, url string, pool, runtime *pgxpool.Pool, repository *PostgresRepository, tenant, requestID string) {
	t.Helper()
	newSubject := "80808080-8080-4080-8080-808080808080"
	newSession := "90909090-9090-4090-8090-909090909090"
	newRequest := "abababab-abab-4aba-8aba-abababababab"
	if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
	 VALUES($1,$2,'local','governance-country-capture',clock_timestamp())`, newSubject, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.sessions
	 (id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at)
	 VALUES($1,$2,$3,'IN','capture-device','capture-reference',clock_timestamp()+interval '1 hour',clock_timestamp(),clock_timestamp())`,
		newSession, newSubject, tenant); err != nil {
		t.Fatal(err)
	}
	requestedAt := time.Now().UTC()
	if _, err := repository.CreateDeletionRequest(ctx, TrustedIdentity{Subject: newSubject, Session: newSession, TenantID: tenant, Country: "IN"},
		DeletionRequest{ID: newRequest, IdentityID: newSubject, Status: "SCHEDULED", RequestedAt: requestedAt, EffectiveAt: requestedAt.Add(24 * time.Hour)}); err != nil {
		t.Fatalf("schedule governed deletion: %v", err)
	}
	var capturedCountry string
	if err := pool.QueryRow(ctx, `SELECT country FROM identity.account_deletion_requests WHERE id=$1`, newRequest).Scan(&capturedCountry); err != nil || capturedCountry != "IN" {
		t.Fatalf("locked source country capture = %q, %v", capturedCountry, err)
	}
	var broadPrivilege bool
	if err := runtime.QueryRow(ctx, `SELECT has_table_privilege(current_user,'identity.deletion_country_policies','INSERT')
	 OR has_table_privilege(current_user,'identity.deletion_country_policies','UPDATE')
	 OR has_table_privilege(current_user,'identity.deletion_policy_proposals','INSERT')
	 OR has_table_privilege(current_user,'identity.deletion_legal_holds','UPDATE')
	 OR has_table_privilege(current_user,'identity.deletion_governance_events','INSERT')`).Scan(&broadPrivilege); err != nil || broadPrivilege {
		t.Fatalf("identity runtime has direct governance DML: %v, %v", broadPrivilege, err)
	}
	if _, err := runtime.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'`); err == nil {
		t.Fatal("runtime directly changed published retention")
	}
	if _, err := runtime.Exec(ctx, `INSERT INTO identity.deletion_legal_holds(request_id,owner,reason_reference,recorded_at)
	 VALUES($1,'identity','unauthorized direct hold',clock_timestamp())`, requestID); err == nil {
		t.Fatal("runtime directly inserted a legal hold")
	}
	first := DeletionGovernor{TenantID: tenant, Country: "IN", SubjectID: "10101010-1010-4010-8010-101010101010", OperatorSessionID: "30303030-3030-4030-8030-303030303030", OperatorRevision: 1}
	second := DeletionGovernor{TenantID: tenant, Country: "IN", SubjectID: "20202020-2020-4020-8020-202020202020", OperatorSessionID: "40404040-4040-4040-8040-404040404040", OperatorRevision: 1}
	for index, actor := range []DeletionGovernor{first, second} {
		if _, err := pool.Exec(ctx, `INSERT INTO admin.operator_accounts
		 (tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries,active,revision)
		 VALUES($1,$2,$3,'Synthetic governor',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'],true,1)`, actor.TenantID, actor.SubjectID, actor.SubjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO admin.sessions
		 (token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,
		 selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision)
		 VALUES(repeat($1,64),$2,$3,$4,'Synthetic governor',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'],
		 'IN',clock_timestamp(),ARRAY['mfa'],repeat('c',40),clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, string(rune('a'+index)), actor.OperatorSessionID, actor.SubjectID, actor.TenantID); err != nil {
			t.Fatal(err)
		}
	}
	staleMFA := first
	staleMFA.OperatorSessionID = "70707070-7070-4070-8070-707070707070"
	if _, err := pool.Exec(ctx, `INSERT INTO admin.sessions
	 (token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,
	 selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision)
	 VALUES(repeat('e',64),$1,$2,$3,'Synthetic governor',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'],
	 'IN',clock_timestamp()-interval '10 minutes',ARRAY['mfa'],repeat('d',40),
	 clock_timestamp()+interval '1 hour',clock_timestamp()-interval '10 minutes',1)`,
		staleMFA.OperatorSessionID, staleMFA.SubjectID, staleMFA.TenantID); err != nil {
		t.Fatal(err)
	}
	proposal := DeletionPolicyProposal{ID: "50505050-5050-4050-8050-505050505050", Owner: "identity", Version: "governed-v1", ExpectedRevision: 0, ReasonReference: "policy-review-reference"}
	stale := first
	stale.OperatorRevision = 2
	if _, err := repository.ProposeDeletionPolicy(ctx, stale, proposal); err == nil {
		t.Fatal("stale operator revision proposed retention")
	}
	wrongCountry := first
	wrongCountry.Country = "US"
	if _, err := repository.ProposeDeletionPolicy(ctx, wrongCountry, proposal); err == nil {
		t.Fatal("operator published outside selected country")
	}
	wrongTenant := first
	wrongTenant.TenantID = "11111111-1111-4111-8111-111111111111"
	if _, err := repository.ProposeDeletionPolicy(ctx, wrongTenant, proposal); err == nil {
		t.Fatal("operator proposed for a foreign tenant")
	}
	if _, err := repository.ProposeDeletionPolicy(ctx, staleMFA, proposal); err == nil {
		t.Fatal("stale MFA proposed retention")
	}
	var proposed string
	if proposed, _ = repository.ProposeDeletionPolicy(ctx, first, proposal); proposed != proposal.ID {
		t.Fatalf("policy proposal id = %q", proposed)
	}
	if _, err := repository.PublishDeletionPolicy(ctx, first, proposal.ID); err == nil {
		t.Fatal("policy requester self-approved")
	}
	if revision, err := repository.PublishDeletionPolicy(ctx, second, proposal.ID); err != nil || revision != 1 {
		t.Fatalf("independent policy publication = %d, %v", revision, err)
	}
	if revision, err := repository.PublishDeletionPolicy(ctx, second, proposal.ID); err != nil || revision != 1 {
		t.Fatalf("policy publication replay = %d, %v", revision, err)
	}
	staleProposal := proposal
	staleProposal.ID = "60606060-6060-4060-8060-606060606060"
	staleProposal.Version = "governed-v2"
	if _, err := repository.ProposeDeletionPolicy(ctx, first, staleProposal); err == nil {
		t.Fatal("stale policy revision proposed")
	}
	var governedRows, events int
	if err := runtime.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_country_policies
	 WHERE tenant_id=$1 AND country='IN' AND owner='identity' AND revision=1 AND policy_version='governed-v1'`, tenant).Scan(&governedRows); err != nil || governedRows != 1 {
		t.Fatalf("published policy = %d, %v", governedRows, err)
	}
	adminConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, roleErr := conn.Exec(ctx, `SET ROLE planext4u_admin_runtime`)
		return roleErr
	}
	adminRuntime, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer adminRuntime.Close()
	adminRepository, err := NewPostgresRepository(adminRuntime)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := adminRepository.ListDeletionRequests(ctx, first)
	if err != nil || len(requests) != 2 {
		t.Fatalf("scoped scheduled deletion review = %#v, %v", requests, err)
	}
	requestDetail, err := adminRepository.GetDeletionRequest(ctx, first, requestID)
	if err != nil || requestDetail.ID != requestID || requestDetail.IdentityID == "" {
		t.Fatalf("scoped deletion detail = %#v, %v", requestDetail, err)
	}
	if _, err := adminRepository.GetDeletionRequest(ctx, wrongCountry, requestID); err == nil {
		t.Fatal("country-mismatched administrator read deletion detail")
	}
	if _, err := adminRepository.ListDeletionRequests(ctx, staleMFA); err == nil {
		t.Fatal("stale MFA listed deletions")
	}
	secondPolicy := DeletionPolicyProposal{ID: "61616161-6161-4161-8161-616161616161", Owner: "identity", Version: "governed-v2", ExpectedRevision: 1, ReasonReference: "second-policy-review-reference"}
	if id, err := adminRepository.ProposeDeletionPolicy(ctx, first, secondPolicy); err != nil || id != secondPolicy.ID {
		t.Fatalf("admin BFF role policy proposal = %q, %v", id, err)
	}
	pending, err := adminRepository.GetDeletionPolicyProposal(ctx, second, secondPolicy.ID)
	if err != nil || pending.Status != "PENDING" || pending.RequestedBy != first.SubjectID || pending.PolicyVersion != "governed-v2" {
		t.Fatalf("independent policy review = %#v, %v", pending, err)
	}
	if _, err := adminRepository.GetDeletionPolicyProposal(ctx, wrongTenant, secondPolicy.ID); err == nil {
		t.Fatal("foreign tenant read retention proposal")
	}
	proposals, err := adminRepository.ListDeletionPolicyProposals(ctx, second)
	if err != nil || len(proposals) != 2 || proposals[0].ID != secondPolicy.ID {
		t.Fatalf("bounded policy list = %#v, %v", proposals, err)
	}
	if revision, err := adminRepository.PublishDeletionPolicy(ctx, second, secondPolicy.ID); err != nil || revision != 2 {
		t.Fatalf("admin BFF role policy publication = %d, %v", revision, err)
	}
	published, err := adminRepository.GetDeletionPolicyProposal(ctx, first, secondPolicy.ID)
	if err != nil || published.Status != "PUBLISHED" || published.ApprovedBy == nil || *published.ApprovedBy != second.SubjectID {
		t.Fatalf("published policy review = %#v, %v", published, err)
	}
	if err := adminRuntime.QueryRow(ctx, `SELECT has_table_privilege(current_user,'identity.deletion_country_policies','INSERT')`).Scan(&broadPrivilege); err != nil || broadPrivilege {
		t.Fatalf("admin runtime has policy DML = %v, %v", broadPrivilege, err)
	}
	if _, err := repository.ReleaseDeletionHold(ctx, first, 987654, 1, "release-without-hold"); err == nil {
		t.Fatal("nonexistent hold released")
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.account_deletion_requests SET country='US' WHERE id=$1`, requestID); err == nil {
		t.Fatal("deletion governing country changed after scheduling")
	}
	transaction, err := runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var holdID int64
	if err = transaction.QueryRow(ctx, `SELECT identity.place_deletion_hold($1,$2,$3,$4,$5,$6,$7,$8)`,
		requestID, first.TenantID, first.Country, "supply", "synthetic-litigation-reference",
		first.SubjectID, first.OperatorSessionID, first.OperatorRevision).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, scrubErr := repository.ScrubSupplyOnboarding(ctx, requestID)
		blocked <- scrubErr
	}()
	select {
	case scrubErr := <-blocked:
		t.Fatalf("scrub crossed uncommitted legal hold: %v", scrubErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err = transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	holds, err := adminRepository.ListDeletionHolds(ctx, second)
	if err != nil || len(holds) != 1 || holds[0].ID != holdID || holds[0].RecordedBy == nil || *holds[0].RecordedBy != first.SubjectID {
		t.Fatalf("scoped active hold list = %#v, %v", holds, err)
	}
	select {
	case scrubErr := <-blocked:
		if scrubErr == nil {
			t.Fatal("owner scrub continued after legal hold committed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner scrub did not resume after legal hold committed")
	}
	if _, err = repository.ReleaseDeletionHold(ctx, first, holdID, 1, "independent-release-reference"); err == nil {
		t.Fatal("hold owner self-released")
	}
	if _, err = repository.ReleaseDeletionHold(ctx, wrongCountry, holdID, 1, "independent-release-reference"); err == nil {
		t.Fatal("foreign country released hold")
	}
	if revision, err := repository.ReleaseDeletionHold(ctx, second, holdID, 1, "independent-release-reference"); err != nil || revision != 2 {
		t.Fatalf("independent hold release = %d, %v", revision, err)
	}
	if revision, err := repository.ReleaseDeletionHold(ctx, second, holdID, 1, "independent-release-reference"); err != nil || revision != 2 {
		t.Fatalf("hold release replay = %d, %v", revision, err)
	}
	secondHold, err := repository.PlaceDeletionHold(ctx, second, requestID, "fulfillment", "second-hold-reference")
	if err != nil || secondHold < 1 {
		t.Fatalf("repository hold placement = %d, %v", secondHold, err)
	}
	if revision, err := repository.ReleaseDeletionHold(ctx, first, secondHold, 1, "second-release-reference"); err != nil || revision != 2 {
		t.Fatalf("repository hold release = %d, %v", revision, err)
	}
	holds, err = adminRepository.ListDeletionHolds(ctx, first)
	if err != nil || len(holds) != 0 {
		t.Fatalf("released holds remained active = %#v, %v", holds, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_governance_events
	 WHERE tenant_id=$1 AND country='IN' AND action IN
	 ('POLICY_PROPOSED','POLICY_PUBLISHED','HOLD_PLACED','HOLD_RELEASED')`, tenant).Scan(&events); err != nil || events != 8 {
		t.Fatalf("governance audit events = %d, %v", events, err)
	}
}
