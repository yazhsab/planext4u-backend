package order

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrVendorWorkCancellationClaimLost = errors.New("vendor work cancellation claim was lost")

// VendorWorkCancellationIntent is the immutable ordering-owned revocation of
// a vendor work source. It is emitted even when payment was never captured.
// The supply owner uses SourceWorkIntentID to serialize against work ingestion.
type VendorWorkCancellationIntent struct {
	ID                 string    `json:"id"`
	SourceWorkIntentID string    `json:"source_work_intent_id"`
	TenantID           string    `json:"tenant_id"`
	Country            string    `json:"country"`
	CustomerID         string    `json:"customer_id"`
	OrderID            string    `json:"order_id"`
	VendorID           string    `json:"vendor_id"`
	OrderRevision      int64     `json:"order_revision"`
	OrderStatus        Status    `json:"order_status"`
	Attempts           int       `json:"attempts"`
	ClaimToken         string    `json:"claim_token"`
	ClaimUntil         time.Time `json:"claim_until"`
	CreatedAt          time.Time `json:"created_at"`
}

func vendorWorkSourceID(orderID, vendorID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+orderID+":"+vendorID)).String()
}

func vendorWorkCancellationID(orderID, vendorID string, revision int64, status Status) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("order-vendor-cancel:%s:%s:%d:%s", orderID, vendorID, revision, status))).String()
}

func queueVendorWorkCancellations(ctx context.Context, tx pgx.Tx, value Order, now time.Time) error {
	if value.Status != StatusCancelRequested && value.Status != StatusCancelled {
		return ErrInvalidRequest
	}
	vendorSet := make(map[string]struct{})
	for _, line := range value.Snapshot.Lines {
		vendorSet[line.VendorID] = struct{}{}
	}
	vendors := make([]string, 0, len(vendorSet))
	for vendorID := range vendorSet {
		vendors = append(vendors, vendorID)
	}
	sort.Strings(vendors)
	for _, vendorID := range vendors {
		_, err := tx.Exec(ctx, `
			INSERT INTO ordering.vendor_work_cancellation_intents
				(id,source_work_intent_id,tenant_id,country,customer_identity_id,
				 order_id,vendor_id,order_revision,order_status,next_attempt_at,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$10)`,
			vendorWorkCancellationID(value.ID, vendorID, value.Revision, value.Status),
			vendorWorkSourceID(value.ID, vendorID), value.scope.TenantID, value.scope.Country,
			value.scope.CustomerID, value.ID, vendorID, value.Revision, value.Status, now)
		if err != nil {
			return fmt.Errorf("queue vendor work cancellation: %w", err)
		}
	}
	return nil
}

// ClaimVendorWorkCancellations grants one-minute leases with bounded attempts.
// A worker must deliver these before any placement claims from the same order.
func (service *PostgresService) ClaimVendorWorkCancellations(ctx context.Context, limit int) ([]VendorWorkCancellationIntent, error) {
	if ctx == nil || limit < 1 || limit > maxVendorWorkClaimBatch {
		return nil, ErrInvalidRequest
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin vendor cancellation claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.clock().UTC()
	if _, err := tx.Exec(ctx, `
		WITH exhausted AS (
			SELECT id FROM ordering.vendor_work_cancellation_intents
			WHERE status='PENDING' AND attempts >= $2
			  AND (claim_until IS NULL OR claim_until <= $1)
			ORDER BY next_attempt_at,created_at,id
			LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE ordering.vendor_work_cancellation_intents AS intent
		SET status='DEAD_LETTER',dead_letter_at=$1,claim_token=NULL,claim_until=NULL,
		    last_error_code=COALESCE(last_error_code,'CLAIM_EXHAUSTED'),updated_at=$1
		FROM exhausted WHERE intent.id=exhausted.id`, now, maxVendorWorkAttempts, limit); err != nil {
		return nil, fmt.Errorf("expire vendor cancellation claims: %w", err)
	}
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM ordering.vendor_work_cancellation_intents
			WHERE status='PENDING' AND attempts < $2 AND next_attempt_at <= $1
			  AND (claim_until IS NULL OR claim_until <= $1)
			ORDER BY next_attempt_at,created_at,id
			LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE ordering.vendor_work_cancellation_intents AS intent
		SET attempts=intent.attempts+1,claim_token=gen_random_uuid(),
		    claim_until=$4,updated_at=$1
		FROM due WHERE intent.id=due.id
		RETURNING intent.id::text,intent.source_work_intent_id::text,
		          intent.tenant_id::text,intent.country,intent.customer_identity_id::text,
		          intent.order_id::text,intent.vendor_id,intent.order_revision,
		          intent.order_status,intent.attempts,intent.claim_token::text,
		          intent.claim_until,intent.created_at`,
		now, maxVendorWorkAttempts, limit, now.Add(vendorWorkLease))
	if err != nil {
		return nil, fmt.Errorf("claim vendor cancellations: %w", err)
	}
	values := make([]VendorWorkCancellationIntent, 0, limit)
	for rows.Next() {
		var value VendorWorkCancellationIntent
		if err := rows.Scan(&value.ID, &value.SourceWorkIntentID, &value.TenantID,
			&value.Country, &value.CustomerID, &value.OrderID, &value.VendorID,
			&value.OrderRevision, &value.OrderStatus, &value.Attempts,
			&value.ClaimToken, &value.ClaimUntil, &value.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan vendor cancellation claim: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate vendor cancellation claims: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit vendor cancellation claims: %w", err)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values, nil
}

func (service *PostgresService) AckVendorWorkCancellation(ctx context.Context, intentID, claimToken string) error {
	if ctx == nil || !orderUUID(intentID) || !orderUUID(claimToken) {
		return ErrInvalidRequest
	}
	now := service.clock().UTC()
	command, err := service.pool.Exec(ctx, `
		UPDATE ordering.vendor_work_cancellation_intents
		SET status='DELIVERED',delivered_at=$3,claim_token=NULL,claim_until=NULL,
		    last_error_code=NULL,updated_at=$3
		WHERE id=$1 AND claim_token=$2 AND status='PENDING'`, intentID, claimToken, now)
	if err != nil {
		return fmt.Errorf("acknowledge vendor cancellation: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrVendorWorkCancellationClaimLost
	}
	return nil
}

func (service *PostgresService) RetryVendorWorkCancellation(ctx context.Context, intentID, claimToken, errorCode string) error {
	if ctx == nil || !orderUUID(intentID) || !orderUUID(claimToken) || !validVendorWorkErrorCode(errorCode) {
		return ErrInvalidRequest
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin vendor cancellation retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT attempts FROM ordering.vendor_work_cancellation_intents
		WHERE id=$1 AND claim_token=$2 AND status='PENDING' FOR UPDATE`, intentID, claimToken).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVendorWorkCancellationClaimLost
	}
	if err != nil {
		return fmt.Errorf("load vendor cancellation retry: %w", err)
	}
	now := service.clock().UTC()
	status, nextAttempt, deadLetter := "PENDING", now.Add(vendorWorkRetryDelay(attempts)), any(nil)
	if attempts >= maxVendorWorkAttempts {
		status, deadLetter = "DEAD_LETTER", now
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ordering.vendor_work_cancellation_intents
		SET status=$2,next_attempt_at=$3,claim_token=NULL,claim_until=NULL,
		    dead_letter_at=$4,last_error_code=$5,updated_at=$6
		WHERE id=$1`, intentID, status, nextAttempt, deadLetter, errorCode, now); err != nil {
		return fmt.Errorf("release vendor cancellation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit vendor cancellation retry: %w", err)
	}
	return nil
}
