//go:build integration

package notification

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func notificationPrivacyDatabase(t *testing.T) (context.Context, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("NOTIFICATION_DATABASE_TEST_URL")
	if raw == "" {
		t.Skip("NOTIFICATION_DATABASE_TEST_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Path != "/planext4u_phase0_verification" && u.Path != "/planext4u_test" && u.Path != "/planext4u_notification_test") ||
		(u.Hostname() != "postgres" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("notification privacy integration requires an explicitly disposable verification database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS notification CASCADE;
        DROP SCHEMA IF EXISTS messaging CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS notification CASCADE;
            DROP SCHEMA IF EXISTS messaging CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`)
	})
	var paths []string
	for _, service := range []string{"platform", "identity", "messaging", "notification"} {
		selected, globErr := filepath.Glob("../../migrations/" + service + "/*.up.sql")
		if globErr != nil || len(selected) == 0 {
			t.Fatalf("discover %s migrations: %v", service, globErr)
		}
		sort.Strings(selected)
		paths = append(paths, selected...)
	}
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, e := c.Exec(ctx, `SET ROLE planext4u_notification_runtime`)
		return e
	}
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return ctx, pool, runtime
}

func TestNotificationPrivacyPushScrubCountryHoldQueuedSendAndRollback(t *testing.T) {
	ctx, pool, runtime := notificationPrivacyDatabase(t)
	tenant, subject, otherSubject, request := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
        VALUES($1,$2,'synthetic',$1,$3)`, subject, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
        (id,identity_id,status,reason,requested_at,effective_at,country)
        VALUES($1,$2,'SCHEDULED','synthetic notification privacy verification',$3,$4,'IN')`,
		request, subject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ country, owner string }{
		{"IN", subject}, {"GB", subject}, {"IN", otherSubject},
	} {
		insertPrivacyDevice(t, ctx, pool, tenant, row.country, row.owner)
	}
	queuedID := insertPrivacyDelivery(t, ctx, pool, tenant, "IN", subject, "QUEUED")
	sendingID := insertPrivacyDelivery(t, ctx, pool, tenant, "IN", subject, "QUEUED")
	sentID := insertPrivacyDelivery(t, ctx, pool, tenant, "IN", subject, "SENT")
	foreignID := insertPrivacyDelivery(t, ctx, pool, tenant, "GB", subject, "QUEUED")
	otherID := insertPrivacyDelivery(t, ctx, pool, tenant, "IN", otherSubject, "QUEUED")
	if _, err := pool.Exec(ctx, `INSERT INTO notification.provider_receipts
        (tenant_id,receipt_id,delivery_id,provider_message_id,status,occurred_at)
        VALUES($1,$2,$3,'provider-before-scrub','DELIVERED',$4)`, tenant, uuid.NewString(), sentID, now); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err := pool.QueryRow(ctx, `SELECT has_function_privilege('planext4u_notification_runtime',
        'notification.privacy_scrub_push_tokens(text)','EXECUTE')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("runtime must not execute owner scrub: allowed=%v err=%v", allowed, err)
	}
	if _, _, err := runNotificationPrivacyScrub(ctx, pool, request); err == nil {
		t.Fatal("push scrub ran without current country policy and READY task")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
        (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
        VALUES($1,'IN','notification',1,'notification-policy-v1',interval '0',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
        (request_id,owner,status,updated_at) VALUES($1,'notification','READY',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
        (request_id,owner,reason_reference,recorded_at)
        VALUES($1,'notification','synthetic-notification-hold',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runNotificationPrivacyScrub(ctx, pool, request); err == nil {
		t.Fatal("push scrub ignored an active legal hold")
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.deletion_legal_holds
        SET released_at=clock_timestamp(),release_reason_reference='synthetic notification hold release'
        WHERE request_id=$1`, request); err != nil {
		t.Fatal(err)
	}

	// A worker that claims a queued delivery before the scrub owns a durable
	// SENDING state. The scrub waits for that write and refuses to commit while
	// a provider call may still be in flight.
	worker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Exec(ctx, `UPDATE notification.deliveries SET status='SENDING'
        WHERE id=$1`, sendingID); err != nil {
		_ = worker.Rollback(ctx)
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, scrubErr := runNotificationPrivacyScrub(ctx, pool, request)
		result <- scrubErr
	}()
	select {
	case err = <-result:
		_ = worker.Rollback(ctx)
		t.Fatalf("scrub bypassed the uncommitted worker claim: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = worker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err == nil {
			t.Fatal("scrub committed while a delivery was SENDING")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scrub did not resolve after worker claim committed")
	}
	var retained int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notification.push_device
        WHERE tenant_id=$1 AND country='IN' AND subject_id=$2`, tenant, subject).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("failed scrub removed token: rows=%d err=%v", retained, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE notification.deliveries SET status='SENT' WHERE id=$1`, sendingID); err != nil {
		t.Fatal(err)
	}
	deviceRows, deliveryRows, err := runNotificationPrivacyScrub(ctx, pool, request)
	if err != nil || deviceRows != 1 || deliveryRows != 1 {
		t.Fatalf("scrub devices=%d deliveries=%d err=%v", deviceRows, deliveryRows, err)
	}
	deviceRows, deliveryRows, err = runNotificationPrivacyScrub(ctx, pool, request)
	if err != nil || deviceRows != 1 || deliveryRows != 1 {
		t.Fatalf("idempotent scrub devices=%d deliveries=%d err=%v", deviceRows, deliveryRows, err)
	}
	for _, row := range []struct {
		id, status, reason string
	}{
		{queuedID, "SUPPRESSED", "PRIVACY_ERASURE"},
		{sendingID, "SENT", ""}, {sentID, "SENT", ""},
		{foreignID, "QUEUED", ""}, {otherID, "QUEUED", ""},
	} {
		var status string
		var reason *string
		if err = pool.QueryRow(ctx, `SELECT status,suppression_reason FROM notification.deliveries
            WHERE id=$1`, row.id).Scan(&status, &reason); err != nil || status != row.status ||
			(reason != nil && *reason != row.reason) || (reason == nil && row.reason != "") {
			t.Fatalf("delivery %s status=%q reason=%v err=%v", row.id, status, reason, err)
		}
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notification.push_device
        WHERE tenant_id=$1 AND subject_id=$2`, tenant, subject).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("other-country device was lost: rows=%d err=%v", retained, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notification.provider_receipts
        WHERE delivery_id=$1`, sentID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("provider receipt was lost: rows=%d err=%v", retained, err)
	}
	var outcome, policy, evidence string
	if err = pool.QueryRow(ctx, `SELECT outcome,policy_version,evidence_reference
        FROM identity.deletion_owner_receipts WHERE request_id=$1 AND owner='notification'`, request).
		Scan(&outcome, &policy, &evidence); err != nil || outcome != "RETAINED" ||
		policy != "notification-policy-v1" || evidence != "notification-push-scrub:"+request {
		t.Fatalf("owner receipt=%q %q %q err=%v", outcome, policy, evidence, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notification.push_device
        (id,tenant_id,country,subject_id,device_reference,platform,locale,token_ciphertext,
         token_key_version,enabled,updated_at)
        VALUES($1,$2,'IN',$3,$4,'ANDROID','en',$5,1,true,$6)`,
		uuid.NewString(), tenant, subject, uuid.NewString(), []byte("restored-token"), now); err == nil {
		t.Fatal("post-scrub token reintroduction succeeded")
	}
	if _, err = pool.Exec(ctx, `UPDATE notification.deliveries SET status='QUEUED'
        WHERE id=$1`, queuedID); err == nil {
		t.Fatal("suppressed delivery was requeued")
	}
	if _, err = pool.Exec(ctx, `UPDATE notification.deliveries SET status='SENDING'
        WHERE id=$1`, sentID); err == nil {
		t.Fatal("previously sent delivery became sendable again")
	}
	if _, err = pool.Exec(ctx, `UPDATE notification.privacy_push_scrub_runs SET device_rows=0
        WHERE request_id=$1`, request); err == nil {
		t.Fatal("notification scrub evidence was mutable")
	}
	if _, err = runtime.Exec(ctx, `SELECT notification.privacy_scrub_push_tokens($1)`, request); err == nil {
		t.Fatal("notification runtime invoked owner-only scrub")
	}
	key := bytes.Repeat([]byte{0x45}, 32)
	cipher, err := NewAESGCMTokenCipher(map[int][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewPostgresRepository(runtime, cipher)
	if err != nil {
		t.Fatal(err)
	}
	provider := &postgresNotificationProvider{}
	service, err := NewService(repository, map[Channel]Provider{ChannelPush: provider}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Process(ctx, tenant, queuedID); err != nil || provider.calls != 0 {
		t.Fatalf("suppressed queued delivery sent: calls=%d err=%v", provider.calls, err)
	}
	if err = pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_notification_runtime',
        'notification.privacy_push_scrub_runs','SELECT')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("runtime could read owner privacy evidence: allowed=%v err=%v", allowed, err)
	}
	if _, err = runtime.Exec(ctx, `INSERT INTO notification.deliveries
        (id,tenant_id,country,subject_id,recipient_ref,channel,purpose,template_key,
         template_version,locale,rendered_subject,rendered_body,status,idempotency_key,
         created_at,updated_at)
        VALUES($1,$2,'IN',$3,'device-private','PUSH','TRANSACTIONAL','status',1,'en',
         'Private','Private body','QUEUED',$4,$5,$5)`,
		uuid.NewString(), tenant, subject, uuid.NewString(), now); err == nil {
		t.Fatal("runtime queued a deleted notification subject")
	}
	down, err := os.ReadFile("../../migrations/notification/000006_privacy_push_token_scrub.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("notification privacy rollback removed live evidence")
	}
	var installed bool
	if err = pool.QueryRow(ctx, `SELECT to_regprocedure('notification.privacy_scrub_push_tokens(text)') IS NOT NULL`).Scan(&installed); err != nil || !installed {
		t.Fatalf("failed rollback removed privacy fence: installed=%v err=%v", installed, err)
	}
}

func insertPrivacyDevice(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, country, subject string) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO notification.push_device
        (id,tenant_id,country,subject_id,device_reference,platform,locale,token_ciphertext,
         token_key_version,enabled,updated_at)
        VALUES($1,$2,$3,$4,$5,'ANDROID','en',$6,1,true,$7)`,
		uuid.NewString(), tenant, country, subject, uuid.NewString(), []byte("synthetic-ciphertext"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func insertPrivacyDelivery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, country, subject, status string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := pool.Exec(ctx, `INSERT INTO notification.deliveries
        (id,tenant_id,country,subject_id,recipient_ref,channel,purpose,template_key,
         template_version,locale,rendered_subject,rendered_body,status,idempotency_key,
         created_at,updated_at)
        VALUES($1,$2,$3,$4,'device-private','PUSH','TRANSACTIONAL','status',1,'en',
         'Private','Private body',$5,$6,$7,$7)`, id, tenant, country, subject, status, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func runNotificationPrivacyScrub(ctx context.Context, pool *pgxpool.Pool, request string) (int, int, error) {
	var outcome, evidence string
	var deviceRows, deliveryRows int
	err := pool.QueryRow(ctx, `SELECT outcome,evidence_reference,device_rows,delivery_rows
        FROM notification.privacy_scrub_push_tokens($1)`, request).
		Scan(&outcome, &evidence, &deviceRows, &deliveryRows)
	if err != nil {
		return 0, 0, err
	}
	if outcome != "RETAINED" || evidence != "notification-push-scrub:"+request {
		return 0, 0, fmt.Errorf("unexpected notification privacy evidence %q %q", outcome, evidence)
	}
	return deviceRows, deliveryRows, nil
}
