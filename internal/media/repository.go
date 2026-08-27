package media

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrInvalidRequest = errors.New("invalid media request")
	ErrNotFound       = errors.New("media asset not found")
	ErrConflict       = errors.New("media state conflict")
	ErrExpired        = errors.New("media upload expired")
	ErrDependency     = errors.New("media dependency unavailable")
)

type Repository interface {
	Create(context.Context, Asset) error
	Get(context.Context, string, string, string) (Asset, error)
	Update(context.Context, Asset, int64) error
}

type MemoryRepository struct {
	mu     sync.RWMutex
	assets map[string]Asset
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{assets: map[string]Asset{}}
}

func (repository *MemoryRepository) Create(_ context.Context, asset Asset) error {
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

func cloneAsset(asset Asset) Asset {
	if asset.ReadyAt != nil {
		readyAt := *asset.ReadyAt
		asset.ReadyAt = &readyAt
	}
	return asset
}
