package identity

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DeletionPolicyReview struct {
	ID                    string     `json:"id"`
	Owner                 string     `json:"owner"`
	ExpectedRevision      int64      `json:"expected_revision"`
	PolicyVersion         string     `json:"policy_version"`
	RetentionMicroseconds int64      `json:"retention_microseconds"`
	ReasonReference       string     `json:"reason_reference"`
	Status                string     `json:"status"`
	RequestedBy           string     `json:"requested_by"`
	RequestedAt           time.Time  `json:"requested_at"`
	ApprovedBy            *string    `json:"approved_by,omitempty"`
	PublishedAt           *time.Time `json:"published_at,omitempty"`
}

type DeletionHoldReview struct {
	ID              int64     `json:"id"`
	RequestID       string    `json:"request_id"`
	Owner           string    `json:"owner"`
	ReasonReference string    `json:"reason_reference"`
	RecordedBy      *string   `json:"recorded_by,omitempty"`
	RecordedAt      time.Time `json:"recorded_at"`
	Revision        int64     `json:"revision"`
}

type DeletionRequestReview struct {
	ID            string    `json:"id"`
	IdentityID    string    `json:"identity_id"`
	Status        string    `json:"status"`
	RequestedAt   time.Time `json:"requested_at"`
	EffectiveAt   time.Time `json:"effective_at"`
	ReadyOwners   int64     `json:"ready_owners"`
	BlockedOwners int64     `json:"blocked_owners"`
	ActiveHolds   int64     `json:"active_holds"`
}

func scanDeletionPolicyReview(rows pgx.Rows) (DeletionPolicyReview, error) {
	var value DeletionPolicyReview
	err := rows.Scan(&value.ID, &value.Owner, &value.ExpectedRevision,
		&value.PolicyVersion, &value.RetentionMicroseconds, &value.ReasonReference,
		&value.Status, &value.RequestedBy, &value.RequestedAt, &value.ApprovedBy,
		&value.PublishedAt)
	return value, err
}

func (repository *PostgresRepository) ListDeletionPolicyProposals(ctx context.Context, actor DeletionGovernor) ([]DeletionPolicyReview, error) {
	if !validDeletionGovernor(actor) {
		return nil, ErrInvalidInput
	}
	rows, err := repository.pool.Query(ctx, `SELECT id::text,owner,expected_revision,policy_version,
	 retention_microseconds,reason_reference,status,requested_by::text,requested_at,
	 approved_by::text,published_at FROM identity.list_deletion_policy_proposals($1,$2,$3,$4,$5)`,
		actor.TenantID, actor.Country, actor.SubjectID, actor.OperatorSessionID, actor.OperatorRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DeletionPolicyReview, 0)
	for rows.Next() {
		value, scanErr := scanDeletionPolicyReview(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (repository *PostgresRepository) GetDeletionPolicyProposal(ctx context.Context, actor DeletionGovernor, proposalID string) (DeletionPolicyReview, error) {
	if !validDeletionGovernor(actor) || uuid.Validate(proposalID) != nil {
		return DeletionPolicyReview{}, ErrInvalidInput
	}
	rows, err := repository.pool.Query(ctx, `SELECT id::text,owner,expected_revision,policy_version,
	 retention_microseconds,reason_reference,status,requested_by::text,requested_at,
	 approved_by::text,published_at FROM identity.get_deletion_policy_proposal($1,$2,$3,$4,$5,$6)`,
		proposalID, actor.TenantID, actor.Country, actor.SubjectID,
		actor.OperatorSessionID, actor.OperatorRevision)
	if err != nil {
		return DeletionPolicyReview{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			return DeletionPolicyReview{}, rows.Err()
		}
		return DeletionPolicyReview{}, ErrNotFound
	}
	value, err := scanDeletionPolicyReview(rows)
	if err != nil {
		return DeletionPolicyReview{}, err
	}
	return value, rows.Err()
}

func (repository *PostgresRepository) ListDeletionHolds(ctx context.Context, actor DeletionGovernor) ([]DeletionHoldReview, error) {
	if !validDeletionGovernor(actor) {
		return nil, ErrInvalidInput
	}
	rows, err := repository.pool.Query(ctx, `SELECT id,request_id,owner,reason_reference,
	 recorded_by::text,recorded_at,revision FROM identity.list_deletion_holds($1,$2,$3,$4,$5)`,
		actor.TenantID, actor.Country, actor.SubjectID, actor.OperatorSessionID, actor.OperatorRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DeletionHoldReview, 0)
	for rows.Next() {
		var value DeletionHoldReview
		if err = rows.Scan(&value.ID, &value.RequestID, &value.Owner,
			&value.ReasonReference, &value.RecordedBy, &value.RecordedAt,
			&value.Revision); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanDeletionRequestReview(rows pgx.Rows) (DeletionRequestReview, error) {
	var value DeletionRequestReview
	err := rows.Scan(&value.ID, &value.IdentityID, &value.Status,
		&value.RequestedAt, &value.EffectiveAt, &value.ReadyOwners,
		&value.BlockedOwners, &value.ActiveHolds)
	return value, err
}

func (repository *PostgresRepository) ListDeletionRequests(ctx context.Context, actor DeletionGovernor) ([]DeletionRequestReview, error) {
	if !validDeletionGovernor(actor) {
		return nil, ErrInvalidInput
	}
	rows, err := repository.pool.Query(ctx, `SELECT * FROM identity.list_deletion_requests($1,$2,$3,$4,$5)`,
		actor.TenantID, actor.Country, actor.SubjectID, actor.OperatorSessionID, actor.OperatorRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DeletionRequestReview, 0)
	for rows.Next() {
		value, scanErr := scanDeletionRequestReview(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (repository *PostgresRepository) GetDeletionRequest(ctx context.Context, actor DeletionGovernor, requestID string) (DeletionRequestReview, error) {
	if !validDeletionGovernor(actor) || !validSafeIdentifier(requestID, 128) {
		return DeletionRequestReview{}, ErrInvalidInput
	}
	rows, err := repository.pool.Query(ctx, `SELECT * FROM identity.get_deletion_request($1,$2,$3,$4,$5,$6)`,
		requestID, actor.TenantID, actor.Country, actor.SubjectID,
		actor.OperatorSessionID, actor.OperatorRevision)
	if err != nil {
		return DeletionRequestReview{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			return DeletionRequestReview{}, rows.Err()
		}
		return DeletionRequestReview{}, ErrNotFound
	}
	value, err := scanDeletionRequestReview(rows)
	if err != nil {
		return DeletionRequestReview{}, err
	}
	return value, rows.Err()
}
