package configcms

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound         = errors.New("configuration not found")
	ErrRevisionConflict = errors.New("configuration revision conflict")
	ErrInvalidSnapshot  = errors.New("invalid configuration snapshot")
)

type Repository interface {
	Get(context.Context, string, string) (Snapshot, error)
}

type WritableRepository interface {
	Repository
	Publish(context.Context, Snapshot, int64) error
}

type AuditSink interface {
	Append(context.Context, AuditRecord) error
}

type MemoryRepository struct {
	mu        sync.RWMutex
	snapshots map[string]Snapshot
}

func NewMemoryRepository(initial ...Snapshot) (*MemoryRepository, error) {
	repository := &MemoryRepository{snapshots: make(map[string]Snapshot, len(initial))}
	for _, snapshot := range initial {
		if err := validateSnapshot(snapshot); err != nil {
			return nil, err
		}
		repository.snapshots[snapshotKey(snapshot.TenantID, snapshot.Country)] = cloneSnapshot(snapshot)
	}
	return repository, nil
}

func (repository *MemoryRepository) Get(_ context.Context, tenantID, country string) (Snapshot, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	snapshot, found := repository.snapshots[snapshotKey(tenantID, country)]
	if !found {
		return Snapshot{}, ErrNotFound
	}
	return cloneSnapshot(snapshot), nil
}

func (repository *MemoryRepository) Publish(_ context.Context, snapshot Snapshot, expectedRevision int64) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := snapshotKey(snapshot.TenantID, snapshot.Country)
	current, found := repository.snapshots[key]
	if (!found && expectedRevision != 0) || (found && current.Revision != expectedRevision) || snapshot.Revision != expectedRevision+1 {
		return ErrRevisionConflict
	}
	repository.snapshots[key] = cloneSnapshot(snapshot)
	return nil
}

type cacheEntry struct {
	snapshot  Snapshot
	expiresAt time.Time
}

type CachedRepository struct {
	source Repository
	ttl    time.Duration
	clock  func() time.Time
	mu     sync.RWMutex
	values map[string]cacheEntry
}

func NewCachedRepository(source Repository, ttl time.Duration, clock func() time.Time) (*CachedRepository, error) {
	if source == nil || ttl <= 0 || ttl > time.Hour || clock == nil {
		return nil, fmt.Errorf("invalid configuration cache")
	}
	return &CachedRepository{source: source, ttl: ttl, clock: clock, values: map[string]cacheEntry{}}, nil
}

func (repository *CachedRepository) Get(ctx context.Context, tenantID, country string) (Snapshot, error) {
	key := snapshotKey(tenantID, country)
	now := repository.clock().UTC()
	repository.mu.RLock()
	cached, found := repository.values[key]
	repository.mu.RUnlock()
	if found && cached.expiresAt.After(now) {
		return cloneSnapshot(cached.snapshot), nil
	}
	snapshot, err := repository.source.Get(ctx, tenantID, country)
	if err != nil {
		return Snapshot{}, err
	}
	repository.mu.Lock()
	repository.values[key] = cacheEntry{snapshot: cloneSnapshot(snapshot), expiresAt: now.Add(repository.ttl)}
	repository.mu.Unlock()
	return cloneSnapshot(snapshot), nil
}

func (repository *CachedRepository) Invalidate(tenantID, country string) {
	repository.mu.Lock()
	delete(repository.values, snapshotKey(tenantID, country))
	repository.mu.Unlock()
}

type MemoryAuditSink struct {
	mu      sync.Mutex
	records []AuditRecord
}

func (sink *MemoryAuditSink) Append(_ context.Context, record AuditRecord) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.records = append(sink.records, record)
	return nil
}

func (sink *MemoryAuditSink) Records() []AuditRecord {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]AuditRecord(nil), sink.records...)
}

func validateSnapshot(snapshot Snapshot) error {
	if !safeID(snapshot.TenantID) || len(snapshot.Country) != 2 || snapshot.Revision < 1 || snapshot.PublishedAt.IsZero() ||
		len(snapshot.MinimumVersions) != 2 || len(snapshot.LatestVersions) != 2 || len(snapshot.SupportedLocales) == 0 ||
		!containsString(snapshot.SupportedLocales, snapshot.DefaultLocale) || len(snapshot.Flags) > 256 || len(snapshot.HomeSections) > 64 {
		return ErrInvalidSnapshot
	}
	for _, platform := range []Platform{PlatformAndroid, PlatformIOS} {
		minimum, minOK := snapshot.MinimumVersions[platform]
		latest, latestOK := snapshot.LatestVersions[platform]
		if !minOK || !latestOK || compareVersion(minimum, latest) > 0 {
			return ErrInvalidSnapshot
		}
	}
	seen := map[string]struct{}{}
	for _, locale := range snapshot.SupportedLocales {
		if locale != "en" && locale != "ta" {
			return ErrInvalidSnapshot
		}
	}
	for _, policy := range snapshot.ConsentPolicies {
		if !safeID(policy.Purpose) || !safeID(policy.PolicyVersion) {
			return ErrInvalidSnapshot
		}
	}
	for _, section := range snapshot.HomeSections {
		if !safeID(section.ID) || !safeID(section.Kind) || !safeID(section.TitleKey) || section.Priority < 0 {
			return ErrInvalidSnapshot
		}
		if _, duplicate := seen[section.ID]; duplicate {
			return ErrInvalidSnapshot
		}
		seen[section.ID] = struct{}{}
	}
	if window := snapshot.MaintenanceWindow; window != nil {
		if window.StartsAt.IsZero() || !window.EndsAt.After(window.StartsAt) || strings.TrimSpace(window.Message) == "" || len(window.Message) > 240 {
			return ErrInvalidSnapshot
		}
	}
	return nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	result := snapshot
	result.MinimumVersions = cloneMap(snapshot.MinimumVersions)
	result.LatestVersions = cloneMap(snapshot.LatestVersions)
	result.SupportedLocales = append([]string(nil), snapshot.SupportedLocales...)
	result.ConsentPolicies = append([]ConsentPolicy(nil), snapshot.ConsentPolicies...)
	result.Flags = cloneMap(snapshot.Flags)
	result.HomeSections = append([]HomeSection(nil), snapshot.HomeSections...)
	if snapshot.MaintenanceWindow != nil {
		window := *snapshot.MaintenanceWindow
		result.MaintenanceWindow = &window
	}
	return result
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func snapshotKey(tenantID, country string) string { return tenantID + "\x00" + country }

func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func containsString(values []string, candidate string) bool {
	return sort.SearchStrings(appendSorted(values), candidate) < len(values) && containsUnsorted(values, candidate)
}

func appendSorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func containsUnsorted(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
