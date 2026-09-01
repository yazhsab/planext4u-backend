package configcms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) GetWorkspaceDraft(ctx context.Context, tenantID, country string) (WorkspaceDraft, error) {
	if _, err := uuid.Parse(tenantID); err != nil || len(country) != 2 {
		return WorkspaceDraft{}, ErrInvalidSnapshot
	}
	var value WorkspaceDraft
	var document []byte
	err := repository.pool.QueryRow(ctx, `
		SELECT revision,document,updated_by,updated_at FROM configuration.workspace_drafts
		WHERE tenant_id=$1 AND country=$2`, tenantID, country).Scan(&value.Revision, &document, &value.UpdatedBy, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkspaceDraft{}, ErrNotFound
	}
	if err != nil {
		return WorkspaceDraft{}, fmt.Errorf("load configuration workspace draft: %w", err)
	}
	value.TenantID, value.Country = tenantID, country
	if json.Unmarshal(document, &value.Workspace) != nil || !validWorkspaceDraft(value) {
		return WorkspaceDraft{}, fmt.Errorf("decode configuration workspace draft: %w", ErrInvalidSnapshot)
	}
	return cloneWorkspaceDraft(value), nil
}

func (repository *PostgresRepository) SaveWorkspaceDraft(ctx context.Context, draft WorkspaceDraft, expectedRevision int64) error {
	if !validWorkspaceDraft(draft) || expectedRevision < 0 || draft.Revision != expectedRevision+1 {
		return ErrInvalidSnapshot
	}
	if _, err := uuid.Parse(draft.TenantID); err != nil {
		return ErrInvalidSnapshot
	}
	document, err := json.Marshal(draft.Workspace)
	if err != nil {
		return ErrInvalidSnapshot
	}
	if expectedRevision == 0 {
		_, err = repository.pool.Exec(ctx, `
			INSERT INTO configuration.workspace_drafts (tenant_id,country,revision,document,updated_by,updated_at)
			VALUES ($1,$2,1,$3::jsonb,$4,$5)`, draft.TenantID, draft.Country, document, draft.UpdatedBy, draft.UpdatedAt)
		if isUniqueViolation(err) {
			return ErrRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("insert configuration workspace draft: %w", err)
		}
		return nil
	}
	command, err := repository.pool.Exec(ctx, `
		UPDATE configuration.workspace_drafts SET revision=$3,document=$4::jsonb,updated_by=$5,updated_at=$6
		WHERE tenant_id=$1 AND country=$2 AND revision=$7`, draft.TenantID, draft.Country, draft.Revision, document,
		draft.UpdatedBy, draft.UpdatedAt, expectedRevision)
	if err != nil {
		return fmt.Errorf("update configuration workspace draft: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrRevisionConflict
	}
	return nil
}
