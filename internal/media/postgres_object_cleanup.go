package media

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DeleteTombstonedObject serializes normal S3 deletion with future version
// cleanup on the immutable asset row. A completed exact-key version purge is
// never followed by another unversioned DeleteObject/delete marker. This does
// not itself enumerate historical versions or prove account erasure.
func (repository *PostgresRepository) DeleteTombstonedObject(
	ctx context.Context, tenant, country, owner, id string, deleteObject func(context.Context, string) error,
) error {
	if uuid.Validate(tenant) != nil || uuid.Validate(id) != nil || !validCountry(country) || !safeID(owner) || deleteObject == nil {
		return ErrNotFound
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin media object cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the purge-state row first. Its UPDATE trigger takes the asset lock
	// in the same order when a future cleaner records verified zero versions.
	var purgedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT purged_at FROM media.lock_object_purge_state($1,$2,$3,$4)`,
		id, tenant, country, owner).Scan(&purgedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDependency
		}
		return fmt.Errorf("lock media object purge state: %w", err)
	}
	asset, err := scanPostgresAsset(tx.QueryRow(ctx, `
		SELECT id::text,tenant_id::text,country,owner_id,object_key,purpose,
		       content_type,size_bytes,checksum_sha256,lifecycle_state,created_at,
		       upload_expires_at,ready_at,rejected_code,alt_text,
		       presentation_width,presentation_height,version
		FROM media.assets
		WHERE id=$1 AND tenant_id=$2 AND country=$3 AND owner_id=$4
		FOR UPDATE`, id, tenant, country, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock media object cleanup: %w", err)
	}
	if asset.State != StateDeleted || !validPersistentAsset(asset) {
		return ErrConflict
	}
	if purgedAt == nil {
		if err := deleteObject(ctx, asset.ObjectKey); err != nil {
			return fmt.Errorf("delete tombstoned media object: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit media object cleanup: %w", err)
	}
	return nil
}

var _ TombstonedObjectDeleter = (*PostgresRepository)(nil)
