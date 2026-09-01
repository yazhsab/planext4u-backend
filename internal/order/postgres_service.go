package order

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const orderOperationTimeout = 10 * time.Second

// PostgresService persists the complete order aggregate and its normalized
// lifecycle projections in one transaction. The aggregate snapshot makes
// replay exact while the projections keep operations and reporting queryable.
type PostgresService struct {
	pool     *pgxpool.Pool
	clock    func() time.Time
	notifier Notifier
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time) (*PostgresService, error) {
	return NewPostgresServiceWithNotifier(pool, clock, nil)
}

func NewPostgresServiceWithNotifier(pool *pgxpool.Pool, clock func() time.Time, notifier Notifier) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock, notifier: notifier}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('ordering.orders') IS NOT NULL
		   AND to_regclass('ordering.idempotency_records') IS NOT NULL
		   AND to_regclass('ordering.notification_intents') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check ordering schema readiness: %w", err)
	}
	if !ready {
		return errors.New("ordering schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Create(scope Scope, idempotencyKey string, snapshot CheckoutSnapshot, paymentCaptured bool) (Order, bool, error) {
	if !postgresOrderScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !validSnapshot(snapshot) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := orderFingerprint(snapshotFingerprint(snapshot, paymentCaptured))
	ctx, cancel := context.WithTimeout(context.Background(), orderOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Order{}, false, fmt.Errorf("begin order creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockOrderCommand(ctx, tx, scope, idempotencyKey); err != nil {
		return Order{}, false, err
	}
	if replay, found, err := loadOrderReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return Order{}, false, err
	} else if found {
		return replay, true, nil
	}
	now := service.clock().UTC()
	status := StatusPendingPayment
	if paymentCaptured || snapshot.PaymentMethod == "COD" {
		status = StatusPlaced
	}
	value := Order{ID: uuid.NewString(), Revision: 1, Status: status, Snapshot: cloneSnapshot(snapshot),
		AllowedActions: allowedActions(status, nil, nil), Timeline: []TimelineEvent{{Status: status, Actor: "PLATFORM", CreatedAt: now}},
		CreatedAt: now, UpdatedAt: now, scope: scope}
	if err := persistPostgresOrder(ctx, tx, value, true); err != nil {
		return Order{}, false, err
	}
	if err := storeOrderReplay(ctx, tx, scope, idempotencyKey, fingerprint, value, now); err != nil {
		return Order{}, false, err
	}
	if err := queuePostgresOrderNotification(ctx, tx, value, now); err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, fmt.Errorf("commit order creation: %w", err)
	}
	return value, false, nil
}

func (service *PostgresService) Get(scope Scope, orderID string) (Order, error) {
	if !postgresOrderScope(scope) || !orderUUID(orderID) {
		return Order{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), orderOperationTimeout)
	defer cancel()
	value, found, err := loadPostgresOrder(ctx, service.pool, scope, orderID, false)
	if err != nil {
		return Order{}, err
	}
	if !found {
		return Order{}, ErrOrderNotFound
	}
	return value, nil
}

func (service *PostgresService) List(scope Scope) ([]Order, error) {
	if !postgresOrderScope(scope) {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), orderOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `
		SELECT state_snapshot FROM ordering.orders
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3
		ORDER BY created_at DESC`, scope.TenantID, scope.Country, scope.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()
	values := []Order{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan order list: %w", err)
		}
		var value Order
		if len(payload) == 0 || json.Unmarshal(payload, &value) != nil {
			return nil, fmt.Errorf("decode stored order: %w", ErrInvalidRequest)
		}
		value.scope = scope
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order list: %w", err)
	}
	return values, nil
}

func (service *PostgresService) Transition(scope Scope, idempotencyKey, orderID string, expectedRevision int64, target Status, actor, reason string) (Order, bool, error) {
	if !postgresOrderScope(scope) || !validCommand(idempotencyKey, orderID, actor) || !orderUUID(orderID) || expectedRevision < 1 || (reason != "" && (len(reason) > 500 || strings.TrimSpace(reason) != reason)) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("transition\x00%s\x00%d\x00%s\x00%s\x00%s", orderID, expectedRevision, target, actor, reason)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if !canTransition(value.Status, target, actor) {
			return ErrInvalidTransition
		}
		value.Status = target
		value.Timeline = append(value.Timeline, TimelineEvent{Status: target, Actor: actor, Reason: reason, CreatedAt: service.clock().UTC()})
		return nil
	})
}

