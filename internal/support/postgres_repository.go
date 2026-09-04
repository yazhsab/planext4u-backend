package support

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresRepository{pool: pool}, nil
}

func (repository *PostgresRepository) Ready(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `SELECT to_regclass('support.tickets') IS NOT NULL AND to_regclass('support.messages') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check support schema readiness: %w", err)
	}
	if !ready {
		return errors.New("support schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) Create(ctx context.Context, ticket Ticket, initial Message, key, digest string) (Ticket, bool, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Ticket{}, false, fmt.Errorf("begin support ticket: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `
		INSERT INTO support.tickets
			(id, tenant_id, country, owner_identity_id, owner_role, category, subject,
			 related_reference, priority, status, create_idempotency_key, request_fingerprint,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10, $11, $12, $13, $13)
		ON CONFLICT (tenant_id, country, owner_identity_id, owner_role, create_idempotency_key) DO NOTHING`,
		ticket.ID, ticket.TenantID, ticket.Country, ticket.OwnerSubject, ticket.OwnerRole, ticket.Category,
		ticket.Subject, ticket.RelatedReference, ticket.Priority, ticket.Status, key, digest, ticket.CreatedAt)
	if err != nil {
		return Ticket{}, false, fmt.Errorf("insert support ticket: %w", err)
	}
	created := result.RowsAffected() == 1
	if !created {
		var existingID, existingDigest string
		if err = transaction.QueryRow(ctx, `
			SELECT id::text, request_fingerprint
			FROM support.tickets
			WHERE tenant_id = $1 AND country = $2 AND owner_identity_id = $3
			  AND owner_role = $4 AND create_idempotency_key = $5`,
			ticket.TenantID, ticket.Country, ticket.OwnerSubject, ticket.OwnerRole, key).Scan(&existingID, &existingDigest); err != nil {
			return Ticket{}, false, fmt.Errorf("read replayed support ticket: %w", err)
		}
		if existingDigest != digest {
			return Ticket{}, false, ErrIdempotencyConflict
		}
		value, loadErr := loadPostgresTicket(ctx, transaction, existingID)
		if loadErr != nil {
			return Ticket{}, false, loadErr
		}
		if err = transaction.Commit(ctx); err != nil {
			return Ticket{}, false, fmt.Errorf("commit support ticket replay: %w", err)
		}
		return value, false, nil
	}
	if _, err = transaction.Exec(ctx, `
		INSERT INTO support.messages
			(id, tenant_id, country, ticket_id, author_type, author_identity_id, body,
			 idempotency_key, request_fingerprint, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, $8, $9)`, initial.ID, ticket.TenantID,
		ticket.Country, ticket.ID, initial.AuthorType, ticket.OwnerSubject, initial.Body, digest, initial.CreatedAt); err != nil {
		return Ticket{}, false, fmt.Errorf("insert initial support message: %w", err)
	}
	if err = transaction.Commit(ctx); err != nil {
		return Ticket{}, false, fmt.Errorf("commit support ticket: %w", err)
	}
	ticket.Messages = []Message{initial}
	return cloneTicket(ticket), true, nil
}

func (repository *PostgresRepository) List(ctx context.Context, actor Actor, role Role, limit int, after cursor) (TicketPage, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT id::text
		FROM support.tickets
		WHERE tenant_id = $1 AND country = $2 AND owner_identity_id = $3 AND owner_role = $4
		  AND ($5::boolean = false OR (created_at, id) < ($6::timestamptz, $7::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $8`, actor.TenantID, actor.Country, actor.Subject, role, after.Present, nullableCursorTime(after), nullableCursorID(after), limit+1)
	if err != nil {
		return TicketPage{}, fmt.Errorf("list support tickets: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return TicketPage{}, fmt.Errorf("scan support ticket id: %w", err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return TicketPage{}, fmt.Errorf("iterate support tickets: %w", err)
	}
	values := make([]Ticket, 0, len(ids))
	for _, id := range ids {
		value, loadErr := loadPostgresTicket(ctx, repository.pool, id)
		if loadErr != nil {
			return TicketPage{}, loadErr
		}
		values = append(values, value)
	}
	return ticketPage(values, limit), nil
}

