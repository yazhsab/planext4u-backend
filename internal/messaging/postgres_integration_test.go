//go:build integration

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresOutboxInboxLeasesOrderingAndDLQ(t *testing.T) {
	databaseURL := os.Getenv("MESSAGING_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("MESSAGING_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS messaging CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS messaging CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/messaging/000001_messaging.up.sql",
		"../../migrations/messaging/000002_notification_outbox_writer.up.sql",
		"../../migrations/messaging/000003_notification_schema_usage.up.sql",
		"../../migrations/messaging/000004_notification_subject_scope.up.sql",
		"../../migrations/messaging/000005_privacy_notification_outbox_scrub.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	message := syntheticMessage("00000000-0000-4000-8000-000000000001", 1)
	if err := store.Enqueue(ctx, message, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, message, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate enqueue error=%v", err)
	}
	claimed, err := store.Claim(ctx, "worker-a", now, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].LeaseOwner != "worker-a" || claimed[0].Message.ID != message.ID {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	secondClaim, err := store.Claim(ctx, "worker-b", now, time.Minute, 10)
	if err != nil || len(secondClaim) != 0 {
		t.Fatalf("second claim=%#v err=%v", secondClaim, err)
	}
	state, err := store.MarkFailed(ctx, message.ID, "worker-a", now, now.Add(time.Second), "BROKER_UNAVAILABLE", 1)
	if err != nil || state != OutboxDead {
		t.Fatalf("failed state=%s err=%v", state, err)
	}
	foreign := syntheticMessage("00000000-0000-4000-8000-000000000005", 1)
	foreign.Country = "US"
	if err := store.Enqueue(ctx, foreign, now); err != nil {
		t.Fatal(err)
	}
	foreignClaim, err := store.Claim(ctx, "foreign-worker", now, time.Minute, 10)
	if err != nil || len(foreignClaim) != 1 || foreignClaim[0].Message.ID != foreign.ID {
		t.Fatalf("foreign claim=%#v err=%v", foreignClaim, err)
	}
	if _, err := store.MarkFailed(ctx, foreign.ID, "foreign-worker", now, now.Add(time.Second), "BROKER_UNAVAILABLE", 1); err != nil {
		t.Fatal(err)
	}
	dead, err := store.DeadLetters(ctx, message.TenantID, "IN", 10)
	if err != nil || len(dead) != 1 || dead[0].Attempts != 1 {
		t.Fatalf("dead=%#v err=%v", dead, err)
	}
	other, err := store.DeadLetters(ctx, "23bf7434-3643-49df-9928-c9169011f69d", "IN", 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-tenant dead=%#v err=%v", other, err)
	}
	if err := store.Replay(ctx, message.TenantID, "IN", foreign.ID, now.Add(2*time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-country replay=%v", err)
	}
	foreignDead, err := store.DeadLetters(ctx, message.TenantID, "US", 10)
	if err != nil || len(foreignDead) != 1 || foreignDead[0].Message.ID != foreign.ID {
		t.Fatalf("foreign dead letter=%#v err=%v", foreignDead, err)
	}
	if err := store.Replay(ctx, message.TenantID, "IN", message.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.Claim(ctx, "worker-b", now.Add(2*time.Second), time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("replayed claim=%#v err=%v", claimed, err)
	}
	if err := store.MarkPublished(ctx, message.ID, "worker-b", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.Begin(ctx, "projection", message, now, time.Minute); err != nil || outcome != ConsumeProcessed {
		t.Fatalf("begin=%s err=%v", outcome, err)
	}
	if outcome, err := store.Begin(ctx, "projection", message, now, time.Minute); !errors.Is(err, ErrBusy) || outcome != "" {
		t.Fatalf("active duplicate begin=%s err=%v", outcome, err)
	}
	if err := store.Complete(ctx, "projection", message, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.Begin(ctx, "projection", message, now.Add(2*time.Second), time.Minute); err != nil || outcome != ConsumeDuplicate {
		t.Fatalf("completed duplicate=%s err=%v", outcome, err)
	}
	third := syntheticMessage("00000000-0000-4000-8000-000000000003", 3)
	if outcome, err := store.Begin(ctx, "projection", third, now.Add(2*time.Second), time.Minute); !errors.Is(err, ErrOutOfOrder) || outcome != ConsumeOutOfOrder {
		t.Fatalf("out of order=%s err=%v", outcome, err)
	}
	second := syntheticMessage("00000000-0000-4000-8000-000000000002", 2)
	if outcome, err := store.Begin(ctx, "projection", second, now.Add(2*time.Second), time.Minute); err != nil || outcome != ConsumeProcessed {
		t.Fatalf("second begin=%s err=%v", outcome, err)
	}
	if err := store.Release(ctx, "projection", second.ID); err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.Begin(ctx, "projection", second, now.Add(3*time.Second), time.Minute); err != nil || outcome != ConsumeProcessed {
		t.Fatalf("released retry=%s err=%v", outcome, err)
	}
}

func TestPostgresNotificationSubjectScopeIsProducerBoundAndLegacySafe(t *testing.T) {
	databaseURL := os.Getenv("MESSAGING_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("MESSAGING_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS messaging CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS messaging CASCADE`)
	})
	apply := func(path string) {
		t.Helper()
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/messaging/000001_messaging.up.sql",
		"../../migrations/messaging/000002_notification_outbox_writer.up.sql",
		"../../migrations/messaging/000003_notification_schema_usage.up.sql",
	} {
		apply(path)
	}
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	legacy := syntheticMessage("00000000-0000-4000-8000-000000000071", 1)
	legacy.Producer = "notification-service"
	legacy.EventType = "planext4u.notification.delivery.requested.v1"
	legacy.AggregateType = "notification-delivery"
	legacy.AggregateID = "00000000-0000-4000-8000-000000000072"
	legacy.Data = map[string]any{"delivery_id": legacy.AggregateID, "channel": "PUSH"}
	legacyEnvelope, _ := json.Marshal(legacy)
	if _, err := pool.Exec(ctx, `INSERT INTO messaging.outbox
	 (event_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,envelope,state,attempts,next_attempt_at,created_at)
	 VALUES($1,$2,$3,$4,$5,$6::jsonb,'PENDING',0,$7,$7)`, legacy.ID, legacy.TenantID,
		legacy.AggregateType, legacy.AggregateID, legacy.AggregateVersion, legacyEnvelope, now); err != nil {
		t.Fatal(err)
	}
	apply("../../migrations/messaging/000004_notification_subject_scope.up.sql")
	apply("../../migrations/messaging/000005_privacy_notification_outbox_scrub.up.sql")
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	var legacyUntagged bool
	if err := pool.QueryRow(ctx, `SELECT privacy_country IS NULL AND privacy_subject_id IS NULL
	 FROM messaging.outbox WHERE event_id=$1`, legacy.ID).Scan(&legacyUntagged); err != nil || !legacyUntagged {
		t.Fatalf("legacy scope changed: %v, %v", legacyUntagged, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE messaging.outbox SET state='LEASED',lease_owner='legacy-worker',
	 lease_until=$2 WHERE event_id=$1`, legacy.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("legacy dispatch state blocked: %v", err)
	}

	message := legacy
	message.ID = "00000000-0000-4000-8000-000000000073"
	message.AggregateID = "00000000-0000-4000-8000-000000000074"
	message.Data = map[string]any{"delivery_id": message.AggregateID, "channel": "PUSH"}
	if err := store.Enqueue(ctx, message, now); err == nil {
		t.Fatal("new notification event accepted without owner subject metadata")
	}
	const scopedInsertSQL = `INSERT INTO messaging.outbox
	 (event_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,envelope,state,attempts,next_attempt_at,created_at,privacy_country,privacy_subject_id)
	 VALUES($1,$2,$3,$4,$5,$6::jsonb,'PENDING',0,$7,$7,$8,$9)`
	insert := func(value Message, country, subject string) error {
		t.Helper()
		envelope, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		_, insertErr := pool.Exec(ctx, scopedInsertSQL,
			value.ID, value.TenantID, value.AggregateType, value.AggregateID, value.AggregateVersion,
			envelope, now, country, subject)
		return insertErr
	}
	insertAs := func(value Message, roleSQL string) error {
		t.Helper()
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, roleErr := tx.Exec(ctx, roleSQL); roleErr != nil {
			return roleErr
		}
		envelope, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		if _, insertErr := tx.Exec(ctx, scopedInsertSQL, value.ID, value.TenantID,
			value.AggregateType, value.AggregateID, value.AggregateVersion, envelope,
			now, "IN", "customer-private"); insertErr != nil {
			return insertErr
		}
		return tx.Commit(ctx)
	}
	if err := insert(message, "LK", "customer-private"); err == nil {
		t.Fatal("mismatched jurisdiction accepted")
	}
	if err := insert(message, "IN", "bad subject with spaces"); err == nil {
		t.Fatal("invalid subject accepted")
	}
	invalid := message
	invalid.Data = map[string]any{"delivery_id": legacy.AggregateID, "channel": "PUSH"}
	if err := insert(invalid, "IN", "customer-private"); err == nil {
		t.Fatal("mismatched aggregate-to-delivery binding accepted")
	}
	if err := insert(message, "IN", "customer-private"); err != nil {
		t.Fatalf("valid scoped notification rejected: %v", err)
	}
	roleMessage := message
	roleMessage.ID = "00000000-0000-4000-8000-000000000081"
	roleMessage.AggregateID = "00000000-0000-4000-8000-000000000082"
	roleMessage.Data = map[string]any{"delivery_id": roleMessage.AggregateID, "channel": "PUSH"}
	if err := insertAs(roleMessage, "SET LOCAL ROLE planext4u_messaging_runtime"); err == nil {
		t.Fatal("messaging runtime forged notification owner scope")
	}
	if err := insertAs(roleMessage, "SET LOCAL ROLE planext4u_notification_runtime"); err != nil {
		t.Fatalf("notification runtime could not write its scoped event: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE messaging.outbox SET privacy_subject_id='customer-alien'
	 WHERE event_id=$1`, message.ID); err == nil {
		t.Fatal("notification privacy scope was mutable")
	}
	if _, err := pool.Exec(ctx, `UPDATE messaging.outbox SET state='LEASED',lease_owner='worker',
	 lease_until=$2 WHERE event_id=$1`, message.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("scoped event dispatch state blocked: %v", err)
	}
	other := message
	other.ID = "00000000-0000-4000-8000-000000000075"
	other.AggregateID = "00000000-0000-4000-8000-000000000076"
	other.Producer = "identity"
	other.EventType = "planext4u.identity.profile.updated.v1"
	other.AggregateType = "customer"
	other.Data = map[string]any{"customer_id": "customer-private"}
	if err := insert(other, "IN", "customer-private"); err == nil {
		t.Fatal("unrelated producer accepted notification subject metadata")
	}
	if err := store.Enqueue(ctx, other, now); err != nil {
		t.Fatalf("ordinary producer compatibility failed: %v", err)
	}
	otherCountry := message
	otherCountry.ID = "00000000-0000-4000-8000-000000000077"
	otherCountry.AggregateID = "00000000-0000-4000-8000-000000000078"
	otherCountry.Country = "LK"
	otherCountry.Data = map[string]any{"delivery_id": otherCountry.AggregateID, "channel": "PUSH"}
	if err := insert(otherCountry, "LK", "customer-private"); err != nil {
		t.Fatalf("independent country event rejected: %v", err)
	}
	otherTenant := message
	otherTenant.ID = "00000000-0000-4000-8000-000000000079"
	otherTenant.AggregateID = "00000000-0000-4000-8000-000000000080"
	otherTenant.TenantID = "00000000-0000-4000-8000-000000000099"
	otherTenant.Data = map[string]any{"delivery_id": otherTenant.AggregateID, "channel": "PUSH"}
	if err := insert(otherTenant, "IN", "customer-private"); err != nil {
		t.Fatalf("independent tenant event rejected: %v", err)
	}
	var scopedCount, allCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messaging.outbox
	 WHERE tenant_id=$1 AND privacy_country='IN' AND privacy_subject_id='customer-private'`, message.TenantID).
		Scan(&scopedCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messaging.outbox`).Scan(&allCount); err != nil {
		t.Fatal(err)
	}
	if scopedCount != 2 || allCount != 6 {
		t.Fatalf("subject lookup=%d all=%d; legacy/other producer must stay unclassified", scopedCount, allCount)
	}
}
