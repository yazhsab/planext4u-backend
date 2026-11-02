//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

// Runs only within the existing disposable fulfillment lifecycle database.
func prepareDeliveryConsents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	for _, schema := range []string{"identity", "configuration"} {
		if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS identity CASCADE; DROP SCHEMA IF EXISTS configuration CASCADE`)
	})
	for _, migration := range []string{
		"platform/000001_service_roles", "platform/000002_commerce_roles", "platform/000003_transaction_roles", "platform/000007_transaction_runtime", "configuration/000001_configuration", "configuration/000005_delivery_consent_policy",
		"identity/000001_identity", "identity/000002_profile_contacts", "identity/000003_profile_locales", "identity/000004_account_lifecycle", "identity/000005_security_evidence_append_only", "identity/000006_scoped_consent_decisions",
	} {
		data, err := os.ReadFile("../../migrations/" + migration + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", migration, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO configuration.snapshots(tenant_id,country,revision,document,published_at) VALUES($1,'IN',1,'{"consent_policies":[{"purpose":"LOCATION_DELIVERY","policy_version":"delivery-1","required":false}]}',$2)`, fulfillmentTenantOne, now); err != nil {
		t.Fatal(err)
	}
	repository, _ := identity.NewPostgresRepository(pool)
	for _, rider := range []string{fulfillmentRiderOne, fulfillmentRiderTwo} {
		if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at) VALUES($1,$2,'TEST',$1,$3)`, rider, fulfillmentTenantOne, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.RecordConsent(ctx, rider, identity.Consent{Country: "IN", EvidenceID: "initial-" + rider, Purpose: identity.ConsentLocationDelivery, Granted: true, PolicyVersion: "delivery-1", RecordedAt: now, ExpectedVersion: new(int64(0))}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func verifyDeliveryConsentEnforcement(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, rider Actor, now time.Time) {
	t.Helper()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["role"] = "planext4u_fulfillment_runtime"
	runtimePool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	limited, _ := NewPostgresService(runtimePool, func() time.Time { return now })
	var execute, selectIdentity, selectConfig, mutateEvidence bool
	if err := pool.QueryRow(ctx, `SELECT has_function_privilege('planext4u_fulfillment_runtime','identity.delivery_consent_allowed(text,character,text,text)','EXECUTE'),has_table_privilege('planext4u_fulfillment_runtime','identity.consent_evidence','SELECT'),has_table_privilege('planext4u_fulfillment_runtime','configuration.snapshots','SELECT'),has_table_privilege('planext4u_identity_runtime','identity.consent_evidence','UPDATE,DELETE,TRUNCATE')`).Scan(&execute, &selectIdentity, &selectConfig, &mutateEvidence); err != nil || !execute || selectIdentity || selectConfig || mutateEvidence {
		t.Fatalf("bounded consent privileges %v %v %v %v: %v", execute, selectIdentity, selectConfig, mutateEvidence, err)
	}
	if _, err := limited.Location(Actor{TenantID: rider.TenantID, Country: "IN", Subject: fulfillmentDispatch, Roles: []string{"DISPATCH"}}, rider.Subject); err != nil {
		t.Fatalf("restricted runtime current grant: %v", err)
	}
	repository, _ := identity.NewPostgresRepository(pool)
	withdraw := func(id string) identity.Consent {
		value, err := repository.RecordConsent(ctx, rider.Subject, identity.Consent{Country: "IN", EvidenceID: id, Purpose: identity.ConsentLocationDelivery, Granted: false, PolicyVersion: "delivery-1", RecordedAt: now}, now)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	withdrawn := withdraw("withdraw-delivery")
	if _, _, err := limited.StartDuty(rider, "duty-start-postgres-0001", "600001"); !errors.Is(err, ErrDeliveryConsentRequired) {
		t.Fatalf("withdrawn duty replay: %v", err)
	}
	input := LocationUpdate{Sequence: 1, Point: Point{Latitude: 13.0828, Longitude: 80.2708}, AccuracyM: 8, CapturedAt: now}
	if _, _, err := limited.UpdateLocation(rider, "rider-location-postgres-0001", input); !errors.Is(err, ErrDeliveryConsentRequired) {
		t.Fatalf("withdrawn location replay: %v", err)
	}
	if _, err := limited.RecoverOffline(rider, []OfflineCommand{{DeviceSequence: 3, CommandID: "withdrawn-offline-location", Kind: "LOCATION", Payload: map[string]any{"latitude": 13.0828, "longitude": 80.2708, "accuracy_m": 8.0, "captured_at": now.Format(time.RFC3339)}}}); !errors.Is(err, ErrDeliveryConsentRequired) {
		t.Fatalf("withdrawn offline: %v", err)
	}
	if _, err := limited.Location(Actor{TenantID: rider.TenantID, Country: "IN", Subject: fulfillmentDispatch, Roles: []string{"DISPATCH"}}, rider.Subject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("withdrawn location read: %v", err)
	}
	if _, err := repository.RecordConsent(ctx, rider.Subject, identity.Consent{Country: "IN", EvidenceID: "stale-delivery-grant", Purpose: identity.ConsentLocationDelivery, Granted: true, PolicyVersion: "delivery-1", RecordedAt: now, ExpectedVersion: new(int64(1))}, now); !errors.Is(err, identity.ErrConsentConflict) {
		t.Fatalf("stale grant after withdrawal: %v", err)
	}
	if _, err := repository.RecordConsent(ctx, rider.Subject, identity.Consent{Country: "IN", EvidenceID: "explicit-delivery-grant", Purpose: identity.ConsentLocationDelivery, Granted: true, PolicyVersion: "delivery-1", RecordedAt: now, ExpectedVersion: &withdrawn.Version}, now); err != nil {
		t.Fatal(err)
	}
	// The policy owner changes its version. A grant under the old notice cannot
	// authorize processing, even when an application still caches that notice.
	if _, err := pool.Exec(ctx, `UPDATE configuration.snapshots SET document='{"consent_policies":[{"purpose":"LOCATION_DELIVERY","policy_version":"delivery-2"}]}' WHERE tenant_id=$1 AND country='IN'`, rider.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := limited.UpdateLocation(rider, "changed-policy-location", input); !errors.Is(err, ErrDeliveryConsentRequired) {
		t.Fatalf("old policy grant: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE configuration.snapshots SET document='{"consent_policies":[{"purpose":"LOCATION_DELIVERY","policy_version":"delivery-1"}]}' WHERE tenant_id=$1 AND country='IN'`, rider.TenantID); err != nil {
		t.Fatal(err)
	}
	// Queue an actual location command behind an uncommitted withdrawal. The
	// blocked command must read the post-commit decision, not an old snapshot.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "consent:"+rider.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.consent_evidence(evidence_id,identity_id,purpose,granted,policy_version,recorded_at,version,country) SELECT 'queued-withdrawal',$1,'LOCATION_DELIVERY',false,'delivery-1',$2,max(version)+1,'IN' FROM identity.consent_evidence WHERE identity_id=$1`, rider.Subject, now); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, _, err := limited.UpdateLocation(rider, "location-behind-withdrawal", input); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("command passed uncommitted withdrawal: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrDeliveryConsentRequired) {
			t.Fatalf("queued command used stale grant: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Restore an explicit current grant for the remainder of the unchanged
	// business lifecycle. No financial/order/task state is reseeded.
	var version int64
	if err := pool.QueryRow(ctx, `SELECT max(version) FROM identity.consent_evidence WHERE identity_id=$1`, rider.Subject).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordConsent(ctx, rider.Subject, identity.Consent{Country: "IN", EvidenceID: "restore-delivery-grant", Purpose: identity.ConsentLocationDelivery, Granted: true, PolicyVersion: "delivery-1", RecordedAt: now, ExpectedVersion: &version}, now); err != nil {
		t.Fatal(err)
	}
}