func (service *PostgresService) RequestReturn(scope Scope, idempotencyKey, orderID string, expectedRevision int64, lines []ReturnLine, reason string) (Order, bool, error) {
	normalized, err := normalizeReturn(lines)
	if err != nil || !safeReason(reason) || !orderUUID(orderID) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("return\x00%s\x00%d\x00%v\x00%s", orderID, expectedRevision, normalized, reason)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusCompleted && value.Status != StatusDeliveredPendingConfirmation {
			return ErrInvalidTransition
		}
		refund, err := refundable(value.Snapshot, normalized)
		if err != nil {
			return err
		}
		now := service.clock().UTC()
		value.Return = &ReturnCase{ID: uuid.NewString(), Status: StatusReturnRequested, Lines: normalized, Reason: reason, RefundAmount: refund, CreatedAt: now, UpdatedAt: now}
		value.Status = StatusReturnRequested
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: "CUSTOMER", Reason: reason, CreatedAt: now})
		return nil
	})
}

func (service *PostgresService) DecideReturn(scope Scope, idempotencyKey, orderID string, expectedRevision int64, approved bool, reason string) (Order, bool, error) {
	target := StatusReturnRejected
	if approved {
		target = StatusReturnApproved
	}
	fingerprint := fmt.Sprintf("return-decision\x00%s\x00%d\x00%t\x00%s", orderID, expectedRevision, approved, reason)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusReturnRequested || value.Return == nil {
			return ErrInvalidTransition
		}
		now := service.clock().UTC()
		value.Status, value.Return.Status, value.Return.UpdatedAt = target, target, now
		value.Timeline = append(value.Timeline, TimelineEvent{Status: target, Actor: "ADMIN", Reason: reason, CreatedAt: now})
		return nil
	})
}

func (service *PostgresService) RecordPOD(scope Scope, idempotencyKey, orderID string, expectedRevision int64, proof Proof) (Order, bool, error) {
	if !validProof(proof) || (proof.PhotoAssetID != "" && !orderUUID(proof.PhotoAssetID)) {
		return Order{}, false, ErrProofRequired
	}
	fingerprint := fmt.Sprintf("pod\x00%s\x00%d\x00%+v", orderID, expectedRevision, proof)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusOutForDelivery {
			return ErrInvalidTransition
		}
		now := service.clock().UTC()
		copy := proof
		value.Proof, value.Status = &copy, StatusDeliveredPendingConfirmation
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: "RIDER", CreatedAt: now})
		return nil
	})
}

func (service *PostgresService) RecordRefund(scope Scope, idempotencyKey, orderID string, expectedRevision int64, refundReference string, amount Money) (Order, bool, error) {
	if !safeID(refundReference) || !validMoney(amount) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("refund\x00%s\x00%d\x00%s\x00%d", orderID, expectedRevision, refundReference, amount.AmountMinor)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Return == nil || value.Status != StatusReturned || amount != value.Return.RefundAmount {
			return ErrInvalidTransition
		}
		now := service.clock().UTC()
		value.Return.RefundReference, value.Return.Status, value.Return.UpdatedAt = refundReference, StatusRefunded, now
		value.Status = StatusRefunded
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: "PLATFORM", CreatedAt: now})
		return nil
	})
}

func (service *PostgresService) Rate(scope Scope, idempotencyKey, orderID string, expectedRevision int64, score int, comment string) (Order, bool, error) {
	if score < 1 || score > 5 || len(comment) > 1000 || strings.TrimSpace(comment) != comment {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("rating\x00%s\x00%d\x00%d\x00%s", orderID, expectedRevision, score, comment)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusCompleted || value.Rating != nil {
			return ErrInvalidTransition
		}
		value.Rating = &Rating{Score: score, Comment: comment, CreatedAt: service.clock().UTC()}
		return nil
	})
}

