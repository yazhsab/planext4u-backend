package audit

import (
	"context"
	"errors"
	"sort"
	"sync"
)

var (
	ErrInvalidRequest = errors.New("invalid audit request")
	ErrForbidden      = errors.New("audit operation forbidden")
	ErrConflict       = errors.New("audit append conflict")
	ErrIntegrity      = errors.New("audit chain integrity failure")
)

type Repository interface {
	Append(context.Context, Entry) error
	Last(context.Context, string) (Entry, bool, error)
	Search(context.Context, SearchFilter) ([]Entry, error)
}

type MemoryRepository struct {
	mu      sync.RWMutex
	entries []Entry
}

func NewMemoryRepository() *MemoryRepository { return &MemoryRepository{} }

func (repository *MemoryRepository) Append(_ context.Context, entry Entry) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, existing := range repository.entries {
		if existing.ID == entry.ID || (existing.TenantID == entry.TenantID && existing.Sequence == entry.Sequence) {
			return ErrConflict
		}
	}
	repository.entries = append(repository.entries, cloneEntry(entry))
	return nil
}

func (repository *MemoryRepository) Last(_ context.Context, tenantID string) (Entry, bool, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	var result Entry
	found := false
	for _, entry := range repository.entries {
		if entry.TenantID == tenantID && (!found || entry.Sequence > result.Sequence) {
			result, found = entry, true
		}
	}
	return cloneEntry(result), found, nil
}

func (repository *MemoryRepository) Search(_ context.Context, filter SearchFilter) ([]Entry, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]Entry, 0)
	for _, entry := range repository.entries {
		if entry.TenantID != filter.TenantID || entry.Sequence <= filter.After ||
			(filter.Country != "" && entry.Country != filter.Country) ||
			(filter.ActorID != "" && entry.Actor.SubjectID != filter.ActorID) ||
			(filter.Action != "" && entry.Action != filter.Action) ||
			(filter.TargetID != "" && entry.Target.ID != filter.TargetID) ||
			(!filter.From.IsZero() && entry.OccurredAt.Before(filter.From)) ||
			(!filter.To.IsZero() && entry.OccurredAt.After(filter.To)) {
			continue
		}
		result = append(result, cloneEntry(entry))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result, nil
}

func cloneEntry(entry Entry) Entry {
	entry.Before = cloneMap(entry.Before)
	entry.After = cloneMap(entry.After)
	return entry
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		switch typed := item.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []any:
			result[key] = append([]any(nil), typed...)
		default:
			result[key] = item
		}
	}
	return result
}
