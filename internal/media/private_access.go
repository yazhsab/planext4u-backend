package media

import (
	"context"
	"errors"
	"time"
)

type PrivateSigner interface {
	SignPrivate(context.Context, string, string, time.Time) (string, error)
}
type PrivateAccess struct {
	AssetID   string    `json:"asset_id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TombstonedObjectDeleter holds the owning asset lock while deleting from the
// object store. A future version purge uses that same lock before recording a
// verified empty key, so a normal retry cannot add a later delete marker.
type TombstonedObjectDeleter interface {
	DeleteTombstonedObject(context.Context, string, string, string, string, func(context.Context, string) error) error
}

func (service *Service) GetScoped(ctx context.Context, tenant, country, owner, id string) (Asset, error) {
	asset, err := service.Get(ctx, tenant, owner, id)
	if err != nil {
		return Asset{}, err
	}
	if asset.Country != country {
		return Asset{}, ErrNotFound
	}
	return asset, nil
}
func (service *Service) CompleteScoped(ctx context.Context, tenant, country, owner, id string) (Asset, error) {
	if _, err := service.GetScoped(ctx, tenant, country, owner, id); err != nil {
		return Asset{}, err
	}
	return service.Complete(ctx, tenant, owner, id)
}
func (service *Service) DeleteScoped(ctx context.Context, tenant, country, owner, id string) error {
	asset, err := service.repository.Tombstone(ctx, tenant, country, owner, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRetained) || errors.Is(err, ErrConflict) {
			return err
		}
		return ErrDependency
	}
	// Fence reads and new claims durably before object deletion. A failed object
	// cleanup remains private and the same scoped DELETE safely retries it.
	if guarded, ok := service.repository.(TombstonedObjectDeleter); ok {
		if err := guarded.DeleteTombstonedObject(ctx, tenant, country, owner, id, service.objects.Delete); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRetained) || errors.Is(err, ErrConflict) {
				return err
			}
			return ErrDependency
		}
		return nil
	}
	if err := service.objects.Delete(ctx, asset.ObjectKey); err != nil {
		return ErrDependency
	}
	return nil
}
func (service *Service) PrivateAccess(ctx context.Context, tenant, country, owner, id string) (PrivateAccess, error) {
	asset, err := service.GetScoped(ctx, tenant, country, owner, id)
	if err != nil {
		return PrivateAccess{}, err
	}
	if asset.State != StateReady {
		return PrivateAccess{}, ErrConflict
	}
	signer, ok := service.signer.(PrivateSigner)
	if !ok {
		return PrivateAccess{}, ErrDependency
	}
	expires := service.clock().UTC().Add(2 * time.Minute)
	url, err := signer.SignPrivate(ctx, asset.ObjectKey, asset.ContentType, expires)
	if err != nil {
		return PrivateAccess{}, ErrDependency
	}
	return PrivateAccess{AssetID: id, URL: url, ExpiresAt: expires}, nil
}