func (repository *PostgresRepository) Get(ctx context.Context, actor Actor, id string) (Ticket, error) {
	var allowed bool
	roles := make([]string, 0, len(actor.Roles))
	for _, role := range actor.Roles {
		roles = append(roles, string(role))
	}
	err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM support.tickets
			WHERE id = $1 AND tenant_id = $2 AND country = $3 AND owner_identity_id = $4
			  AND owner_role = ANY($5::text[])
		)`, id, actor.TenantID, actor.Country, actor.Subject, roles).Scan(&allowed)
	if err != nil {
		return Ticket{}, fmt.Errorf("authorize support ticket: %w", err)
	}
	if !allowed {
		return Ticket{}, ErrNotFound
	}
	return loadPostgresTicket(ctx, repository.pool, id)
}

func (repository *PostgresRepository) AddMessage(ctx context.Context, actor Actor, ticketID string, message Message, key, digest string) (Ticket, bool, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Ticket{}, false, fmt.Errorf("begin support message: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	roles := make([]string, 0, len(actor.Roles))
	for _, role := range actor.Roles {
		roles = append(roles, string(role))
	}
	var status Status
	if err = transaction.QueryRow(ctx, `
		SELECT status FROM support.tickets
		WHERE id = $1 AND tenant_id = $2 AND country = $3 AND owner_identity_id = $4
		  AND owner_role = ANY($5::text[])
		FOR UPDATE`, ticketID, actor.TenantID, actor.Country, actor.Subject, roles).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, false, ErrNotFound
	} else if err != nil {
		return Ticket{}, false, fmt.Errorf("authorize support message: %w", err)
	}
	if status == StatusClosed {
		return Ticket{}, false, ErrForbidden
	}
	result, err := transaction.Exec(ctx, `
		INSERT INTO support.messages
			(id, tenant_id, country, ticket_id, author_type, author_identity_id, body,
			 idempotency_key, request_fingerprint, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (ticket_id, idempotency_key) DO NOTHING`, message.ID, actor.TenantID, actor.Country,
		ticketID, message.AuthorType, actor.Subject, message.Body, key, digest, message.CreatedAt)
	if err != nil {
		return Ticket{}, false, fmt.Errorf("insert support message: %w", err)
	}
	created := result.RowsAffected() == 1
	if !created {
		var existingDigest string
		if err = transaction.QueryRow(ctx, `SELECT request_fingerprint FROM support.messages WHERE ticket_id = $1 AND idempotency_key = $2`, ticketID, key).Scan(&existingDigest); err != nil {
			return Ticket{}, false, fmt.Errorf("read replayed support message: %w", err)
		}
		if existingDigest != digest {
			return Ticket{}, false, ErrIdempotencyConflict
		}
	} else {
		if _, err = transaction.Exec(ctx, `
			UPDATE support.tickets
			SET updated_at = $2,
			    status = CASE WHEN status = 'WAITING_FOR_REQUESTER' THEN 'WAITING_FOR_SUPPORT' ELSE status END
			WHERE id = $1`, ticketID, message.CreatedAt); err != nil {
			return Ticket{}, false, fmt.Errorf("update support ticket activity: %w", err)
		}
	}
	value, err := loadPostgresTicket(ctx, transaction, ticketID)
	if err != nil {
		return Ticket{}, false, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return Ticket{}, false, fmt.Errorf("commit support message: %w", err)
	}
	return value, created, nil
}

func (repository *PostgresRepository) AdminList(ctx context.Context, principal AdminPrincipal, filter AdminListFilter, after cursor) (AdminTicketPage, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT t.id::text, t.owner_role, t.owner_identity_id, t.category, t.subject,
		       COALESCE(t.related_reference, ''), t.priority, t.status, COUNT(m.id)::integer,
		       COALESCE(MAX(m.created_at), t.created_at), t.created_at, t.updated_at
		FROM support.tickets t
		LEFT JOIN support.messages m ON m.ticket_id = t.id
		WHERE t.tenant_id = $1 AND t.country = $2
		  AND ($3::text = '' OR t.owner_role = $3)
		  AND ($4::text = '' OR t.status = $4)
		  AND ($5::boolean = false OR (t.created_at, t.id) < ($6::timestamptz, $7::uuid))
		GROUP BY t.id
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $8`, principal.TenantID, principal.Country, filter.OwnerRole, filter.Status, after.Present,
		nullableCursorTime(after), nullableCursorID(after), filter.Limit+1)
	if err != nil {
		return AdminTicketPage{}, fmt.Errorf("list admin support tickets: %w", err)
	}
	defer rows.Close()
	values := make([]AdminTicket, 0, filter.Limit+1)
	for rows.Next() {
		var value AdminTicket
		if err = rows.Scan(&value.ID, &value.OwnerRole, &value.OwnerReference, &value.Category, &value.Subject,
			&value.RelatedReference, &value.Priority, &value.Status, &value.MessageCount, &value.LastMessageAt,
			&value.CreatedAt, &value.UpdatedAt); err != nil {
			return AdminTicketPage{}, fmt.Errorf("scan admin support ticket: %w", err)
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return AdminTicketPage{}, fmt.Errorf("iterate admin support tickets: %w", err)
	}
	return adminTicketPage(values, filter.Limit), nil
}

type supportQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadPostgresTicket(ctx context.Context, querier supportQuerier, id string) (Ticket, error) {
	var value Ticket
	err := querier.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, country, owner_identity_id, owner_role, category,
		       subject, COALESCE(related_reference, ''), priority, status, request_fingerprint,
		       created_at, updated_at
		FROM support.tickets WHERE id = $1`, id).Scan(&value.ID, &value.TenantID, &value.Country,
		&value.OwnerSubject, &value.OwnerRole, &value.Category, &value.Subject, &value.RelatedReference,
		&value.Priority, &value.Status, &value.RequestDigest, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("read support ticket: %w", err)
	}
	rows, err := querier.Query(ctx, `
		SELECT id::text, author_type, body, created_at
		FROM support.messages WHERE ticket_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return Ticket{}, fmt.Errorf("list support messages: %w", err)
	}
	defer rows.Close()
	value.Messages = []Message{}
	for rows.Next() {
		var message Message
		if err = rows.Scan(&message.ID, &message.AuthorType, &message.Body, &message.CreatedAt); err != nil {
			return Ticket{}, fmt.Errorf("scan support message: %w", err)
		}
		value.Messages = append(value.Messages, message)
	}
	if err = rows.Err(); err != nil {
		return Ticket{}, fmt.Errorf("iterate support messages: %w", err)
	}
	return value, nil
}

func nullableCursorTime(value cursor) any {
	if !value.Present {
		return nil
	}
	return value.CreatedAt
}

func nullableCursorID(value cursor) any {
	if !value.Present {
		return nil
	}
	return value.ID
}
