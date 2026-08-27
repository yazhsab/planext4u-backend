package messaging

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBEOutbox001CrashLeaseRetryAndDeadLetter(t *testing.T) {
	t.Parallel()
	service, store, publisher, auditor, now := fixture(t, 2)
	message := syntheticMessage("00000000-0000-4000-8000-000000000001", 1)
	if err := service.Enqueue(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	claimed, _ := store.Claim(context.Background(), "crashed-worker", *now, time.Minute, 1)
	if len(claimed) != 1 {
		t.Fatal("message not claimed before crash")
	}
	if count, _ := service.Dispatch(context.Background(), "worker-2", 10); count != 0 {
		t.Fatalf("active lease published %d messages", count)
	}
	*now = now.Add(61 * time.Second)
	publisher.err = errors.New("synthetic broker outage")
	if count, err := service.Dispatch(context.Background(), "worker-2", 10); err != nil || count != 0 {
		t.Fatalf("first failure = %d, %v", count, err)
	}
	*now = now.Add(2 * time.Second)
	if _, err := service.Dispatch(context.Background(), "worker-2", 10); err != nil {
		t.Fatal(err)
	}
	dead, _ := service.DeadLetters(context.Background(), replayPrincipal(*now), 10)
	if len(dead) != 1 || dead[0].State != OutboxDead || dead[0].LastErrorCode != "PUBLISH_FAILED" {
		t.Fatalf("dead letters = %#v", dead)
	}
	principal := replayPrincipal(*now)
	if err := service.Replay(context.Background(), principal, message.ID, "Broker incident recovered", "corr-synthetic-replay"); err != nil {
		t.Fatal(err)
	}
	if len(auditor.values) != 1 || auditor.values[0].MessageID != message.ID {
		t.Fatalf("replay audit = %#v", auditor.values)
	}
	publisher.err = nil
	if count, err := service.Dispatch(context.Background(), "worker-3", 10); err != nil || count != 1 {
		t.Fatalf("replay dispatch = %d, %v", count, err)
	}
}

func TestBEOutbox001InboxDuplicateOutOfOrderAndCrashRecovery(t *testing.T) {
	t.Parallel()
	service, _, _, _, _ := fixture(t, 3)
	calls := 0
	handler := func(_ context.Context, _ Message) error { calls++; return nil }
	first := syntheticMessage("00000000-0000-4000-8000-000000000001", 1)
	if outcome, err := service.Consume(context.Background(), "projection-worker", first, handler); err != nil || outcome != ConsumeProcessed {
		t.Fatalf("first = %s, %v", outcome, err)
	}
	if outcome, err := service.Consume(context.Background(), "projection-worker", first, handler); err != nil || outcome != ConsumeDuplicate || calls != 1 {
		t.Fatalf("duplicate = %s, %v calls=%d", outcome, err, calls)
	}
	third := syntheticMessage("00000000-0000-4000-8000-000000000003", 3)
	if outcome, err := service.Consume(context.Background(), "projection-worker", third, handler); !errors.Is(err, ErrOutOfOrder) || outcome != ConsumeOutOfOrder {
		t.Fatalf("out of order = %s, %v", outcome, err)
	}
	second := syntheticMessage("00000000-0000-4000-8000-000000000002", 2)
	failing := func(_ context.Context, _ Message) error { calls++; return errors.New("synthetic handler crash") }
	if _, err := service.Consume(context.Background(), "projection-worker", second, failing); err == nil {
		t.Fatal("expected handler failure")
	}
	if outcome, err := service.Consume(context.Background(), "projection-worker", second, handler); err != nil || outcome != ConsumeProcessed {
		t.Fatalf("crash recovery = %s, %v", outcome, err)
	}
	if outcome, err := service.Consume(context.Background(), "projection-worker", third, handler); err != nil || outcome != ConsumeProcessed {
		t.Fatalf("third = %s, %v", outcome, err)
	}
}

func TestBEOutbox001ReplayRequiresCapabilityFreshAuthAndTenant(t *testing.T) {
	t.Parallel()
	service, store, publisher, _, now := fixture(t, 1)
	message := syntheticMessage("00000000-0000-4000-8000-000000000001", 1)
	_ = service.Enqueue(context.Background(), message)
	publisher.err = errors.New("outage")
	_, _ = service.Dispatch(context.Background(), "worker", 1)
	principal := replayPrincipal(*now)
	principal.AuthenticatedAt = now.Add(-10 * time.Minute)
	if err := service.Replay(context.Background(), principal, message.ID, "Approved incident replay", "corr-synthetic-replay"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale auth = %v", err)
	}
	principal = replayPrincipal(*now)
	principal.TenantID = "00000000-0000-4000-8000-000000000099"
	if err := service.Replay(context.Background(), principal, message.ID, "Approved incident replay", "corr-synthetic-replay"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant = %v", err)
	}
	dead, _ := store.DeadLetters(context.Background(), "00000000-0000-4000-8000-000000000010", 10)
	if len(dead) != 1 {
		t.Fatalf("original DLQ altered = %#v", dead)
	}
}

func TestBEOutbox001AtomicMutationRollbackDoesNotPublish(t *testing.T) {
	t.Parallel()
	service, store, _, _, now := fixture(t, 3)
	unit := NewMemoryUnitOfWork(store, func() time.Time { return *now })
	mutationErr := errors.New("synthetic mutation rollback")
	if err := service.EnqueueAtomic(context.Background(), unit, syntheticMessage("00000000-0000-4000-8000-000000000001", 1), func(context.Context) error {
		return mutationErr
	}); !errors.Is(err, mutationErr) {
		t.Fatalf("rollback error = %v", err)
	}
	claimed, _ := store.Claim(context.Background(), "worker", *now, time.Minute, 10)
	if len(claimed) != 0 {
		t.Fatalf("rolled back message was visible: %#v", claimed)
	}
	if err := service.EnqueueAtomic(context.Background(), unit, syntheticMessage("00000000-0000-4000-8000-000000000002", 1), func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claimed, _ = store.Claim(context.Background(), "worker", *now, time.Minute, 10)
	if len(claimed) != 1 {
		t.Fatalf("committed message count = %d", len(claimed))
	}
}

func fixture(t *testing.T, maxAttempts int) (*Service, *MemoryStore, *fakePublisher, *fakeAuditor, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	store, publisher, auditor := NewMemoryStore(), &fakePublisher{}, &fakeAuditor{}
	service, err := NewService(store, store, publisher, auditor, time.Minute, maxAttempts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service, store, publisher, auditor, &now
}

func syntheticMessage(id string, sequence int64) Message {
	return Message{ID: id, TenantID: "00000000-0000-4000-8000-000000000010", Country: "IN", Producer: "identity",
		EventType: "planext4u.identity.profile.updated.v1", SchemaVersion: 1, AggregateType: "customer",
		AggregateID: "00000000-0000-4000-8000-000000000020", AggregateVersion: sequence,
		CorrelationID: "corr-synthetic-message", CausationID: "command-synthetic-message",
		Traceparent: "00-00000000000000000000000000000001-0000000000000001-01", Classification: "CONFIDENTIAL",
		OccurredAt: time.Date(2026, 8, 27, 9, 59, 0, 0, time.UTC), Data: map[string]any{"customer_id": "customer-synthetic"}}
}

func replayPrincipal(now time.Time) ReplayPrincipal {
	return ReplayPrincipal{TenantID: "00000000-0000-4000-8000-000000000010", Country: "IN", SubjectID: "admin-synthetic", AuthenticatedAt: now,
		Capabilities: map[string]bool{"messaging.dlq.read": true, "messaging.dlq.replay": true}}
}

type fakePublisher struct {
	err      error
	messages []Message
}

func (publisher *fakePublisher) Publish(_ context.Context, message Message) error {
	if publisher.err == nil {
		publisher.messages = append(publisher.messages, cloneMessage(message))
	}
	return publisher.err
}

type fakeAuditor struct{ values []ReplayAudit }

func (auditor *fakeAuditor) RecordReplay(_ context.Context, value ReplayAudit) error {
	auditor.values = append(auditor.values, value)
	return nil
}
