//go:build integration

package fulfillment

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresRiderPrivacyScrubRequiresQuietWorkAndRemovesScopedLocation(t *testing.T) {
	url := os.Getenv("FULFILLMENT_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("FULFILLMENT_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, schema := range []string{"fulfillment", "identity"} {
		if _, err = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, schema := range []string{"fulfillment", "identity"} {
			_, _ = pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		}
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/platform/000008_admin_roles.up.sql",
		"../../migrations/identity/000001_identity.up.sql",
		"../../migrations/identity/000004_account_lifecycle.up.sql",
		"../../migrations/identity/000013_privacy_export_authority.up.sql",
		"../../migrations/identity/000014_deletion_completion_guard.up.sql",
		"../../migrations/identity/000015_deletion_owner_tasks.up.sql",
		"../../migrations/identity/000016_owner_erasure_capabilities.up.sql",
		"../../migrations/identity/000017_deletion_governance_scope.up.sql",
		"../../migrations/identity/000019_deletion_governance_review.up.sql",
		"../../migrations/identity/000020_owner_erasure_country_scope.up.sql",
		"../../migrations/identity/000021_terminal_task_ack_gate.up.sql",
		"../../migrations/fulfillment/000001_fulfillment.up.sql",
		"../../migrations/fulfillment/000005_scoped_recovery.up.sql",
		"../../migrations/fulfillment/000008_rider_rework.up.sql",
		"../../migrations/fulfillment/000011_privacy_onboarding_scrub.up.sql",
		"../../migrations/fulfillment/000012_country_scoped_onboarding_scrub.up.sql",
		"../../migrations/fulfillment/000013_ephemeral_rider_location_scrub.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}

	const tenant = "12121212-1212-4121-8121-121212121212"
	const otherTenant = "23232323-2323-4232-8232-232323232323"
	const rider = "34343434-3434-4343-8343-343434343434"
	const otherRider = "45454545-4545-4454-8454-454545454545"
	const request = "56565656-5656-4565-8565-565656565656"
	const profile = "67676767-6767-4676-8676-676767676767"
	const otherCountryProfile = "78787878-7878-4787-8787-787878787878"
	const foreignProfile = "89898989-8989-4898-8898-898989898989"
	const duty = "90909090-9090-4090-8090-909090909090"
	const task = "abababab-abab-4aba-8aba-abababababab"
	now := time.Now().UTC()
	for _, identity := range []struct{ id, tenant string }{{rider, tenant}, {otherRider, otherTenant}} {
		if _, err = pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
		 VALUES($1,$2,'local',$1,$3)`, identity.id, identity.tenant, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
	 (id,identity_id,status,reason,requested_at,effective_at,country)
	 VALUES($1,$2,'SCHEDULED','synthetic scoped erasure',$3,$4,'IN')`,
		request, rider, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
	 (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
	 VALUES($1,'IN','fulfillment',1,'synthetic-fulfillment-v1',interval '0',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
	 (request_id,owner,status,revision,checked_at,updated_at)
	 VALUES($1,'fulfillment','READY',1,$2,$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct{ id, tenant, country, rider string }{
		{profile, tenant, "IN", rider}, {otherCountryProfile, tenant, "NG", rider},
		{foreignProfile, otherTenant, "IN", otherRider},
	} {
		if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_profiles
		 (id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,
		  vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,
		  documents,created_at,updated_at,review_reason,rework_allowed)
		 VALUES($1,$2,$3,$4,1,'APPROVED','Private Rider','*****1234','MOTORBIKE',
		 'TN01AA1234','private-bank','VERIFIED',ARRAY['private-zone'],1,
		 '[{"kind":"IDENTITY","asset_id":"private-asset"}]'::jsonb,$5,$5,'private review',false)`,
			value.id, value.tenant, value.country, value.rider, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_submissions
	 (profile_id,revision,documents,submitted_by,submitted_at)
	 VALUES($1,1,'[{"kind":"IDENTITY","asset_id":"private-asset"}]'::jsonb,$2,$3)`,
		profile, rider, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_locations
	 (rider_identity_id,tenant_id,country,sequence,latitude,longitude,accuracy_m,updated_at,expires_at)
	 VALUES($1,$2,'IN',1,13.1,80.2,5,$3,$4)`, rider, tenant, now, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct{ tenant, country, rider string }{
		{tenant, "IN", rider}, {tenant, "NG", rider}, {otherTenant, "IN", otherRider},
	} {
		if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_location_history
		 (rider_identity_id,tenant_id,country,sequence,latitude,longitude,accuracy_m,updated_at,expires_at)
		 VALUES($1,$2,$3,1,13.1,80.2,5,$4,$5)`, value.rider, value.tenant,
			value.country, now, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, roleErr := conn.Exec(ctx, `SET ROLE planext4u_identity_runtime`)
		return roleErr
	}
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	scrub := func() error {
		var outcome, reference string
		var profiles, submissions int
		return runtime.QueryRow(ctx, `SELECT outcome,evidence_reference,profile_rows,submission_rows
		 FROM fulfillment.privacy_scrub_onboarding($1)`, request).Scan(
			&outcome, &reference, &profiles, &submissions)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
	 (request_id,owner,reason_reference,recorded_at) VALUES($1,'fulfillment','synthetic hold',$2)`,
		request, now); err != nil {
		t.Fatal(err)
	}
	if err = scrub(); err == nil {
		t.Fatal("scrub bypassed unresolved cross-owner hold")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_legal_holds SET released_at=$2
	 WHERE request_id=$1`, request, now); err != nil {
		t.Fatal(err)
	}
	start, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = start.Rollback(ctx) }()
	if err = start.QueryRow(ctx, `SELECT id FROM fulfillment.rider_profiles
	 WHERE id=$1 FOR UPDATE`, profile).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	if _, err = start.Exec(ctx, `INSERT INTO fulfillment.duty_sessions
	 (id,tenant_id,country,rider_identity_id,revision,status,zone_id,started_at,last_seen_at)
	 VALUES($1,$2,'IN',$3,1,'ACTIVE','private-zone',$4,$4)`,
		duty, tenant, rider, now); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- scrub() }()
	select {
	case premature := <-blocked:
		t.Fatalf("scrub escaped the duty-start profile lock: %v", premature)
	case <-time.After(100 * time.Millisecond):
	}
	if err = start.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-blocked; err == nil {
		t.Fatal("scrub bypassed active duty")
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.duty_sessions
	 SET status='ENDED',ended_at=$2 WHERE id=$1`, duty, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.delivery_tasks
	 (id,tenant_id,country,order_id,order_type,region_id,territory_id,zone_id,
	  revision,status,assigned_rider_identity_id,pickup,dropoff,distance_meters,
	  earning_minor,currency,delivery_otp_digest,updated_at)
	 VALUES($1,$2,'IN','cdcdcdcd-cdcd-4cdc-8cdc-cdcdcdcdcdcd','FOOD','region',
	 'territory','private-zone',1,'ASSIGNED',$3,'{}'::jsonb,'{}'::jsonb,1000,
	 100,'INR','\\x01'::bytea,$4)`, task, tenant, rider, now); err != nil {
		t.Fatal(err)
	}
	if err = scrub(); err == nil {
		t.Fatal("scrub bypassed active delivery")
	}
	var beforeRows int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment.rider_location_history
	 WHERE tenant_id=$1 AND country='IN' AND rider_identity_id=$2`, tenant, rider).Scan(&beforeRows); err != nil || beforeRows != 1 {
		t.Fatalf("failed preflight mutated location: %d, %v", beforeRows, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.delivery_tasks SET status='DELIVERED'
	 WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	erasureTx, err := runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = erasureTx.Rollback(ctx) }()
	var outcome, reference string
	var erasedProfiles, erasedSubmissions int
	if err = erasureTx.QueryRow(ctx, `SELECT outcome,evidence_reference,profile_rows,submission_rows
	 FROM fulfillment.privacy_scrub_onboarding($1)`, request).Scan(
		&outcome, &reference, &erasedProfiles, &erasedSubmissions); err != nil ||
		outcome != "RETAINED" || erasedProfiles != 1 || erasedSubmissions != 1 {
		t.Fatalf("owner scrub failed: %s %d %d, %v", outcome,
			erasedProfiles, erasedSubmissions, err)
	}
	queuedWrite := make(chan error, 1)
	go func() {
		_, writeErr := pool.Exec(ctx, `INSERT INTO fulfillment.rider_location_history
		 (rider_identity_id,tenant_id,country,sequence,latitude,longitude,accuracy_m,updated_at,expires_at)
		 VALUES($1,$2,'IN',2,13.1,80.2,5,$3,$4)`, rider, tenant, now,
			now.Add(-time.Minute))
		queuedWrite <- writeErr
	}()
	select {
	case premature := <-queuedWrite:
		t.Fatalf("direct location write escaped owner-scrub table lock: %v", premature)
	case <-time.After(100 * time.Millisecond):
	}
	if err = erasureTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-queuedWrite; err == nil {
		t.Fatal("queued direct location write revived a scrubbed rider scope")
	}
	var status, name, phone, bank, bankStatus, vehicleNumber, reason string
	var zones []string
	var revision int64
	if err = pool.QueryRow(ctx, `SELECT status,full_name,phone_masked,bank_reference,
	 bank_status,vehicle_number,review_reason,zones,revision FROM fulfillment.rider_profiles
	 WHERE id=$1`, profile).Scan(&status, &name, &phone, &bank, &bankStatus,
		&vehicleNumber, &reason, &zones, &revision); err != nil || status != "SUSPENDED" ||
		name != "" || phone != "" || bank != "" || bankStatus != "" ||
		vehicleNumber != "" || reason != "" || len(zones) != 0 || revision != 2 {
		t.Fatalf("rider profile still contains private onboarding data: %s %q %q %q %q %q %q %v %d, %v",
			status, name, phone, bank, bankStatus, vehicleNumber, reason, zones, revision, err)
	}
	var locationRows, priorProfiles, priorSubmissions, priorLocations int
	if err = pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM fulfillment.rider_locations WHERE tenant_id=$1 AND country='IN' AND rider_identity_id=$2)
	 +(SELECT count(*) FROM fulfillment.rider_location_history WHERE tenant_id=$1 AND country='IN' AND rider_identity_id=$2),
	 (SELECT profile_rows FROM fulfillment.privacy_erasure_runs WHERE request_id=$3),
	 (SELECT submission_rows FROM fulfillment.privacy_erasure_runs WHERE request_id=$3),
	 (SELECT location_rows FROM fulfillment.privacy_erasure_runs WHERE request_id=$3)`, tenant, rider, request).Scan(
		&locationRows, &priorProfiles, &priorSubmissions, &priorLocations); err != nil ||
		locationRows != 0 || priorProfiles != 1 || priorSubmissions != 1 || priorLocations != 2 {
		t.Fatalf("scoped erasure audit: %d %d %d %d, %v", locationRows,
			priorProfiles, priorSubmissions, priorLocations, err)
	}
	var preserved int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment.rider_location_history
	 WHERE (tenant_id=$1 AND country='NG' AND rider_identity_id=$2)
	    OR (tenant_id=$3 AND country='IN' AND rider_identity_id=$4)`,
		tenant, rider, otherTenant, otherRider).Scan(&preserved); err != nil || preserved != 2 {
		t.Fatalf("foreign country or tenant location was scrubbed: %d, %v", preserved, err)
	}
	if err = scrub(); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT revision FROM fulfillment.rider_profiles WHERE id=$1`,
		profile).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("retry modified rider profile: %d, %v", revision, err)
	}
	var receiptRows int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.deletion_owner_receipts
	 WHERE request_id=$1 AND owner='fulfillment' AND outcome='RETAINED'`, request).Scan(&receiptRows); err != nil || receiptRows != 1 {
		t.Fatalf("partial receipt not immutable: %d, %v", receiptRows, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_location_history
	 (rider_identity_id,tenant_id,country,sequence,latitude,longitude,accuracy_m,updated_at,expires_at)
	 VALUES($1,$2,'IN',2,13.1,80.2,5,$3,$4)`, rider, tenant, now, now.Add(-time.Minute)); err == nil {
		t.Fatal("direct location insertion revived scrubbed rider scope")
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.rider_profiles
	 SET full_name='reintroduced private name' WHERE id=$1`, profile); err == nil {
		t.Fatal("direct profile update revived scrubbed rider scope")
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.rider_submissions
	 SET documents='[{"kind":"IDENTITY","asset_id":"private-asset"}]'::jsonb
	 WHERE profile_id=$1`, profile); err == nil {
		t.Fatal("direct submission update revived scrubbed rider scope")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_locations
	 (rider_identity_id,tenant_id,country,sequence,latitude,longitude,accuracy_m,updated_at,expires_at)
	 VALUES($1,$2,'IN',2,13.1,80.2,5,$3,$4)`, rider, tenant, now,
		now.Add(-time.Minute)); err == nil {
		t.Fatal("legacy live location insertion revived scrubbed rider scope")
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.rider_location_history
	 SET accuracy_m=4 WHERE tenant_id=$1 AND country='NG' AND rider_identity_id=$2`,
		tenant, rider); err != nil {
		t.Fatalf("foreign-country location was unnecessarily fenced: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.privacy_erasure_runs
	 SET location_rows=0 WHERE request_id=$1`, request); err == nil {
		t.Fatal("owner scrub evidence was mutable")
	}
	down, readErr := os.ReadFile("../../migrations/fulfillment/000013_ephemeral_rider_location_scrub.down.sql")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("migration rollback dropped a used privacy fence")
	}
	var fencePresent bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger
	 WHERE tgrelid='fulfillment.rider_profiles'::regclass
	   AND tgname='rider_profile_privacy_fence')`).Scan(&fencePresent); err != nil || !fencePresent {
		t.Fatalf("rollback removed the live privacy fence: %v, %v", fencePresent, err)
	}
}
