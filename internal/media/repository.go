package media

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidRequest = errors.New("invalid media request")
	ErrNotFound       = errors.New("media asset not found")
	ErrConflict       = errors.New("media state conflict")
	ErrExpired        = errors.New("media upload expired")
	ErrDependency     = errors.New("media dependency unavailable")
	ErrRetained       = errors.New("media evidence retained by owning application")
)

type Repository interface {
	Create(context.Context, Asset, UploadGrantProvenance) error
	Get(context.Context, string, string, string) (Asset, error)
	GetPublic(context.Context, string, string, string) (Asset, error)
	Update(context.Context, Asset, int64) error
	Tombstone(context.Context, string, string, string, string) (Asset, error)
}

const (
	GrantProvenanceUnknown             = "UNKNOWN"
	GrantProvenanceAWSConditionalSigV4 = "AWS_SIGV4_IF_NONE_MATCH"
)

// UploadGrantProvenance is persisted with the session at creation. An unknown
// value is deliberately ineligible for automatic historical-version cleanup.
type UploadGrantProvenance struct {
	Scheme     string
	ValidUntil *time.Time
}

func (repository *MemoryRepository) GetPublic(_ context.Context, tenantID, country, assetID string) (Asset, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	asset, exists := repository.assets[assetID]
	if !exists || asset.TenantID != tenantID || asset.Country != country || asset.Purpose != PurposeCatalogImage || asset.State != StateReady {
		return Asset{}, ErrNotFound
	}
	return cloneAsset(asset), nil
}

type MemoryRepository struct {
	mu     sync.RWMutex
	assets map[string]Asset
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{assets: map[string]Asset{}}
}

func (repository *MemoryRepository) Create(_ context.Context, asset Asset, provenance UploadGrantProvenance) error {
	if !validUploadGrantProvenance(provenance) {
		return ErrInvalidRequest
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.assets[asset.ID]; exists {
		return ErrConflict
	}
	repository.assets[asset.ID] = cloneAsset(asset)
	return nil
}

func (repository *MemoryRepository) Get(_ context.Context, tenantID, ownerID, assetID string) (Asset, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	asset, exists := repository.assets[assetID]
	if !exists || asset.TenantID != tenantID || asset.OwnerID != ownerID || asset.State == StateDeleted {
		return Asset{}, ErrNotFound
	}
	return cloneAsset(asset), nil
}

func (repository *MemoryRepository) Update(_ context.Context, asset Asset, expectedVersion int64) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, exists := repository.assets[asset.ID]
	if !exists || current.TenantID != asset.TenantID || current.OwnerID != asset.OwnerID {
		return ErrNotFound
	}
	if current.Version != expectedVersion {
		return ErrConflict
	}
	repository.assets[asset.ID] = cloneAsset(asset)
	return nil
}

func (repository *MemoryRepository) Tombstone(_ context.Context, tenant, country, owner, id string) (Asset, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	asset, ok := repository.assets[id]
	if !ok || asset.TenantID != tenant || asset.Country != country || asset.OwnerID != owner {
		return Asset{}, ErrNotFound
	}
	if asset.State != StateDeleted {
		asset.State = StateDeleted
		asset.Version++
		repository.assets[id] = cloneAsset(asset)
	}
	return cloneAsset(asset), nil
}

func cloneAsset(asset Asset) Asset {
	if asset.ReadyAt != nil {
		readyAt := *asset.ReadyAt
		asset.ReadyAt = &readyAt
	}
	return asset
}
