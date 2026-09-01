package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const inventoryOperationTimeout = 10 * time.Second

// PostgresService provides durable inventory reservations for horizontally
// scaled checkout workers. Stock rows are locked in canonical variant order so
// concurrent multi-line reservations cannot oversell or deadlock each other.
type PostgresService struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('inventory.stock') IS NOT NULL
		   AND to_regclass('inventory.reservations') IS NOT NULL
		   AND to_regclass('inventory.reservation_lines') IS NOT NULL
		   AND to_regclass('inventory.restock_requests') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check inventory schema readiness: %w", err)
	}
	if !ready {
		return errors.New("inventory schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Available(scope Scope, variantID string) (int, error) {
	if !postgresInventoryScope(scope) || !inventoryUUID(variantID) {
		return 0, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryOperationTimeout)
	defer cancel()
	var value int
	err := service.pool.QueryRow(ctx, `
		SELECT available_quantity FROM inventory.stock
		WHERE tenant_id=$1 AND country=$2 AND variant_id=$3`, scope.TenantID, scope.Country, variantID).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load available inventory: %w", err)
	}
	return value, nil
}

func (service *PostgresService) Get(scope Scope, reservationID string) (Reservation, error) {
	if !postgresInventoryScope(scope) || !inventoryUUID(reservationID) {
		return Reservation{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryOperationTimeout)
	defer cancel()
	value, found, err := loadPostgresReservation(ctx, service.pool, scope, reservationID, false)
	if err != nil {
		return Reservation{}, err
	}
	if !found {
		return Reservation{}, ErrReservationNotFound
	}
	return value, nil
}

func (service *PostgresService) Reserve(scope Scope, idempotencyKey, orderReference string, lines []Line, expiresAt time.Time) (Reservation, bool, error) {
	now := service.clock().UTC()
	normalized, fingerprint, err := normalizeRequest(scope, idempotencyKey, orderReference, lines, expiresAt, now)
	if err != nil || !postgresInventoryScope(scope) {
		return Reservation{}, false, ErrInvalidRequest
	}
	for _, line := range normalized {
		if !inventoryUUID(line.VariantID) {
			return Reservation{}, false, ErrInvalidRequest
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Reservation{}, false, fmt.Errorf("begin inventory reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := scope.TenantID + ":" + scope.Country + ":" + idempotencyKey
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return Reservation{}, false, fmt.Errorf("lock inventory reservation command: %w", err)
	}
	if replay, found, err := loadReservationReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return Reservation{}, false, err
	} else if found {
		return replay, true, nil
	}
	for _, line := range normalized {
		var available int
		err := tx.QueryRow(ctx, `
			SELECT available_quantity FROM inventory.stock
			WHERE tenant_id=$1 AND country=$2 AND variant_id=$3 FOR UPDATE`,
			scope.TenantID, scope.Country, line.VariantID).Scan(&available)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && available < line.Quantity) {
			return Reservation{}, false, ErrInsufficientStock
		}
		if err != nil {
			return Reservation{}, false, fmt.Errorf("lock inventory stock: %w", err)
		}
	}
	for _, line := range normalized {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory.stock
			SET available_quantity=available_quantity-$4, revision=revision+1, updated_at=$5
			WHERE tenant_id=$1 AND country=$2 AND variant_id=$3`,
			scope.TenantID, scope.Country, line.VariantID, line.Quantity, now); err != nil {
			return Reservation{}, false, fmt.Errorf("decrement inventory stock: %w", err)
		}
	}
	value := Reservation{ID: uuid.NewString(), TenantID: scope.TenantID, Country: scope.Country,
		OrderReference: orderReference, State: StateReserved, Lines: normalized,
		CreatedAt: now, ExpiresAt: expiresAt.UTC(), UpdatedAt: now}
	responsePayload, err := json.Marshal(value)
	if err != nil {
		return Reservation{}, false, fmt.Errorf("encode inventory reservation replay: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory.reservations
			(id, tenant_id, country, order_reference, state, idempotency_key,
			 request_fingerprint, response_payload, created_at, expires_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11)`,
		value.ID, value.TenantID, value.Country, value.OrderReference, value.State,
		idempotencyKey, fingerprint, responsePayload, value.CreatedAt, value.ExpiresAt, value.UpdatedAt); err != nil {
		return Reservation{}, false, fmt.Errorf("insert inventory reservation: %w", err)
	}
	for _, line := range normalized {
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory.reservation_lines (reservation_id, variant_id, quantity, restocked_quantity)
			VALUES ($1,$2,$3,0)`, value.ID, line.VariantID, line.Quantity); err != nil {
			return Reservation{}, false, fmt.Errorf("insert inventory reservation line: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, false, fmt.Errorf("commit inventory reservation: %w", err)
	}
	return value, false, nil
}

func (service *PostgresService) Commit(scope Scope, reservationID string) (Reservation, error) {
	return service.transition(scope, reservationID, StateCommitted)
}

func (service *PostgresService) Release(scope Scope, reservationID string) (Reservation, error) {
	return service.transition(scope, reservationID, StateReleased)
}

func (service *PostgresService) transition(scope Scope, reservationID string, target ReservationState) (Reservation, error) {
	if !postgresInventoryScope(scope) || !inventoryUUID(reservationID) || (target != StateCommitted && target != StateReleased) {
		return Reservation{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Reservation{}, fmt.Errorf("begin inventory transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, found, err := loadPostgresReservation(ctx, tx, scope, reservationID, true)
	if err != nil {
		return Reservation{}, err
	}
	if !found {
		return Reservation{}, ErrReservationNotFound
	}
	if value.State == target {
		return value, nil
	}
	if value.State != StateReserved {
		return Reservation{}, ErrInvalidTransition
	}
	now := service.clock().UTC()
	if !value.ExpiresAt.After(now) {
		if err := restorePostgresStock(ctx, tx, scope, value.Lines, now); err != nil {
			return Reservation{}, err
		}
		value.State, value.UpdatedAt = StateReleased, now
		if err := updateReservationState(ctx, tx, value); err != nil {
			return Reservation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Reservation{}, fmt.Errorf("commit expired inventory reservation: %w", err)
		}
		if target == StateReleased {
			return value, nil
		}
		return Reservation{}, ErrInvalidTransition
	}
	if target == StateReleased {
		if err := restorePostgresStock(ctx, tx, scope, value.Lines, now); err != nil {
			return Reservation{}, err
		}
	}
	value.State, value.UpdatedAt = target, now
	if err := updateReservationState(ctx, tx, value); err != nil {
		return Reservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, fmt.Errorf("commit inventory transition: %w", err)
	}
	return value, nil
}

func (service *PostgresService) Restock(scope Scope, idempotencyKey, reservationID string, lines []Line) (Reservation, bool, error) {
	normalized, fingerprint, err := normalizeRestock(scope, idempotencyKey, reservationID, lines)
	if err != nil || !postgresInventoryScope(scope) || !inventoryUUID(reservationID) {
		return Reservation{}, false, ErrInvalidRequest
	}
	for _, line := range normalized {
		if !inventoryUUID(line.VariantID) {
			return Reservation{}, false, ErrInvalidRequest
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Reservation{}, false, fmt.Errorf("begin inventory restock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := "restock:" + scope.TenantID + ":" + scope.Country + ":" + idempotencyKey
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return Reservation{}, false, fmt.Errorf("lock inventory restock command: %w", err)
	}
	if replay, found, err := loadRestockReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return Reservation{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, found, err := loadPostgresReservation(ctx, tx, scope, reservationID, true)
	if err != nil {
		return Reservation{}, false, err
	}
	if !found {
		return Reservation{}, false, ErrReservationNotFound
	}
	if value.State != StateCommitted {
		return Reservation{}, false, ErrInvalidTransition
	}
	original, already := quantities(value.Lines), quantities(value.RestockedLines)
	for _, line := range normalized {
		if line.Quantity > original[line.VariantID]-already[line.VariantID] {
			return Reservation{}, false, ErrInvalidTransition
		}
	}
	now := service.clock().UTC()
	if err := restorePostgresStock(ctx, tx, scope, normalized, now); err != nil {
		return Reservation{}, false, err
	}
	for _, line := range normalized {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory.reservation_lines
			SET restocked_quantity=restocked_quantity+$3
			WHERE reservation_id=$1 AND variant_id=$2`, reservationID, line.VariantID, line.Quantity); err != nil {
			return Reservation{}, false, fmt.Errorf("record inventory restock line: %w", err)
		}
	}
	value.RestockedLines = mergeLines(value.RestockedLines, normalized)
	value.UpdatedAt = now
	if _, err := tx.Exec(ctx, `UPDATE inventory.reservations SET updated_at=$2 WHERE id=$1`, value.ID, now); err != nil {
		return Reservation{}, false, fmt.Errorf("update inventory restock: %w", err)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return Reservation{}, false, fmt.Errorf("encode inventory restock replay: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory.restock_requests
			(tenant_id,country,idempotency_key,reservation_id,request_fingerprint,response_payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`,
		scope.TenantID, scope.Country, idempotencyKey, reservationID, fingerprint, payload, now); err != nil {
		return Reservation{}, false, fmt.Errorf("store inventory restock replay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, false, fmt.Errorf("commit inventory restock: %w", err)
	}
	return value, false, nil
}

// Expire releases every due reservation. Workers use SKIP LOCKED batches so
// multiple replicas can run the sweeper safely.
func (service *PostgresService) Expire() int {
	total := 0
	for {
		count, err := service.expireBatch(500)
		if err != nil {
			return total
		}
		total += count
		if count < 500 {
			return total
		}
	}
}

func (service *PostgresService) expireBatch(limit int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), inventoryOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin inventory expiry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.clock().UTC()
	rows, err := tx.Query(ctx, `
		SELECT id::text, tenant_id::text, country
		FROM inventory.reservations
		WHERE state='RESERVED' AND expires_at <= $1
		ORDER BY expires_at, id FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("claim expired inventory reservations: %w", err)
	}
	type expiredReservation struct{ id, tenant, country string }
	claimed := []expiredReservation{}
	for rows.Next() {
		var value expiredReservation
		if err := rows.Scan(&value.id, &value.tenant, &value.country); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired inventory reservation: %w", err)
		}
		claimed = append(claimed, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate expired inventory reservations: %w", err)
	}
	rows.Close()
	for _, claimedValue := range claimed {
		scope := Scope{TenantID: claimedValue.tenant, Country: claimedValue.country}
		value, found, err := loadPostgresReservation(ctx, tx, scope, claimedValue.id, false)
		if err != nil || !found {
			if err == nil {
				err = ErrReservationNotFound
			}
			return 0, err
		}
		if err := restorePostgresStock(ctx, tx, scope, value.Lines, now); err != nil {
			return 0, err
		}
		value.State, value.UpdatedAt = StateReleased, now
		if err := updateReservationState(ctx, tx, value); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit inventory expiry: %w", err)
	}
	return len(claimed), nil
}

type inventoryQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadPostgresReservation(ctx context.Context, querier inventoryQuerier, scope Scope, reservationID string, lock bool) (Reservation, bool, error) {
	query := `SELECT id::text, tenant_id::text, country, order_reference, state, created_at, expires_at, updated_at
		FROM inventory.reservations WHERE id=$1 AND tenant_id=$2 AND country=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	var value Reservation
	err := querier.QueryRow(ctx, query, reservationID, scope.TenantID, scope.Country).Scan(
		&value.ID, &value.TenantID, &value.Country, &value.OrderReference, &value.State,
		&value.CreatedAt, &value.ExpiresAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, fmt.Errorf("load inventory reservation: %w", err)
	}
	rows, err := querier.Query(ctx, `
		SELECT variant_id::text, quantity, restocked_quantity
		FROM inventory.reservation_lines WHERE reservation_id=$1 ORDER BY variant_id`, value.ID)
	if err != nil {
		return Reservation{}, false, fmt.Errorf("load inventory reservation lines: %w", err)
	}
	defer rows.Close()
	value.Lines, value.RestockedLines = []Line{}, []Line{}
	for rows.Next() {
		var id string
		var quantity, restocked int
		if err := rows.Scan(&id, &quantity, &restocked); err != nil {
			return Reservation{}, false, fmt.Errorf("scan inventory reservation line: %w", err)
		}
		value.Lines = append(value.Lines, Line{VariantID: id, Quantity: quantity})
		if restocked > 0 {
			value.RestockedLines = append(value.RestockedLines, Line{VariantID: id, Quantity: restocked})
		}
	}
	if err := rows.Err(); err != nil {
		return Reservation{}, false, fmt.Errorf("iterate inventory reservation lines: %w", err)
	}
	return value, true, nil
}

func loadReservationReplay(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string) (Reservation, bool, error) {
	var reservationID, storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT id::text, request_fingerprint, response_payload FROM inventory.reservations
		WHERE tenant_id=$1 AND country=$2 AND idempotency_key=$3`, scope.TenantID, scope.Country, key).Scan(&reservationID, &storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, fmt.Errorf("load inventory reservation replay: %w", err)
	}
	if storedFingerprint != fingerprint {
		return Reservation{}, false, ErrIdempotencyConflict
	}
	if len(payload) > 0 {
		var value Reservation
		if err := json.Unmarshal(payload, &value); err != nil {
			return Reservation{}, false, fmt.Errorf("decode inventory reservation replay: %w", err)
		}
		return value, true, nil
	}
	value, found, err := loadPostgresReservation(ctx, tx, scope, reservationID, false)
	return value, found, err
}

func loadRestockReplay(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string) (Reservation, bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT request_fingerprint, response_payload FROM inventory.restock_requests
		WHERE tenant_id=$1 AND country=$2 AND idempotency_key=$3`, scope.TenantID, scope.Country, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, fmt.Errorf("load inventory restock replay: %w", err)
	}
	if storedFingerprint != fingerprint {
		return Reservation{}, false, ErrIdempotencyConflict
	}
	var value Reservation
	if err := json.Unmarshal(payload, &value); err != nil {
		return Reservation{}, false, fmt.Errorf("decode inventory restock replay: %w", err)
	}
	return value, true, nil
}

func restorePostgresStock(ctx context.Context, tx pgx.Tx, scope Scope, lines []Line, now time.Time) error {
	for _, line := range lines {
		command, err := tx.Exec(ctx, `
			UPDATE inventory.stock SET available_quantity=available_quantity+$4,
				revision=revision+1, updated_at=$5
			WHERE tenant_id=$1 AND country=$2 AND variant_id=$3`,
			scope.TenantID, scope.Country, line.VariantID, line.Quantity, now)
		if err != nil {
			return fmt.Errorf("restore inventory stock: %w", err)
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("restore inventory stock: %w", ErrInvalidTransition)
		}
	}
	return nil
}

func updateReservationState(ctx context.Context, tx pgx.Tx, value Reservation) error {
	if _, err := tx.Exec(ctx, `UPDATE inventory.reservations SET state=$2, updated_at=$3 WHERE id=$1`, value.ID, value.State, value.UpdatedAt); err != nil {
		return fmt.Errorf("update inventory reservation: %w", err)
	}
	return nil
}

func postgresInventoryScope(scope Scope) bool {
	return inventoryUUID(scope.TenantID) && len(scope.Country) == 2 && scope.Country[0] >= 'A' && scope.Country[0] <= 'Z' && scope.Country[1] >= 'A' && scope.Country[1] <= 'Z'
}

func inventoryUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
