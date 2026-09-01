package media

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresRepository{pool: pool}, nil
}

func (repository *PostgresRepository) Ready(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT to_regclass('media.assets') IS NOT NULL
		   AND to_regclass('media.upload_sessions') IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema = 'media' AND table_name = 'assets'
		         AND column_name = 'lifecycle_state'
		   )`).Scan(&ready); err != nil {
		return fmt.Errorf("check media schema readiness: %w", err)
	}
	if !ready {
		return errors.New("media schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) Create(ctx context.Context, asset Asset) error {
	if !validPersistentAsset(asset) || asset.Version != 1 || asset.State != StatePendingUpload {
		return ErrInvalidRequest
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin media create: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	_, err = transaction.Exec(ctx, `
		INSERT INTO media.assets
			(id, tenant_id, country, owner_id, object_key, content_type, size_bytes,
			 checksum_sha256, status, classification, created_at, updated_at, purpose,
			 lifecycle_state, upload_expires_at, ready_at, rejected_code, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12, $13, $14, $15, $16, $17)`,
		asset.ID, asset.TenantID, asset.Country, asset.OwnerID, asset.ObjectKey, asset.ContentType,
		asset.SizeBytes, asset.SHA256, legacyStatus(asset.State), classification(asset.Purpose),
		asset.CreatedAt, asset.Purpose, asset.State, asset.UploadExpiresAt, asset.ReadyAt,
		nullableString(asset.RejectedCode), asset.Version)
	if err != nil {
		if mediaConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("insert media asset: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO media.upload_sessions (id, asset_id, expires_at, created_at)
		VALUES ($1, $1, $2, $3)`, asset.ID, asset.UploadExpiresAt, asset.CreatedAt); err != nil {
		if mediaConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("insert media upload session: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit media asset: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) Get(ctx context.Context, tenantID, ownerID, assetID string) (Asset, error) {
	if _, err := uuid.Parse(tenantID); err != nil || !safeID(ownerID) {
		return Asset{}, ErrNotFound
	}
	if _, err := uuid.Parse(assetID); err != nil {
		return Asset{}, ErrNotFound
	}
	value, err := scanPostgresAsset(repository.pool.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, country, owner_id, object_key, purpose,
		       content_type, size_bytes, checksum_sha256, lifecycle_state, created_at,
		       upload_expires_at, ready_at, rejected_code, version
		FROM media.assets
		WHERE id = $1 AND tenant_id = $2 AND owner_id = $3 AND lifecycle_state <> 'DELETED'`,
		assetID, tenantID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, fmt.Errorf("read media asset: %w", err)
	}
	return value, nil
}

func (repository *PostgresRepository) Update(ctx context.Context, asset Asset, expectedVersion int64) error {
	if !validPersistentAsset(asset) || expectedVersion < 1 || asset.Version != expectedVersion+1 {
		return ErrInvalidRequest
	}
	terminal := asset.State == StateReady || asset.State == StateRejected || asset.State == StateExpired || asset.State == StateDeleted
	var completedAt any
	if asset.ReadyAt != nil {
		completedAt = *asset.ReadyAt
	} else if asset.State == StateExpired {
		completedAt = asset.UploadExpiresAt
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin media update: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `
		UPDATE media.assets
		SET status = $1, lifecycle_state = $2, ready_at = $3, rejected_code = $4,
		    version = $5, updated_at = now()
		WHERE id = $6 AND tenant_id = $7 AND owner_id = $8 AND version = $9`,
		legacyStatus(asset.State), asset.State, asset.ReadyAt, nullableString(asset.RejectedCode),
		asset.Version, asset.ID, asset.TenantID, asset.OwnerID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update media asset: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if terminal {
		if _, err := transaction.Exec(ctx, `
			UPDATE media.upload_sessions SET completed_at = COALESCE(completed_at, $1, now())
			WHERE asset_id = $2`, completedAt, asset.ID); err != nil {
			return fmt.Errorf("complete media upload session: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit media update: %w", err)
	}
	return nil
}

type mediaRow interface {
	Scan(...any) error
}

func scanPostgresAsset(row mediaRow) (Asset, error) {
	var value Asset
	var rejectedCode *string
	if err := row.Scan(&value.ID, &value.TenantID, &value.Country, &value.OwnerID,
		&value.ObjectKey, &value.Purpose, &value.ContentType, &value.SizeBytes, &value.SHA256,
		&value.State, &value.CreatedAt, &value.UploadExpiresAt, &value.ReadyAt, &rejectedCode,
		&value.Version); err != nil {
		return Asset{}, err
	}
	if rejectedCode != nil {
		value.RejectedCode = *rejectedCode
	}
	return value, nil
}

func validPersistentAsset(asset Asset) bool {
	_, tenantErr := uuid.Parse(asset.TenantID)
	_, idErr := uuid.Parse(asset.ID)
	return tenantErr == nil && idErr == nil && validCountry(asset.Country) && safeID(asset.OwnerID) &&
		asset.ObjectKey == objectKey(asset.TenantID, asset.OwnerID, asset.ID) && validPurpose(asset.Purpose) &&
		validMetadata(ObjectMetadata{ContentType: asset.ContentType, SizeBytes: asset.SizeBytes, SHA256: asset.SHA256}, asset.Purpose) &&
		validState(asset.State) && !asset.CreatedAt.IsZero() && !asset.UploadExpiresAt.IsZero() &&
		asset.UploadExpiresAt.After(asset.CreatedAt) && asset.Version >= 1 &&
		(asset.RejectedCode == "" || safeReason(asset.RejectedCode) == asset.RejectedCode) &&
		(asset.State != StateReady || asset.ReadyAt != nil) &&
		(asset.State != StateRejected || asset.RejectedCode != "") &&
		(asset.State != StatePendingUpload || (asset.ReadyAt == nil && asset.RejectedCode == "")) &&
		(asset.State != StatePendingScan || (asset.ReadyAt == nil && asset.RejectedCode == ""))
}

func validState(value State) bool {
	switch value {
	case StatePendingUpload, StatePendingScan, StateReady, StateRejected, StateExpired, StateDeleted:
		return true
	default:
		return false
	}
}

func legacyStatus(value State) string {
	switch value {
	case StateReady:
		return "READY"
	case StateRejected:
		return "QUARANTINED"
	case StateExpired, StateDeleted:
		return "DELETED"
	default:
		return "PENDING"
	}
}

func classification(value Purpose) string {
	switch value {
	case PurposeIdentityDocument:
		return "RESTRICTED"
	case PurposeCompletionProof:
		return "CONFIDENTIAL"
	case PurposeAvatar:
		return "INTERNAL"
	default:
		return "PUBLIC"
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func mediaConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

var _ Repository = (*PostgresRepository)(nil)
