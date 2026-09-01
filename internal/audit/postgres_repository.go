package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		return nil, ErrInvalidRequest
	}
	return &PostgresRepository{pool: pool}, nil
}

func (repository *PostgresRepository) Ready(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT to_regclass('audit.events') IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema = 'audit' AND table_name = 'events' AND column_name = 'tenant_sequence'
		   )`).Scan(&ready); err != nil {
		return fmt.Errorf("check audit schema readiness: %w", err)
	}
	if !ready {
		return errors.New("audit schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) Append(ctx context.Context, entry Entry) error {
	if !validPostgresEntry(entry) {
		return ErrInvalidRequest
	}
	actor, err := json.Marshal(entry.Actor)
	if err != nil {
		return ErrInvalidRequest
	}
	target, err := json.Marshal(entry.Target)
	if err != nil {
		return ErrInvalidRequest
	}
	before, err := marshalOptionalJSON(entry.Before)
	if err != nil {
		return ErrInvalidRequest
	}
	after, err := marshalOptionalJSON(entry.After)
	if err != nil {
		return ErrInvalidRequest
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin audit append: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, entry.TenantID); err != nil {
		return fmt.Errorf("lock audit tenant chain: %w", err)
	}
	last, found, err := lastPostgresEntry(ctx, transaction, entry.TenantID)
	if err != nil {
		return err
	}
	if (!found && (entry.Sequence != 1 || entry.PreviousHash != "")) || (found && (entry.Sequence != last.Sequence+1 || entry.PreviousHash != last.Hash)) {
		return ErrConflict
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO audit.events
			(id, tenant_id, tenant_sequence, country, actor, action, target, outcome,
			 reason_code, correlation_id, occurred_at, recorded_at, before_value,
			 after_value, previous_hash, hash)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8, $9, $10, $11, $12,
		        $13::jsonb, $14::jsonb, $15, $16)`,
		entry.ID, entry.TenantID, entry.Sequence, entry.Country, string(actor), entry.Action, string(target), entry.Outcome,
		entry.ReasonCode, entry.CorrelationID, entry.OccurredAt, entry.RecordedAt, before, after, nullableHash(entry.PreviousHash), entry.Hash)
	if err != nil {
		if auditUniqueViolation(err) {
			return ErrConflict
		}
		return fmt.Errorf("insert audit event: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit event: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) Last(ctx context.Context, tenantID string) (Entry, bool, error) {
	if _, err := uuid.Parse(tenantID); err != nil {
		return Entry{}, false, ErrInvalidRequest
	}
	return lastPostgresEntry(ctx, repository.pool, tenantID)
}

func (repository *PostgresRepository) Search(ctx context.Context, filter SearchFilter) ([]Entry, error) {
	if _, err := uuid.Parse(filter.TenantID); err != nil || filter.After < 0 || filter.Limit < 0 || filter.Limit > 200 || (filter.Country != "" && (len(filter.Country) != 2 || strings.ToUpper(filter.Country) != filter.Country)) {
		return nil, ErrInvalidRequest
	}
	limit := filter.Limit
	if limit == 0 {
		limit = 200
	}
	var from, to *time.Time
	if !filter.From.IsZero() {
		value := filter.From.UTC()
		from = &value
	}
	if !filter.To.IsZero() {
		value := filter.To.UTC()
		to = &value
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, tenant_sequence, country, actor, action, target,
		       outcome, reason_code, correlation_id, occurred_at, recorded_at,
		       before_value, after_value, previous_hash, hash
		FROM audit.events
		WHERE tenant_id = $1 AND tenant_sequence > $2
		  AND ($3 = '' OR country = $3)
		  AND ($4 = '' OR actor->>'subject_id' = $4)
		  AND ($5 = '' OR action = $5)
		  AND ($6 = '' OR target->>'id' = $6)
		  AND ($7::timestamptz IS NULL OR occurred_at >= $7)
		  AND ($8::timestamptz IS NULL OR occurred_at <= $8)
		ORDER BY tenant_sequence
		LIMIT $9`, filter.TenantID, filter.After, filter.Country, filter.ActorID, filter.Action, filter.TargetID, from, to, limit+1)
	if err != nil {
		return nil, fmt.Errorf("search audit events: %w", err)
	}
	defer rows.Close()
	values := []Entry{}
	for rows.Next() {
		value, err := scanPostgresEntry(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return values, nil
}

type auditRow interface {
	Scan(...any) error
}

type auditQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func lastPostgresEntry(ctx context.Context, querier auditQuerier, tenantID string) (Entry, bool, error) {
	row := querier.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, tenant_sequence, country, actor, action, target,
		       outcome, reason_code, correlation_id, occurred_at, recorded_at,
		       before_value, after_value, previous_hash, hash
		FROM audit.events
		WHERE tenant_id = $1
		ORDER BY tenant_sequence DESC
		LIMIT 1`, tenantID)
	value, err := scanPostgresEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return value, true, nil
}

func scanPostgresEntry(row auditRow) (Entry, error) {
	var value Entry
	var actor, target, before, after []byte
	var previousHash *string
	if err := row.Scan(&value.ID, &value.TenantID, &value.Sequence, &value.Country, &actor, &value.Action, &target,
		&value.Outcome, &value.ReasonCode, &value.CorrelationID, &value.OccurredAt, &value.RecordedAt,
		&before, &after, &previousHash, &value.Hash); err != nil {
		return Entry{}, err
	}
	if err := json.Unmarshal(actor, &value.Actor); err != nil {
		return Entry{}, fmt.Errorf("decode audit actor: %w", err)
	}
	if err := json.Unmarshal(target, &value.Target); err != nil {
		return Entry{}, fmt.Errorf("decode audit target: %w", err)
	}
	if len(before) > 0 && string(before) != "null" {
		if err := json.Unmarshal(before, &value.Before); err != nil {
			return Entry{}, fmt.Errorf("decode audit before value: %w", err)
		}
	}
	if len(after) > 0 && string(after) != "null" {
		if err := json.Unmarshal(after, &value.After); err != nil {
			return Entry{}, fmt.Errorf("decode audit after value: %w", err)
		}
	}
	if previousHash != nil {
		value.PreviousHash = *previousHash
	}
	return value, nil
}

func validPostgresEntry(entry Entry) bool {
	_, tenantErr := uuid.Parse(entry.TenantID)
	_, idErr := uuid.Parse(entry.ID)
	return tenantErr == nil && idErr == nil && entry.Sequence >= 1 && len(entry.Country) == 2 && strings.ToUpper(entry.Country) == entry.Country &&
		validRecord(RecordRequest{Country: entry.Country, Actor: entry.Actor, Action: entry.Action, Target: entry.Target, Outcome: entry.Outcome, ReasonCode: entry.ReasonCode, CorrelationID: entry.CorrelationID, OccurredAt: entry.OccurredAt}) &&
		!entry.RecordedAt.IsZero() && len(entry.Hash) == 64 && entry.Hash == hashEntry(entry) && (entry.PreviousHash == "" || len(entry.PreviousHash) == 64)
}

func marshalOptionalJSON(value map[string]any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func nullableHash(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func auditUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

var _ Repository = (*PostgresRepository)(nil)
