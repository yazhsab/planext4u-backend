package media

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) Tombstone(ctx context.Context, tenant, country, owner, id string) (Asset, error) {
	if uuid.Validate(tenant) != nil || uuid.Validate(id) != nil || !validCountry(country) || !safeID(owner) {
		return Asset{}, ErrNotFound
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Asset{}, fmt.Errorf("begin media tombstone: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	asset, err := scanPostgresAsset(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,country,owner_id,object_key,purpose,content_type,size_bytes,checksum_sha256,lifecycle_state,created_at,upload_expires_at,ready_at,rejected_code,alt_text,presentation_width,presentation_height,version FROM media.assets WHERE id=$1 AND tenant_id=$2 AND country=$3 AND owner_id=$4 FOR UPDATE`, id, tenant, country, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, fmt.Errorf("lock media deletion: %w", err)
	}
	var retained bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media.identity_evidence_claims WHERE asset_id=$1) OR supply.private_identity_evidence_referenced($2,$3,$4,$1) OR fulfillment.private_identity_evidence_referenced($2,$3,$4,$1)`, id, tenant, country, owner).Scan(&retained); err != nil {
		return Asset{}, fmt.Errorf("read media retention: %w", err)
	}
	if retained {
		return Asset{}, ErrRetained
	}
	if asset.State != StateDeleted {
		if _, err := tx.Exec(ctx, `UPDATE media.assets SET status='DELETED',lifecycle_state='DELETED',version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
			return Asset{}, fmt.Errorf("persist media tombstone: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE media.upload_sessions SET completed_at=coalesce(completed_at,now()) WHERE asset_id=$1`, id); err != nil {
			return Asset{}, err
		}
		asset.State = StateDeleted
		asset.Version++
	}
	if err := tx.Commit(ctx); err != nil {
		return Asset{}, fmt.Errorf("commit media tombstone: %w", err)
	}
	return asset, nil
}
