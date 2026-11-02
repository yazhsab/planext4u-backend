//go:build integration

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func messagingPrivacyDatabase(t *testing.T) (context.Context, *pgxpool.Pool, *pgxpool.Pool, *pgxpool.Pool, *pgxpool.Pool, func(string)) {
	t.Helper()
	raw := os.Getenv("MESSAGING_DATABASE_TEST_URL")
	if raw == "" {
		t.Skip("MESSAGING_DATABASE_TEST_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Path != "/planext4u_phase0_verification" && u.Path != "/planext4u_test" && u.Path != "/planext4u_messaging_test") ||
		(u.Hostname() != "postgres" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("messaging privacy integration requires an explicitly disposable verification database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS messaging CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS messaging CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`)
	})
	apply := func(path string) {
		t.Helper()
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(body)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	for _, service := range []string{"platform", "identity"} {
		paths, globErr := filepath.Glob("../../migrations/" + service + "/*.up.sql")
		if globErr != nil || len(paths) == 0 {
			t.Fatalf("discover %s migrations: %v", service, globErr)
		}
		sort.Strings(paths)
		for _, path := range paths {
			apply(path)
		}
	}
	for _, version := range []string{"000001_messaging", "000002_notification_outbox_writer", "000003_notification_schema_usage"} {
		apply("../../migrations/messaging/" + version + ".up.sql")
	}
	rolePool := func(role string) *pgxpool.Pool {
		t.Helper()
		config, configErr := pgxpool.ParseConfig(raw)
		if configErr != nil {
			t.Fatal(configErr)
		}
		config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
			_, roleErr := connection.Exec(ctx, "SET ROLE "+role)
			return roleErr
		}
		restricted, newErr := pgxpool.NewWithConfig(ctx, config)
		if newErr != nil {
			t.Fatal(newErr)
		}
		t.Cleanup(restricted.Close)
		return restricted
	}
	owner := rolePool("planext4u_messaging_owner")
	runtime := rolePool("planext4u_messaging_runtime")
	producer := rolePool("planext4u_notification_runtime")
	return ctx, pool, owner, runtime, producer, apply
}

func privacyNotificationMessage(tenant, country, id string) Message {
	message := syntheticMessage(id, 1)
	message.TenantID = tenant
	message.Country = country
	message.Producer = "notification-service"
	message.EventType = "planext4u.notification.delivery.requested.v1"
	message.AggregateType = "notification-delivery"
	message.AggregateID = uuid.NewString()
	message.Data = map[string]any{"delivery_id": message.AggregateID, "channel": "PUSH"}
	return message
}

func insertPrivacyOutbox(ctx context.Context, pool *pgxpool.Pool, message Message, subject, state string) error {
	envelope, err := json.Marshal(message)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = pool.Exec(ctx, `INSERT INTO messaging.outbox
     (event_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,envelope,
      state,attempts,next_attempt_at,created_at,published_at,dead_at,
      privacy_country,privacy_subject_id)
     VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,0,$8::timestamptz,$8::timestamptz,
       CASE WHEN $7='PUBLISHED' THEN $8::timestamptz ELSE NULL END,
       CASE WHEN $7='DEAD_LETTER' THEN $8::timestamptz ELSE NULL END,$9,$10)`,
		message.ID, message.TenantID, message.AggregateType, message.AggregateID,
		message.AggregateVersion, envelope, state, now, message.Country, subject)
	return err
}

func runMessagingPrivacyScrub(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, request string) (int, int64, error) {
	var outcome, evidence string
	var deleted int
	var published int64
	err := query.QueryRow(ctx, `SELECT outcome,evidence_reference,deleted_rows,published_rows
     FROM messaging.privacy_scrub_notification_outbox($1)`, request).
		Scan(&outcome, &evidence, &deleted, &published)
	if err != nil {
		return 0, 0, err
	}
	if outcome != "RETAINED" || evidence != "messaging-notification-outbox-scrub:"+request {
		return 0, 0, errors.New("messaging scrub returned an invalid receipt")
	}
	return deleted, published, nil
}

func TestMessagingPrivacyOutboxScrubScopeRaceLegacyAndRollback(t *testing.T) {
	ctx, pool, owner, runtime, producer, apply := messagingPrivacyDatabase(t)
	tenant, otherTenant := uuid.NewString(), uuid.NewString()
	subject, otherSubject, request := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
     VALUES($1,$2,'synthetic',$1,$3)`, subject, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
     (id,identity_id,status,reason,requested_at,effective_at,country)
     VALUES($1,$2,'SCHEDULED','synthetic messaging privacy verification',$3,$4,'IN')`,
		request, subject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// This row predates independent notification subject metadata. Its country
	// and recipient cannot be trusted for a subject-level deletion decision.
	legacy := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	legacyEnvelope, _ := json.Marshal(legacy)
	if _, err := pool.Exec(ctx, `INSERT INTO messaging.outbox
     (event_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,envelope,
      state,attempts,next_attempt_at,created_at)
     VALUES($1,$2,$3,$4,$5,$6::jsonb,'PENDING',0,$7,$7)`,
		legacy.ID, tenant, legacy.AggregateType, legacy.AggregateID,
		legacy.AggregateVersion, legacyEnvelope, now); err != nil {
		t.Fatal(err)
	}
	apply("../../migrations/messaging/000004_notification_subject_scope.up.sql")
	apply("../../migrations/messaging/000005_privacy_notification_outbox_scrub.up.sql")
	store, err := NewPostgresStore(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("messaging privacy schema not ready: %v", err)
	}
	pending := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	dead := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	published := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	leased := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	foreignCountry := privacyNotificationMessage(tenant, "GB", uuid.NewString())
	otherPerson := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	foreignTenant := privacyNotificationMessage(otherTenant, "IN", uuid.NewString())
	for _, row := range []struct {
		message Message
		subject string
		state   string
	}{
		{pending, subject, "PENDING"}, {dead, subject, "DEAD_LETTER"},
		{published, subject, "PUBLISHED"}, {leased, subject, "LEASED"},
		{foreignCountry, subject, "PENDING"}, {otherPerson, otherSubject, "PENDING"},
		{foreignTenant, subject, "PENDING"},
	} {
		if err := insertPrivacyOutbox(ctx, producer, row.message, row.subject, row.state); err != nil {
			t.Fatalf("insert privacy fixture %s: %v", row.message.ID, err)
		}
	}
	if _, _, err := runMessagingPrivacyScrub(ctx, owner, request); err == nil {
		t.Fatal("messaging scrub ran without a current country policy and READY task")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
     (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
     VALUES($1,'IN','messaging',1,'messaging-policy-v1',interval '0',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
     (request_id,owner,status,updated_at) VALUES($1,'messaging','READY',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
     (request_id,owner,reason_reference,recorded_at)
     VALUES($1,'messaging','synthetic-messaging-hold',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMessagingPrivacyScrub(ctx, owner, request); err == nil {
		t.Fatal("messaging scrub ignored an active legal hold")
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.deletion_legal_holds
     SET released_at=clock_timestamp(),release_reason_reference='synthetic messaging hold release'
     WHERE request_id=$1`, request); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMessagingPrivacyScrub(ctx, owner, request); err == nil ||
		!strings.Contains(err.Error(), "unclassified legacy") {
		t.Fatalf("active legacy notification escaped quarantine: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE messaging.outbox SET state='PUBLISHED',published_at=$2
     WHERE event_id=$1`, legacy.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMessagingPrivacyScrub(ctx, owner, request); err == nil ||
		!strings.Contains(err.Error(), "in-flight publisher") {
		t.Fatalf("leased notification escaped scrub barrier: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE messaging.outbox SET state='PUBLISHED',published_at=$2
     WHERE event_id=$1`, leased.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Exec(ctx, `INSERT INTO messaging.inbox
     (consumer,event_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,
      state,lease_until,processed_at)
     VALUES('privacy-in-flight',$1,$2,$3,$4,1,'PROCESSING',$5,NULL)`,
		published.ID, tenant, published.AggregateType, published.AggregateID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMessagingPrivacyScrub(ctx, owner, request); err == nil ||
		!strings.Contains(err.Error(), "in-flight consumer") {
		t.Fatalf("processing inbox escaped scrub barrier: %v", err)
	}
	if _, err := runtime.Exec(ctx, `UPDATE messaging.inbox
     SET state='DONE',lease_until=NULL,processed_at=$2
     WHERE consumer='privacy-in-flight' AND event_id=$1`, published.ID, now); err != nil {
		t.Fatal(err)
	}
	// Keep the successful scrub transaction open while a producer tries to
	// reintroduce the subject. The table lock makes the writer wait for the
	// durable run; after commit the insert trigger must reject it.
	ownerTx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deletedRows, publishedRows, err := runMessagingPrivacyScrub(ctx, ownerTx, request)
	if err != nil || deletedRows != 2 || publishedRows != 2 {
		_ = ownerTx.Rollback(ctx)
		t.Fatalf("scrub deleted=%d published=%d err=%v", deletedRows, publishedRows, err)
	}
	queuedResult := make(chan error, 1)
	queued := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	go func() { queuedResult <- insertPrivacyOutbox(ctx, producer, queued, subject, "PENDING") }()
	select {
	case err := <-queuedResult:
		_ = ownerTx.Rollback(ctx)
		t.Fatalf("producer bypassed uncommitted privacy fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := ownerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-queuedResult:
		if err == nil || !strings.Contains(err.Error(), "deleted messaging subject cannot be queued") {
			t.Fatalf("queued producer reintroduction was not denied by the privacy fence: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued producer did not resume after scrub commit")
	}
	if deletedRows, publishedRows, err = runMessagingPrivacyScrub(ctx, owner, request); err != nil ||
		deletedRows != 2 || publishedRows != 2 {
		t.Fatalf("idempotent scrub deleted=%d published=%d err=%v", deletedRows, publishedRows, err)
	}
	for _, id := range []string{pending.ID, dead.ID} {
		var present bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messaging.outbox WHERE event_id=$1)`, id).
			Scan(&present); err != nil || present {
			t.Fatalf("unsent envelope %s retained: present=%v err=%v", id, present, err)
		}
	}
	var tombstones int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messaging.privacy_outbox_scrubbed_events
     WHERE request_id=$1`, request).Scan(&tombstones); err != nil || tombstones != 2 {
		t.Fatalf("immutable event tombstones=%d err=%v", tombstones, err)
	}
	for _, id := range []string{published.ID, leased.ID, foreignCountry.ID, otherPerson.ID, foreignTenant.ID, legacy.ID} {
		var present bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messaging.outbox WHERE event_id=$1)`, id).
			Scan(&present); err != nil || !present {
			t.Fatalf("retained/foreign event %s lost: present=%v err=%v", id, present, err)
		}
	}
	if err := insertPrivacyOutbox(ctx, producer, privacyNotificationMessage(tenant, "IN", pending.ID), otherSubject, "PENDING"); err == nil ||
		!strings.Contains(err.Error(), "scrubbed outbox event ID cannot be reused") {
		t.Fatalf("deleted event ID reintroduction was not denied by its tombstone: %v", err)
	}
	if err := insertPrivacyOutbox(ctx, producer, privacyNotificationMessage(tenant, "IN", uuid.NewString()), subject, "PENDING"); err == nil ||
		!strings.Contains(err.Error(), "deleted messaging subject cannot be queued") {
		t.Fatalf("scrubbed subject insertion was not denied by the privacy fence: %v", err)
	}
	// The earlier notification-scope trigger must also reject a new legacy
	// envelope after a scrub; the scrub trigger cannot infer its owner.
	newLegacy := privacyNotificationMessage(tenant, "IN", uuid.NewString())
	legacyPayload, err := json.Marshal(newLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Exec(ctx, `INSERT INTO messaging.outbox
     (event_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,envelope,
      state,attempts,next_attempt_at,created_at)
     VALUES($1,$2,$3,$4,$5,$6::jsonb,'PENDING',0,$7,$7)`,
		newLegacy.ID, tenant, newLegacy.AggregateType, newLegacy.AggregateID,
		newLegacy.AggregateVersion, legacyPayload, now); err == nil ||
		!strings.Contains(err.Error(), "notification outbox privacy scope is missing or inconsistent") {
		t.Fatalf("new unclassified notification escaped the scope guard after scrub: %v", err)
	}
	if err := insertPrivacyOutbox(ctx, producer, privacyNotificationMessage(tenant, "IN", uuid.NewString()), otherSubject, "PENDING"); err != nil {
		t.Fatalf("unrelated subject was blocked: %v", err)
	}
	if _, err := runtime.Exec(ctx, `UPDATE messaging.outbox SET state='PENDING'
     WHERE event_id=$1`, published.ID); err == nil ||
		!strings.Contains(err.Error(), "deleted messaging subject cannot be requeued") {
		t.Fatalf("runtime published-event replay was not denied by the privacy fence: %v", err)
	}
	if _, err := runtime.Exec(ctx, `UPDATE messaging.outbox SET state='PENDING'
     WHERE event_id=$1`, legacy.ID); err == nil ||
		!strings.Contains(err.Error(), "unclassified legacy notification event cannot be requeued") {
		t.Fatalf("runtime legacy-event replay was not denied by quarantine: %v", err)
	}
	if err := store.Replay(ctx, tenant, "IN", dead.ID, now.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dead-letter replay recreated a deleted event: %v", err)
	}
	for _, row := range []struct {
		name    string
		message Message
	}{
		{"deleted-dead", dead}, {"retained-published", leased},
		{"unclassified-legacy", legacy},
	} {
		if outcome, err := store.Begin(ctx, "privacy-projection-"+row.name, row.message,
			now.Add(time.Minute), time.Minute); err == nil || outcome != "" ||
			!strings.Contains(err.Error(), "scrubbed or unclassified notification cannot be reprocessed") {
			t.Fatalf("%s was processed after scrub: outcome=%q err=%v", row.name, outcome, err)
		}
	}
	if _, err := runtime.Exec(ctx, `SELECT messaging.privacy_scrub_notification_outbox($1)`, request); err == nil {
		t.Fatal("ordinary messaging runtime invoked owner-only scrub")
	}
	if _, err := runtime.Exec(ctx, `SELECT count(*) FROM messaging.privacy_outbox_scrub_runs`); err == nil {
		t.Fatal("ordinary messaging runtime read private scrub evidence")
	}
	var outcome, policy, evidence string
	if err := pool.QueryRow(ctx, `SELECT outcome,policy_version,evidence_reference
     FROM identity.deletion_owner_receipts WHERE request_id=$1 AND owner='messaging'`, request).
		Scan(&outcome, &policy, &evidence); err != nil || outcome != "RETAINED" ||
		policy != "messaging-policy-v1" || evidence != "messaging-notification-outbox-scrub:"+request {
		t.Fatalf("messaging owner receipt=%q %q %q err=%v", outcome, policy, evidence, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE messaging.privacy_outbox_scrub_runs SET deleted_rows=0
     WHERE request_id=$1`, request); err == nil {
		t.Fatal("messaging scrub evidence was mutable")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM messaging.privacy_outbox_scrubbed_events
     WHERE request_id=$1`, request); err == nil {
		t.Fatal("messaging event tombstones were mutable")
	}
	for _, path := range []string{
		"../../migrations/messaging/000005_privacy_notification_outbox_scrub.down.sql",
		"../../migrations/identity/000027_messaging_erasure_scope.down.sql",
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := pool.Exec(ctx, string(body)); err == nil {
			t.Fatalf("privacy evidence allowed rollback of %s", path)
		}
	}
}
