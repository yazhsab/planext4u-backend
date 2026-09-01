package configcms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

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
		return nil, ErrInvalidRepository
	}
	return &PostgresRepository{pool: pool}, nil
}

func (repository *PostgresRepository) Ready(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT to_regclass('configuration.snapshots') IS NOT NULL
		   AND to_regclass('configuration.publish_history') IS NOT NULL
		   AND to_regclass('configuration.page_drafts') IS NOT NULL
		   AND to_regclass('configuration.workspace_drafts') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check configuration schema readiness: %w", err)
	}
	if !ready {
		return errors.New("configuration schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) ListPageDrafts(ctx context.Context, tenantID, country string) ([]PageDraft, error) {
	if _, err := uuid.Parse(tenantID); err != nil || len(country) != 2 {
		return nil, ErrInvalidSnapshot
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT page_id, revision, document, updated_by, updated_at
		FROM configuration.page_drafts
		WHERE tenant_id = $1 AND country = $2
		ORDER BY page_id`, tenantID, country)
	if err != nil {
		return nil, fmt.Errorf("list configuration page drafts: %w", err)
	}
	defer rows.Close()
	result := []PageDraft{}
	for rows.Next() {
		var draft PageDraft
		var pageID string
		var document []byte
		if err := rows.Scan(&pageID, &draft.Revision, &document, &draft.UpdatedBy, &draft.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan configuration page draft: %w", err)
		}
		if err := json.Unmarshal(document, &draft.Page); err != nil || draft.Page.ID != pageID {
			return nil, fmt.Errorf("decode configuration page draft: %w", ErrInvalidSnapshot)
		}
		draft.TenantID, draft.Country = tenantID, country
		if !validPageDraft(draft) {
			return nil, fmt.Errorf("stored configuration page draft is invalid: %w", ErrInvalidSnapshot)
		}
		result = append(result, clonePageDraft(draft))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate configuration page drafts: %w", err)
	}
	return result, nil
}

func (repository *PostgresRepository) GetPageDraft(ctx context.Context, tenantID, country, pageID string) (PageDraft, error) {
	if _, err := uuid.Parse(tenantID); err != nil || len(country) != 2 || !safeID(pageID) {
		return PageDraft{}, ErrInvalidSnapshot
	}
	var draft PageDraft
	var document []byte
	err := repository.pool.QueryRow(ctx, `
		SELECT revision, document, updated_by, updated_at
		FROM configuration.page_drafts
		WHERE tenant_id = $1 AND country = $2 AND page_id = $3`, tenantID, country, pageID).Scan(&draft.Revision, &document, &draft.UpdatedBy, &draft.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PageDraft{}, ErrNotFound
	}
	if err != nil {
		return PageDraft{}, fmt.Errorf("load configuration page draft: %w", err)
	}
	if err := json.Unmarshal(document, &draft.Page); err != nil || draft.Page.ID != pageID {
		return PageDraft{}, fmt.Errorf("decode configuration page draft: %w", ErrInvalidSnapshot)
	}
	draft.TenantID, draft.Country = tenantID, country
	if !validPageDraft(draft) {
		return PageDraft{}, fmt.Errorf("stored configuration page draft is invalid: %w", ErrInvalidSnapshot)
	}
	return clonePageDraft(draft), nil
}

func (repository *PostgresRepository) SavePageDraft(ctx context.Context, draft PageDraft, expectedRevision int64) error {
	if !validPageDraft(draft) || expectedRevision < 0 || draft.Revision != expectedRevision+1 {
		return ErrInvalidSnapshot
	}
	if _, err := uuid.Parse(draft.TenantID); err != nil {
		return ErrInvalidSnapshot
	}
	document, err := json.Marshal(draft.Page)
	if err != nil {
		return ErrInvalidSnapshot
	}
	if expectedRevision == 0 {
		_, err = repository.pool.Exec(ctx, `
			INSERT INTO configuration.page_drafts
				(tenant_id, country, page_id, revision, document, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`, draft.TenantID, draft.Country, draft.Page.ID, draft.Revision, string(document), draft.UpdatedBy, draft.UpdatedAt)
		if isUniqueViolation(err) {
			return ErrRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("insert configuration page draft: %w", err)
		}
		return nil
	}
	command, err := repository.pool.Exec(ctx, `
		UPDATE configuration.page_drafts
		SET revision = $4, document = $5::jsonb, updated_by = $6, updated_at = $7
		WHERE tenant_id = $1 AND country = $2 AND page_id = $3 AND revision = $8`, draft.TenantID, draft.Country, draft.Page.ID, draft.Revision, string(document), draft.UpdatedBy, draft.UpdatedAt, expectedRevision)
	if err != nil {
		return fmt.Errorf("update configuration page draft: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrRevisionConflict
	}
	return nil
}

func (repository *PostgresRepository) Get(ctx context.Context, tenantID, country string) (Snapshot, error) {
	if _, err := uuid.Parse(tenantID); err != nil || len(country) != 2 {
		return Snapshot{}, ErrInvalidSnapshot
	}
	var revision int64
	var document []byte
	var publishedAt time.Time
	row := repository.pool.QueryRow(ctx, `
		SELECT revision, document, published_at
		FROM configuration.snapshots
		WHERE tenant_id = $1 AND country = $2`, tenantID, country)
	var snapshot Snapshot
	if err := row.Scan(&revision, &document, &publishedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, ErrNotFound
		}
		return Snapshot{}, fmt.Errorf("load configuration snapshot: %w", err)
	}
	if err := json.Unmarshal(document, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode configuration snapshot: %w", err)
	}
	snapshot.TenantID, snapshot.Country, snapshot.Revision, snapshot.PublishedAt = tenantID, country, revision, publishedAt
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("stored configuration snapshot is invalid: %w", err)
	}
	return cloneSnapshot(snapshot), nil
}

func (repository *PostgresRepository) Publish(context.Context, Snapshot, int64) error {
	return ErrAuditRequired
}

func (repository *PostgresRepository) PublishWithAudit(ctx context.Context, snapshot Snapshot, expectedRevision int64, record AuditRecord) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	if _, err := uuid.Parse(snapshot.TenantID); err != nil || expectedRevision < 0 || expectedRevision == math.MaxInt64 || record.TenantID != snapshot.TenantID || record.Country != snapshot.Country || record.PreviousRevision != expectedRevision || record.NewRevision != snapshot.Revision || record.ActorID == "" || record.Reason == "" || record.RecordedAt.IsZero() {
		return ErrInvalidSnapshot
	}
	if snapshot.Revision != expectedRevision+1 {
		return ErrRevisionConflict
	}
	eventID, err := uuid.Parse(record.EventID)
	if err != nil {
		return ErrInvalidSnapshot
	}
	document, err := json.Marshal(snapshot)
	if err != nil {
		return ErrInvalidSnapshot
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin configuration publication: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var currentRevision int64
	err = transaction.QueryRow(ctx, `
		SELECT revision FROM configuration.snapshots
		WHERE tenant_id = $1 AND country = $2
		FOR UPDATE`, snapshot.TenantID, snapshot.Country).Scan(&currentRevision)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if expectedRevision != 0 {
			return ErrRevisionConflict
		}
		if _, err = transaction.Exec(ctx, `
			INSERT INTO configuration.snapshots (tenant_id, country, revision, document, published_at)
			VALUES ($1, $2, $3, $4::jsonb, $5)`, snapshot.TenantID, snapshot.Country, snapshot.Revision, string(document), snapshot.PublishedAt); err != nil {
			if isUniqueViolation(err) {
				return ErrRevisionConflict
			}
			return fmt.Errorf("insert configuration snapshot: %w", err)
		}
	case err != nil:
		return fmt.Errorf("lock configuration snapshot: %w", err)
	default:
		if currentRevision != expectedRevision {
			return ErrRevisionConflict
		}
		command, updateErr := transaction.Exec(ctx, `
			UPDATE configuration.snapshots
			SET revision = $3, document = $4::jsonb, published_at = $5
			WHERE tenant_id = $1 AND country = $2 AND revision = $6`, snapshot.TenantID, snapshot.Country, snapshot.Revision, string(document), snapshot.PublishedAt, expectedRevision)
		if updateErr != nil {
			return fmt.Errorf("update configuration snapshot: %w", updateErr)
		}
		if command.RowsAffected() != 1 {
			return ErrRevisionConflict
		}
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO configuration.publish_history
			(event_id, tenant_id, country, previous_revision, new_revision, actor_id, reason, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, eventID, record.TenantID, record.Country, record.PreviousRevision, record.NewRevision, record.ActorID, record.Reason, record.RecordedAt); err != nil {
		if isUniqueViolation(err) {
			return ErrRevisionConflict
		}
		return fmt.Errorf("append configuration publication audit: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit configuration publication: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
