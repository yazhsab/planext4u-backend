//go:build integration

package messaging

import (
	"context"
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
	dead, err := store.DeadLetters(ctx, message.TenantID, 10)
	if err != nil || len(dead) != 1 || dead[0].Attempts != 1 {
		t.Fatalf("dead=%#v err=%v", dead, err)
	}
	other, err := store.DeadLetters(ctx, "23bf7434-3643-49df-9928-c9169011f69d", 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-tenant dead=%#v err=%v", other, err)
	}
	if err := store.Replay(ctx, message.TenantID, message.ID, now.Add(2*time.Second)); err != nil {
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