func (service *PostgresService) mutate(scope Scope, idempotencyKey, rawFingerprint, orderID string, expectedRevision int64, apply func(*Order) error) (Order, bool, error) {
	if !postgresOrderScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !orderUUID(orderID) || expectedRevision < 1 || apply == nil {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := orderFingerprint(rawFingerprint)
	ctx, cancel := context.WithTimeout(context.Background(), orderOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Order{}, false, fmt.Errorf("begin order mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockOrderCommand(ctx, tx, scope, idempotencyKey); err != nil {
		return Order{}, false, err
	}
	if replay, found, err := loadOrderReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return Order{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, found, err := loadPostgresOrder(ctx, tx, scope, orderID, true)
	if err != nil {
		return Order{}, false, err
	}
	if !found {
		return Order{}, false, ErrOrderNotFound
	}
	if value.Revision != expectedRevision {
		return Order{}, false, ErrRevisionConflict
	}
	if err := apply(&value); err != nil {
		return Order{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	value.AllowedActions = allowedActions(value.Status, value.Return, value.Rating)
	if err := persistPostgresOrder(ctx, tx, value, false); err != nil {
		return Order{}, false, err
	}
	if err := storeOrderReplay(ctx, tx, scope, idempotencyKey, fingerprint, value, value.UpdatedAt); err != nil {
		return Order{}, false, err
	}
	if err := queuePostgresOrderNotification(ctx, tx, value, value.UpdatedAt); err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, fmt.Errorf("commit order mutation: %w", err)
	}
	return value, false, nil
}

func (service *PostgresService) ProcessNotifications(ctx context.Context, limit int) (int, error) {
	if service.notifier == nil || ctx == nil || limit < 1 || limit > 100 {
		return 0, ErrInvalidRequest
	}
	delivered := 0
	for index := 0; index < limit; index++ {
		value, found, err := service.claimNotification(ctx)
		if err != nil {
			return delivered, err
		}
		if !found {
			break
		}
		err = service.notifier.Send(ctx, value)
		now := service.clock().UTC()
		if err != nil {
			delay := time.Second << min(value.Attempts, 12)
			if delay > time.Hour {
				delay = time.Hour
			}
			_, updateErr := service.pool.Exec(ctx, `
				UPDATE ordering.notification_intents
				SET claim_until=NULL, next_attempt_at=$2, last_error='NOTIFICATION_PROVIDER_FAILED'
				WHERE id=$1 AND delivered_at IS NULL`, value.ID, now.Add(delay))
			if updateErr != nil {
				return delivered, fmt.Errorf("release order notification: %w", updateErr)
			}
			continue
		}
		if _, err := service.pool.Exec(ctx, `
			UPDATE ordering.notification_intents
			SET delivered_at=$2, claim_until=NULL, last_error=NULL
			WHERE id=$1 AND delivered_at IS NULL`, value.ID, now); err != nil {
			return delivered, fmt.Errorf("complete order notification: %w", err)
		}
		delivered++
	}
	return delivered, nil
}

func (service *PostgresService) claimNotification(ctx context.Context) (Notification, bool, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Notification{}, false, fmt.Errorf("begin order notification claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.clock().UTC()
	var value Notification
	err = tx.QueryRow(ctx, `
		SELECT id::text,tenant_id::text,country,customer_identity_id::text,
		       order_id::text,status,order_revision,attempts,delivered_at,
		       COALESCE(last_error,''),created_at
		FROM ordering.notification_intents
		WHERE delivered_at IS NULL AND next_attempt_at <= $1
		  AND (claim_until IS NULL OR claim_until <= $1)
		ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(
		&value.ID, &value.TenantID, &value.Country, &value.CustomerID, &value.OrderID,
		&value.Status, &value.Revision, &value.Attempts, &value.DeliveredAt,
		&value.LastError, &value.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Notification{}, false, nil
	}
	if err != nil {
		return Notification{}, false, fmt.Errorf("claim order notification: %w", err)
	}
	value.Attempts++
	if _, err := tx.Exec(ctx, `UPDATE ordering.notification_intents SET attempts=$2,claim_until=$3 WHERE id=$1`, value.ID, value.Attempts, now.Add(time.Minute)); err != nil {
		return Notification{}, false, fmt.Errorf("lease order notification: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Notification{}, false, fmt.Errorf("commit order notification claim: %w", err)
	}
	return value, true, nil
}

func (service *PostgresService) PendingNotifications(scope Scope) ([]Notification, error) {
	if !postgresOrderScope(scope) {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), orderOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `
		SELECT id::text,tenant_id::text,country,customer_identity_id::text,
		       order_id::text,status,order_revision,attempts,delivered_at,
		       COALESCE(last_error,''),created_at
		FROM ordering.notification_intents
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND delivered_at IS NULL
		ORDER BY created_at,id`, scope.TenantID, scope.Country, scope.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("list pending order notifications: %w", err)
	}
	defer rows.Close()
	values := []Notification{}
	for rows.Next() {
		var value Notification
		if err := rows.Scan(&value.ID, &value.TenantID, &value.Country, &value.CustomerID,
			&value.OrderID, &value.Status, &value.Revision, &value.Attempts,
			&value.DeliveredAt, &value.LastError, &value.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending order notification: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type orderQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadPostgresOrder(ctx context.Context, querier orderQuerier, scope Scope, orderID string, lock bool) (Order, bool, error) {
	query := `SELECT state_snapshot FROM ordering.orders WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4`
	if lock {
		query += ` FOR UPDATE`
	}
	var payload []byte
	err := querier.QueryRow(ctx, query, orderID, scope.TenantID, scope.Country, scope.CustomerID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, fmt.Errorf("load order: %w", err)
	}
	var value Order
	if len(payload) == 0 || json.Unmarshal(payload, &value) != nil {
		return Order{}, false, fmt.Errorf("decode stored order: %w", ErrInvalidRequest)
	}
	value.scope = scope
	return value, true, nil
}

func persistPostgresOrder(ctx context.Context, tx pgx.Tx, value Order, create bool) error {
	statePayload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode order state: %w", err)
	}
	checkoutPayload, err := json.Marshal(value.Snapshot)
	if err != nil {
		return fmt.Errorf("encode checkout snapshot: %w", err)
	}
	if create {
		_, err = tx.Exec(ctx, `
			INSERT INTO ordering.orders
				(id,tenant_id,country,customer_identity_id,revision,status,checkout_snapshot,
				 allowed_actions,created_at,updated_at,state_snapshot)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11::jsonb)`,
			value.ID, value.scope.TenantID, value.scope.Country, value.scope.CustomerID,
			value.Revision, value.Status, checkoutPayload, value.AllowedActions,
			value.CreatedAt, value.UpdatedAt, statePayload)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE ordering.orders SET revision=$2,status=$3,allowed_actions=$4,
				updated_at=$5,state_snapshot=$6::jsonb WHERE id=$1`,
			value.ID, value.Revision, value.Status, value.AllowedActions, value.UpdatedAt, statePayload)
	}
	if err != nil {
		return fmt.Errorf("persist order: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ordering.timeline_events WHERE order_id=$1`, value.ID); err != nil {
		return fmt.Errorf("replace order timeline: %w", err)
	}
	for _, event := range value.Timeline {
		if _, err := tx.Exec(ctx, `INSERT INTO ordering.timeline_events (order_id,status,actor,reason,created_at) VALUES ($1,$2,$3,NULLIF($4,''),$5)`, value.ID, event.Status, event.Actor, event.Reason, event.CreatedAt); err != nil {
			return fmt.Errorf("persist order timeline: %w", err)
		}
	}
	if value.Proof == nil {
		_, err = tx.Exec(ctx, `DELETE FROM ordering.delivery_proofs WHERE order_id=$1`, value.ID)
	} else {
		var photo any
		if value.Proof.PhotoAssetID != "" {
			photo = value.Proof.PhotoAssetID
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ordering.delivery_proofs (order_id,policy_version,photo_asset_id,recipient_name,otp_verified,signed_at)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,$6)
			ON CONFLICT (order_id) DO UPDATE SET policy_version=EXCLUDED.policy_version,
			photo_asset_id=EXCLUDED.photo_asset_id,recipient_name=EXCLUDED.recipient_name,
			otp_verified=EXCLUDED.otp_verified,signed_at=EXCLUDED.signed_at`,
			value.ID, value.Proof.PolicyVersion, photo, value.Proof.RecipientName, value.Proof.OTPVerified, nullableTime(value.Proof.SignedAt))
	}
	if err != nil {
		return fmt.Errorf("persist order proof: %w", err)
	}
	if value.Return != nil {
		lines, marshalErr := json.Marshal(value.Return.Lines)
		if marshalErr != nil {
			return fmt.Errorf("encode order return: %w", marshalErr)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ordering.return_cases
				(id,order_id,status,lines,reason,refund_amount_minor,currency,refund_reference,created_at,updated_at)
			VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,NULLIF($8,''),$9,$10)
			ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status,lines=EXCLUDED.lines,
				reason=EXCLUDED.reason,refund_amount_minor=EXCLUDED.refund_amount_minor,
				currency=EXCLUDED.currency,refund_reference=EXCLUDED.refund_reference,updated_at=EXCLUDED.updated_at`,
			value.Return.ID, value.ID, value.Return.Status, lines, value.Return.Reason,
			value.Return.RefundAmount.AmountMinor, value.Return.RefundAmount.Currency,
			value.Return.RefundReference, value.Return.CreatedAt, value.Return.UpdatedAt)
		if err != nil {
			return fmt.Errorf("persist order return: %w", err)
		}
	}
	if value.Rating != nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO ordering.ratings (order_id,score,comment,created_at)
			VALUES ($1,$2,NULLIF($3,''),$4)
			ON CONFLICT (order_id) DO UPDATE SET score=EXCLUDED.score,comment=EXCLUDED.comment`,
			value.ID, value.Rating.Score, value.Rating.Comment, value.Rating.CreatedAt)
		if err != nil {
			return fmt.Errorf("persist order rating: %w", err)
		}
	}
	return nil
}

func lockOrderCommand(ctx context.Context, tx pgx.Tx, scope Scope, key string) error {
	lockKey := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID + ":" + key
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return fmt.Errorf("lock order command: %w", err)
	}
	return nil
}

func loadOrderReplay(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string) (Order, bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT request_fingerprint,response_payload FROM ordering.idempotency_records
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND idempotency_key=$4`,
		scope.TenantID, scope.Country, scope.CustomerID, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, fmt.Errorf("load order replay: %w", err)
	}
	if storedFingerprint != fingerprint {
		return Order{}, false, ErrIdempotencyConflict
	}
	var value Order
	if json.Unmarshal(payload, &value) != nil {
		return Order{}, false, fmt.Errorf("decode order replay: %w", ErrInvalidRequest)
	}
	value.scope = scope
	return value, true, nil
}

func storeOrderReplay(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string, value Order, now time.Time) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode order replay: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ordering.idempotency_records
			(tenant_id,country,customer_identity_id,idempotency_key,request_fingerprint,response_payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`,
		scope.TenantID, scope.Country, scope.CustomerID, key, fingerprint, payload, now); err != nil {
		return fmt.Errorf("store order replay: %w", err)
	}
	return nil
}

func queuePostgresOrderNotification(ctx context.Context, tx pgx.Tx, value Order, now time.Time) error {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%d", value.ID, value.Revision))).String()
	if _, err := tx.Exec(ctx, `
		INSERT INTO ordering.notification_intents
			(id,tenant_id,country,customer_identity_id,order_id,status,order_revision,next_attempt_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT (order_id,order_revision) DO NOTHING`,
		id, value.scope.TenantID, value.scope.Country, value.scope.CustomerID,
		value.ID, value.Status, value.Revision, now); err != nil {
		return fmt.Errorf("queue order notification: %w", err)
	}
	return nil
}

func orderFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func postgresOrderScope(scope Scope) bool {
	return orderUUID(scope.TenantID) && orderUUID(scope.CustomerID) && len(scope.Country) == 2 && scope.Country == strings.ToUpper(scope.Country)
}

func orderUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
