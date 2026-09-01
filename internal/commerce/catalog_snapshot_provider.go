package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/catalog"
)

// CatalogSnapshotProvider resolves only published catalogue documents. The
// final oversell decision remains with inventory reservations.
type CatalogSnapshotProvider struct{ pool *pgxpool.Pool }

func NewCatalogSnapshotProvider(pool *pgxpool.Pool) (*CatalogSnapshotProvider, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &CatalogSnapshotProvider{pool: pool}, nil
}

func (provider *CatalogSnapshotProvider) Ready(ctx context.Context) error {
	var ready bool
	if err := provider.pool.QueryRow(ctx, `SELECT to_regclass('catalog.items') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check commerce catalog projection: %w", err)
	}
	if !ready {
		return errors.New("catalog projection is unavailable")
	}
	return nil
}

func (provider *CatalogSnapshotProvider) Resolve(ctx context.Context, scope Scope, variantID string) (VariantSnapshot, error) {
	if ctx == nil || !postgresCartScope(scope) || !postgresUUID(variantID) {
		return VariantSnapshot{}, ErrInvalidRequest
	}
	rows, err := provider.pool.Query(ctx, `
		SELECT id::text,title FROM catalog.items
		WHERE tenant_id=$1 AND country=$2 AND kind='ITEM' AND status='PUBLISHED'
		ORDER BY priority,id`, scope.TenantID, scope.Country)
	if err != nil {
		return VariantSnapshot{}, fmt.Errorf("load published commerce catalog: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var itemID string
		var payload []byte
		if err := rows.Scan(&itemID, &payload); err != nil {
			return VariantSnapshot{}, fmt.Errorf("scan published commerce item: %w", err)
		}
		var item catalog.Item
		if json.Unmarshal(payload, &item) != nil || item.ID != itemID {
			return VariantSnapshot{}, ErrVariantUnavailable
		}
		for _, variant := range item.Variants {
			if variant.ID != variantID {
				continue
			}
			if !postgresUUID(item.VendorID) {
				return VariantSnapshot{}, ErrVariantUnavailable
			}
			media := item.MediaRef
			if media == "" && len(item.MediaRefs) > 0 {
				media = item.MediaRefs[0]
			}
			return VariantSnapshot{
				VariantID: variant.ID, ItemID: item.ID, VendorID: item.VendorID,
				ItemName: item.Name, VariantName: variant.Label, MediaRef: media,
				UnitPrice: Money{AmountMinor: variant.Price.AmountMinor, Currency: variant.Price.Currency},
				Available: item.Available && variant.Available, Stock: variant.StockQuantity, MaxPerOrder: variant.MaxPerOrder,
			}, nil
		}
	}
	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return VariantSnapshot{}, fmt.Errorf("iterate published commerce catalog: %w", err)
	}
	return VariantSnapshot{}, ErrVariantNotFound
}
