package messaging

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrInvalidRequest = errors.New("invalid messaging request")
	ErrNotFound       = errors.New("message not found")
	ErrConflict       = errors.New("message state conflict")
	ErrBusy           = errors.New("message processing busy")
	ErrOutOfOrder     = errors.New("message out of order")
	ErrForbidden      = errors.New("messaging operation forbidden")
)

type OutboxStore interface {
	Enqueue(context.Context, Message, time.Time) error
	Claim(context.Context, string, time.Time, time.Duration, int) ([]OutboxRecord, error)
	MarkPublished(context.Context, string, string, time.Time) error
	MarkFailed(context.Context, string, string, time.Time, time.Time, string, int) (OutboxState, error)
	DeadLetters(context.Context, string, int) ([]OutboxRecord, error)
	Replay(context.Context, string, string, time.Time) error
}

type InboxStore interface {
	Begin(context.Context, string, Message, time.Time, time.Duration) (ConsumeOutcome, error)
	Complete(context.Context, string, Message, time.Time) error
	Release(context.Context, string, string) error
}

type MemoryStore struct {
	mu       sync.Mutex
	outbox   map[string]OutboxRecord
	inbox    map[string]inboxRecord
	sequence map[string]int64
}

type inboxRecord struct {
	state      string
	leaseUntil time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{outbox: map[string]OutboxRecord{}, inbox: map[string]inboxRecord{}, sequence: map[string]int64{}}
}

func (store *MemoryStore) Enqueue(_ context.Context, message Message, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.outbox[message.ID]; exists {
		return ErrConflict
	}
	store.outbox[message.ID] = OutboxRecord{Message: cloneMessage(message), State: OutboxPending, NextAttemptAt: now.UTC(), CreatedAt: now.UTC()}
	return nil
}

func (store *MemoryStore) Claim(_ context.Context, worker string, now time.Time, lease time.Duration, limit int) ([]OutboxRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	candidates := make([]OutboxRecord, 0)
	for _, record := range store.outbox {
		eligible := record.State == OutboxPending || (record.State == OutboxLeased && !record.LeaseUntil.After(now))
		if eligible && !record.NextAttemptAt.After(now) {
			candidates = append(candidates, record)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].NextAttemptAt.Equal(candidates[j].NextAttemptAt) {
			return candidates[i].Message.ID < candidates[j].Message.ID
		}
		return candidates[i].NextAttemptAt.Before(candidates[j].NextAttemptAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for index := range candidates {
		record := store.outbox[candidates[index].Message.ID]
		record.State, record.LeaseOwner, record.LeaseUntil = OutboxLeased, worker, now.Add(lease)
		store.outbox[record.Message.ID] = record
		candidates[index] = cloneRecord(record)
	}
	return candidates, nil
}

func (store *MemoryStore) MarkPublished(_ context.Context, id, worker string, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.outbox[id]
	if !exists {
		return ErrNotFound
	}
	if record.State != OutboxLeased || record.LeaseOwner != worker || !record.LeaseUntil.After(now) {
		return ErrConflict
	}
	record.State, record.LeaseOwner, record.LeaseUntil = OutboxPublished, "", time.Time{}
	record.PublishedAt = timePointer(now.UTC())
	store.outbox[id] = record
	return nil
}

func (store *MemoryStore) MarkFailed(_ context.Context, id, worker string, now, next time.Time, code string, maxAttempts int) (OutboxState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.outbox[id]
	if !exists {
		return "", ErrNotFound
	}
	if record.State != OutboxLeased || record.LeaseOwner != worker || !record.LeaseUntil.After(now) {
		return "", ErrConflict
	}
	record.Attempts++
	record.LastErrorCode, record.LeaseOwner, record.LeaseUntil = code, "", time.Time{}
	if record.Attempts >= maxAttempts {
		record.State, record.DeadAt = OutboxDead, timePointer(now.UTC())
	} else {
		record.State, record.NextAttemptAt = OutboxPending, next.UTC()
	}
	store.outbox[id] = record
	return record.State, nil
}

func (store *MemoryStore) DeadLetters(_ context.Context, tenantID string, limit int) ([]OutboxRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]OutboxRecord, 0)
	for _, record := range store.outbox {
		if record.State == OutboxDead && record.Message.TenantID == tenantID {
			result = append(result, cloneRecord(record))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Message.ID < result[j].Message.ID })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (store *MemoryStore) Replay(_ context.Context, tenantID, id string, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.outbox[id]
	if !exists || record.Message.TenantID != tenantID {
		return ErrNotFound
	}
	if record.State != OutboxDead {
		return ErrConflict
	}
	record.State, record.Attempts, record.LastErrorCode, record.DeadAt, record.NextAttemptAt = OutboxPending, 0, "", nil, now.UTC()
	store.outbox[id] = record
	return nil
}

func (store *MemoryStore) Begin(_ context.Context, consumer string, message Message, now time.Time, lease time.Duration) (ConsumeOutcome, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := consumer + "\x00" + message.ID
	if record, exists := store.inbox[key]; exists {
		if record.state == "DONE" {
			return ConsumeDuplicate, nil
		}
		if record.leaseUntil.After(now) {
			return "", ErrBusy
		}
	}
	sequenceKey := consumer + "\x00" + message.TenantID + "\x00" + message.AggregateType + "\x00" + message.AggregateID
	last := store.sequence[sequenceKey]
	if message.AggregateVersion <= last {
		return ConsumeDuplicate, nil
	}
	if message.AggregateVersion != last+1 {
		return ConsumeOutOfOrder, ErrOutOfOrder
	}
	store.inbox[key] = inboxRecord{state: "PROCESSING", leaseUntil: now.Add(lease)}
	return ConsumeProcessed, nil
}

func (store *MemoryStore) Complete(_ context.Context, consumer string, message Message, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := consumer + "\x00" + message.ID
	record, exists := store.inbox[key]
	if !exists || record.state != "PROCESSING" || !record.leaseUntil.After(now) {
		return ErrConflict
	}
	record.state, record.leaseUntil = "DONE", time.Time{}
	store.inbox[key] = record
	sequenceKey := consumer + "\x00" + message.TenantID + "\x00" + message.AggregateType + "\x00" + message.AggregateID
	store.sequence[sequenceKey] = message.AggregateVersion
	return nil
}

func (store *MemoryStore) Release(_ context.Context, consumer, messageID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := consumer + "\x00" + messageID
	if _, exists := store.inbox[key]; !exists {
		return ErrNotFound
	}
	delete(store.inbox, key)
	return nil
}

func cloneMessage(message Message) Message {
	message.Data = cloneMap(message.Data)
	return message
}

func cloneRecord(record OutboxRecord) OutboxRecord {
	record.Message = cloneMessage(record.Message)
	if record.PublishedAt != nil {
		record.PublishedAt = timePointer(*record.PublishedAt)
	}
	if record.DeadAt != nil {
		record.DeadAt = timePointer(*record.DeadAt)
	}
	return record
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		switch typed := item.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []any:
			result[key] = cloneSlice(typed)
		default:
			result[key] = item
		}
	}
	return result
}

func cloneSlice(values []any) []any {
	result := make([]any, len(values))
	for index, item := range values {
		switch typed := item.(type) {
		case map[string]any:
			result[index] = cloneMap(typed)
		case []any:
			result[index] = cloneSlice(typed)
		default:
			result[index] = item
		}
	}
	return result
}

func timePointer(value time.Time) *time.Time { return &value }
