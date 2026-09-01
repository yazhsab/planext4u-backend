package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
		SELECT to_regclass('catalog.items') IS NOT NULL
		   AND to_regclass('catalog.customer_questions') IS NOT NULL
		   AND to_regclass('catalog.serviceability_zones') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check catalog schema readiness: %w", err)
	}
	if !ready {
		return errors.New("catalog schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) Zones(ctx context.Context) ([]Zone, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT tenant_id::text, country, id, locality,
		       minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude,
		       postal_codes
		FROM catalog.serviceability_zones
		WHERE enabled
		ORDER BY tenant_id, country, id`)
	if err != nil {
		return nil, fmt.Errorf("load catalog serviceability zones: %w", err)
	}
	defer rows.Close()
	values := []Zone{}
	for rows.Next() {
		var value Zone
		if err := rows.Scan(&value.TenantID, &value.Country, &value.ID, &value.Locality, &value.MinimumLatitude, &value.MaximumLatitude, &value.MinimumLongitude, &value.MaximumLongitude, &value.PostalCodes); err != nil {
			return nil, fmt.Errorf("scan catalog serviceability zone: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog serviceability zones: %w", err)
	}
	return values, nil
}

func (repository *PostgresRepository) Categories(ctx context.Context, tenantID, country string) ([]Category, error) {
	if !postgresScope(tenantID, country) {
		return nil, ErrInvalidRequest
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id::text, title
		FROM catalog.items
		WHERE tenant_id = $1 AND country = $2 AND kind = 'CATEGORY' AND status = 'PUBLISHED'
		ORDER BY priority, id`, tenantID, country)
	if err != nil {
		return nil, fmt.Errorf("load catalog categories: %w", err)
	}
	defer rows.Close()
	values := []Category{}
	for rows.Next() {
		var id string
		var document []byte
		if err := rows.Scan(&id, &document); err != nil {
			return nil, fmt.Errorf("scan catalog category: %w", err)
		}
		var value Category
		if err := json.Unmarshal(document, &value); err != nil || value.ID != id {
			return nil, fmt.Errorf("decode catalog category: %w", ErrInvalidRequest)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog categories: %w", err)
	}
	if len(values) > 0 {
		validated, err := NewMemoryRepository(values, nil)
		if err != nil {
			return nil, fmt.Errorf("stored catalog categories are invalid: %w", err)
		}
		return validated.Categories(ctx, tenantID, country)
	}
	return values, nil
}

func (repository *PostgresRepository) Items(ctx context.Context, tenantID, country string) ([]Item, error) {
	if !postgresScope(tenantID, country) {
		return nil, ErrInvalidRequest
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id::text, title
		FROM catalog.items
		WHERE tenant_id = $1 AND country = $2 AND kind = 'ITEM' AND status = 'PUBLISHED'
		ORDER BY priority, id`, tenantID, country)
	if err != nil {
		return nil, fmt.Errorf("load catalog items: %w", err)
	}
	values := []Item{}
	byID := map[string]int{}
	for rows.Next() {
		var id string
		var document []byte
		if err := rows.Scan(&id, &document); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan catalog item: %w", err)
		}
		var value Item
		if err := json.Unmarshal(document, &value); err != nil || value.ID != id {
			rows.Close()
			return nil, fmt.Errorf("decode catalog item: %w", ErrInvalidRequest)
		}
		value.Questions = []Question{}
		byID[id] = len(values)
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate catalog items: %w", err)
	}
	rows.Close()
	questionRows, err := repository.pool.Query(ctx, `
		SELECT question.item_id::text, question.id, question.question_text,
		       question.customer_identity_id, question.status, question.answer_text,
		       question.answered_by, question.asked_at, question.answered_at
		FROM catalog.customer_questions AS question
		JOIN catalog.items AS item ON item.id = question.item_id
		WHERE item.tenant_id = $1 AND item.country = $2 AND item.kind = 'ITEM'
		  AND item.status = 'PUBLISHED' AND question.status <> 'HIDDEN'
		ORDER BY question.asked_at, question.id`, tenantID, country)
	if err != nil {
		return nil, fmt.Errorf("load catalog questions: %w", err)
	}
	defer questionRows.Close()
	for questionRows.Next() {
		var itemID, status string
		var value Question
		var answer, answeredBy *string
		if err := questionRows.Scan(&itemID, &value.ID, &value.Question, &value.AskedByID, &status, &answer, &answeredBy, &value.AskedAt, &value.AnsweredAt); err != nil {
			return nil, fmt.Errorf("scan catalog question: %w", err)
		}
		value.AskedBy = "Planext4u customer"
		if answer != nil {
			value.Answer = *answer
		}
		if answeredBy != nil {
			value.AnsweredBy = *answeredBy
		}
		if status != "PENDING" && status != "ANSWERED" {
			return nil, fmt.Errorf("stored catalog question is invalid: %w", ErrInvalidRequest)
		}
		if index, found := byID[itemID]; found {
			values[index].Questions = append(values[index].Questions, value)
		}
	}
	if err := questionRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog questions: %w", err)
	}
	categories, err := repository.Categories(ctx, tenantID, country)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return values, nil
	}
	validated, err := NewMemoryRepository(categories, values)
	if err != nil {
		return nil, fmt.Errorf("stored catalog items are invalid: %w", err)
	}
	return validated.Items(ctx, tenantID, country)
}

func (repository *PostgresRepository) AddQuestion(ctx context.Context, tenantID, country, itemID string, question Question) (Question, error) {
	if !postgresScope(tenantID, country) || !postgresQuestion(itemID, question) {
		return Question{}, ErrInvalidRequest
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Question{}, fmt.Errorf("begin catalog question: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	lockKey := tenantID + "\x1f" + question.AskedByID + "\x1f" + itemID
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return Question{}, fmt.Errorf("lock catalog question scope: %w", err)
	}
	var published bool
	if err := transaction.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM catalog.items
			WHERE id = $1 AND tenant_id = $2 AND country = $3 AND kind = 'ITEM' AND status = 'PUBLISHED'
		)`, itemID, tenantID, country).Scan(&published); err != nil {
		return Question{}, fmt.Errorf("check catalog item: %w", err)
	}
	if !published {
		return Question{}, ErrNotFound
	}
	existing, found, err := loadPostgresQuestion(ctx, transaction, tenantID, question.ID)
	if err != nil {
		return Question{}, err
	}
	if found {
		if existing.Question != question.Question || existing.AskedByID != question.AskedByID {
			return Question{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	var pending int
	if err := transaction.QueryRow(ctx, `
		SELECT count(*) FROM catalog.customer_questions
		WHERE tenant_id = $1 AND country = $2 AND item_id = $3
		  AND customer_identity_id = $4 AND status = 'PENDING'`, tenantID, country, itemID, question.AskedByID).Scan(&pending); err != nil {
		return Question{}, fmt.Errorf("count pending catalog questions: %w", err)
	}
	if pending >= 5 {
		return Question{}, ErrQuestionLimit
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO catalog.customer_questions
			(id, tenant_id, country, item_id, customer_identity_id, idempotency_key,
			 question_text, status, asked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING', $8)`, question.ID, tenantID, country, itemID, question.AskedByID, question.ID, question.Question, question.AskedAt)
	if err != nil {
		if postgresUniqueViolation(err) {
			return Question{}, ErrIdempotencyConflict
		}
		return Question{}, fmt.Errorf("insert catalog question: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return Question{}, fmt.Errorf("commit catalog question: %w", err)
	}
	return question, nil
}

type postgresQuestionQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadPostgresQuestion(ctx context.Context, querier postgresQuestionQuerier, tenantID, questionID string) (Question, bool, error) {
	var value Question
	var status string
	var answer, answeredBy *string
	err := querier.QueryRow(ctx, `
		SELECT id, question_text, customer_identity_id, status, answer_text,
		       answered_by, asked_at, answered_at
		FROM catalog.customer_questions
		WHERE tenant_id = $1 AND id = $2`, tenantID, questionID).Scan(
		&value.ID, &value.Question, &value.AskedByID, &status, &answer,
		&answeredBy, &value.AskedAt, &value.AnsweredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Question{}, false, nil
	}
	if err != nil {
		return Question{}, false, fmt.Errorf("load catalog question replay: %w", err)
	}
	value.AskedBy = "Planext4u customer"
	if answer != nil {
		value.Answer = *answer
	}
	if answeredBy != nil {
		value.AnsweredBy = *answeredBy
	}
	if status != "PENDING" && status != "ANSWERED" {
		return Question{}, false, ErrInvalidRequest
	}
	return value, true, nil
}

func postgresScope(tenantID, country string) bool {
	_, err := uuid.Parse(tenantID)
	return err == nil && len(country) == 2 && strings.ToUpper(country) == country
}

func postgresQuestion(itemID string, value Question) bool {
	_, err := uuid.Parse(itemID)
	return err == nil && safeID(value.ID) && safeID(value.AskedByID) && strings.TrimSpace(value.Question) == value.Question && len(value.Question) >= 5 && len(value.Question) <= 500 && !value.AskedAt.IsZero() && value.Answer == "" && value.AnsweredBy == "" && value.AnsweredAt == nil
}

func postgresUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

var _ Repository = (*PostgresRepository)(nil)
