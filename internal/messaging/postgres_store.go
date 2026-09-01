package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresStore{pool: pool}, nil
}

func (store *PostgresStore) Ready(ctx context.Context) error {
	var ready bool
	if err := store.pool.QueryRow(ctx, `
		SELECT to_regclass('messaging.outbox') IS NOT NULL
		   AND to_regclass('messaging.inbox') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check messaging schema readiness: %w", err)
	}
	if !ready {
		return errors.New("messaging schema is unavailable")
	}
	return nil
}

func (store *PostgresStore) Enqueue(ctx context.Context, message Message, now time.Time) error {
	if !validMessage(message) || now.IsZero() {
		return ErrInvalidRequest
	}
	envelope, err := json.Marshal(message)
	if err != nil {
		return ErrInvalidRequest
	}
	_, err = store.pool.Exec(ctx, `
		INSERT INTO messaging.outbox
			(event_id, tenant_id, aggregate_type, aggregate_id, aggregate_version,
			 envelope, state, attempts, next_attempt_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, 'PENDING', 0, $7, $7)`,
		message.ID, message.TenantID, message.AggregateType, message.AggregateID,
		message.AggregateVersion, string(envelope), now.UTC())
	if err != nil {
		if messagingConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("enqueue outbox message: %w", err)
	}
	return nil
}

func (store *PostgresStore) Claim(ctx context.Context, worker string, now time.Time, lease time.Duration, limit int) ([]OutboxRecord, error) {
	if !safeID(worker) || now.IsZero() || lease < time.Second || lease > 5*time.Minute || limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	rows, err := store.pool.Query(ctx, `
		WITH candidates AS (
			SELECT event_id
			FROM messaging.outbox
			WHERE next_attempt_at <= $1
			  AND (state = 'PENDING' OR (state = 'LEASED' AND lease_until <= $1))
			ORDER BY next_attempt_at, event_id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE messaging.outbox AS event
		SET state = 'LEASED', lease_owner = $3, lease_until = $1 + ($4 * interval '1 microsecond')
		FROM candidates
		WHERE event.event_id = candidates.event_id
		RETURNING event.envelope, event.state, event.attempts, event.next_attempt_at,
		          event.lease_owner, event.lease_until, event.last_error_code,
		          event.created_at, event.published_at, event.dead_at`,
		now.UTC(), limit, worker, lease.Microseconds())
	if err != nil {
		return nil, fmt.Errorf("claim outbox messages: %w", err)
	}
	defer rows.Close()
	result := make([]OutboxRecord, 0, limit)
	for rows.Next() {
		value, scanErr := scanPostgresOutbox(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox messages: %w", err)
	}
	return result, nil
}

func (store *PostgresStore) MarkPublished(ctx context.Context, id, worker string, now time.Time) error {
	if _, err := uuid.Parse(id); err != nil || !safeID(worker) || now.IsZero() {
		return ErrInvalidRequest
	}
	result, err := store.pool.Exec(ctx, `
		UPDATE messaging.outbox
		SET state = 'PUBLISHED', lease_owner = NULL, lease_until = NULL, published_at = $3
		WHERE event_id = $1 AND state = 'LEASED' AND lease_owner = $2 AND lease_until > $3`, id, worker, now.UTC())
	if err != nil {
		return fmt.Errorf("mark outbox message published: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (store *PostgresStore) MarkFailed(ctx context.Context, id, worker string, now, next time.Time, code string, maxAttempts int) (OutboxState, error) {
	if _, err := uuid.Parse(id); err != nil || !safeID(worker) || now.IsZero() || next.Before(now) || !safeID(code) || maxAttempts < 1 || maxAttempts > 20 {
		return "", ErrInvalidRequest
	}
	var state OutboxState
	err := store.pool.QueryRow(ctx, `
		UPDATE messaging.outbox
		SET attempts = attempts + 1,
		    state = CASE WHEN attempts + 1 >= $6 THEN 'DEAD_LETTER' ELSE 'PENDING' END,
		    next_attempt_at = CASE WHEN attempts + 1 >= $6 THEN next_attempt_at ELSE $4 END,
		    dead_at = CASE WHEN attempts + 1 >= $6 THEN $3 ELSE NULL END,
		    lease_owner = NULL, lease_until = NULL, last_error_code = $5
		WHERE event_id = $1 AND state = 'LEASED' AND lease_owner = $2 AND lease_until > $3
		RETURNING state`, id, worker, now.UTC(), next.UTC(), code, maxAttempts).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConflict
	}
	if err != nil {
		return "", fmt.Errorf("mark outbox message failed: %w", err)
	}
	return state, nil
}

func (store *PostgresStore) DeadLetters(ctx context.Context, tenantID string, limit int) ([]OutboxRecord, error) {
	if _, err := uuid.Parse(tenantID); err != nil || limit < 1 || limit > 200 {
		return nil, ErrInvalidRequest
	}
	rows, err := store.pool.Query(ctx, `
		SELECT envelope, state, attempts, next_attempt_at, lease_owner, lease_until,
		       last_error_code, created_at, published_at, dead_at
		FROM messaging.outbox
		WHERE tenant_id = $1 AND state = 'DEAD_LETTER'
		ORDER BY dead_at DESC, event_id
		LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list dead-letter messages: %w", err)
	}
	defer rows.Close()
	result := make([]OutboxRecord, 0, limit)
	for rows.Next() {
		value, scanErr := scanPostgresOutbox(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *PostgresStore) Replay(ctx context.Context, tenantID, id string, now time.Time) error {
	if _, err := uuid.Parse(tenantID); err != nil {
		return ErrInvalidRequest
	}
	if _, err := uuid.Parse(id); err != nil || now.IsZero() {
		return ErrInvalidRequest
	}
	result, err := store.pool.Exec(ctx, `
		UPDATE messaging.outbox
		SET state = 'PENDING', attempts = 0, next_attempt_at = $3, lease_owner = NULL,
		    lease_until = NULL, last_error_code = NULL, dead_at = NULL
		WHERE tenant_id = $1 AND event_id = $2 AND state = 'DEAD_LETTER'`, tenantID, id, now.UTC())
	if err != nil {
		return fmt.Errorf("replay dead-letter message: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (store *PostgresStore) Begin(ctx context.Context, consumer string, message Message, now time.Time, lease time.Duration) (ConsumeOutcome, error) {
	if !safeID(consumer) || !validMessage(message) || now.IsZero() || lease < time.Second || lease > 5*time.Minute {
		return "", ErrInvalidRequest
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", fmt.Errorf("begin inbox lease: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	lockKey := consumer + "|" + message.TenantID + "|" + message.AggregateType + "|" + message.AggregateID
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return "", fmt.Errorf("lock inbox aggregate: %w", err)
	}
	var state string
	var leaseUntil *time.Time
	err = transaction.QueryRow(ctx, `
		SELECT state, lease_until FROM messaging.inbox WHERE consumer = $1 AND event_id = $2`,
		consumer, message.ID).Scan(&state, &leaseUntil)
	if err == nil {
		if state == "DONE" {
			return ConsumeDuplicate, nil
		}
		if leaseUntil != nil && leaseUntil.After(now) {
			return "", ErrBusy
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("read inbox lease: %w", err)
	}
	var last int64
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(MAX(aggregate_version), 0)
		FROM messaging.inbox
		WHERE consumer = $1 AND tenant_id = $2 AND aggregate_type = $3 AND aggregate_id = $4 AND state = 'DONE'`,
		consumer, message.TenantID, message.AggregateType, message.AggregateID).Scan(&last); err != nil {
		return "", fmt.Errorf("read inbox sequence: %w", err)
	}
	if message.AggregateVersion <= last {
		return ConsumeDuplicate, nil
	}
	if message.AggregateVersion != last+1 {
		return ConsumeOutOfOrder, ErrOutOfOrder
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO messaging.inbox
			(consumer, event_id, tenant_id, aggregate_type, aggregate_id,
			 aggregate_version, state, lease_until, processed_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'PROCESSING', $7, NULL)
		ON CONFLICT (consumer, event_id) DO UPDATE
		SET state = 'PROCESSING', lease_until = EXCLUDED.lease_until, processed_at = NULL
		WHERE messaging.inbox.state = 'PROCESSING' AND messaging.inbox.lease_until <= $8`,
		consumer, message.ID, message.TenantID, message.AggregateType, message.AggregateID,
		message.AggregateVersion, now.UTC().Add(lease), now.UTC())
	if err != nil {
		if messagingConflict(err) {
			return "", ErrConflict
		}
		return "", fmt.Errorf("lease inbox message: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit inbox lease: %w", err)
	}
	return ConsumeProcessed, nil
}

func (store *PostgresStore) Complete(ctx context.Context, consumer string, message Message, now time.Time) error {
	if !safeID(consumer) || !validMessage(message) || now.IsZero() {
		return ErrInvalidRequest
	}
	result, err := store.pool.Exec(ctx, `
		UPDATE messaging.inbox
		SET state = 'DONE', lease_until = NULL, processed_at = $3
		WHERE consumer = $1 AND event_id = $2 AND state = 'PROCESSING' AND lease_until > $3`,
		consumer, message.ID, now.UTC())
	if err != nil {
		return fmt.Errorf("complete inbox message: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (store *PostgresStore) Release(ctx context.Context, consumer, messageID string) error {
	if !safeID(consumer) {
		return ErrInvalidRequest
	}
	if _, err := uuid.Parse(messageID); err != nil {
		return ErrInvalidRequest
	}
	result, err := store.pool.Exec(ctx, `
		DELETE FROM messaging.inbox WHERE consumer = $1 AND event_id = $2 AND state = 'PROCESSING'`, consumer, messageID)
	if err != nil {
		return fmt.Errorf("release inbox message: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

type postgresOutboxRow interface {
	Scan(...any) error
}

func scanPostgresOutbox(row postgresOutboxRow) (OutboxRecord, error) {
	var value OutboxRecord
	var envelope []byte
	var leaseOwner, lastErrorCode *string
	var leaseUntil *time.Time
	if err := row.Scan(&envelope, &value.State, &value.Attempts, &value.NextAttemptAt,
		&leaseOwner, &leaseUntil, &lastErrorCode, &value.CreatedAt,
		&value.PublishedAt, &value.DeadAt); err != nil {
		return OutboxRecord{}, err
	}
	if err := json.Unmarshal(envelope, &value.Message); err != nil || !validMessage(value.Message) {
		return OutboxRecord{}, errors.New("stored outbox envelope is invalid")
	}
	if leaseOwner != nil {
		value.LeaseOwner = *leaseOwner
	}
	if leaseUntil != nil {
		value.LeaseUntil = *leaseUntil
	}
	if lastErrorCode != nil {
		value.LastErrorCode = *lastErrorCode
	}
	return value, nil
}

func messagingConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

var _ OutboxStore = (*PostgresStore)(nil)
var _ InboxStore = (*PostgresStore)(nil)
