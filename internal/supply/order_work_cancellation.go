package supply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/order"
)

var ErrOrderWorkCancelled = errors.New("order vendor work was cancelled")

// OrderWorkCancellation is the immutable ordering event consumed by supply.
// It has no lease metadata, so a retry always has the same fingerprint.
type OrderWorkCancellation struct {
	ID                 string       `json:"id"`
	SourceWorkIntentID string       `json:"source_work_intent_id"`
	TenantID           string       `json:"tenant_id"`
	Country            string       `json:"country"`
	CustomerID         string       `json:"customer_id"`
	OrderID            string       `json:"order_id"`
	VendorID           string       `json:"vendor_id"`
	OrderRevision      int64        `json:"order_revision"`
	OrderStatus        order.Status `json:"order_status"`
}

// CancellationOutcome is a reconciliation result, not an order-status decision.
// ALREADY_COMPLETED requires investigation or compensation; it is never
// reported as successfully cancelled vendor work.
type CancellationOutcome struct {
	WorkID   string `json:"work_id,omitempty"`
	Outcome  string `json:"outcome"`
	Replayed bool   `json:"replayed"`
}

func validOrderWorkCancellation(value OrderWorkCancellation) bool {
	if !canonicalWorkUUID(value.ID) || !canonicalWorkUUID(value.SourceWorkIntentID) ||
		!canonicalWorkUUID(value.TenantID) || !canonicalWorkUUID(value.CustomerID) ||
		!canonicalWorkUUID(value.OrderID) || !canonicalWorkUUID(value.VendorID) ||
		len(value.Country) != 2 || value.OrderRevision < 1 ||
		(value.OrderStatus != order.StatusCancelRequested && value.OrderStatus != order.StatusCancelled) {
		return false
	}
	for _, character := range value.Country {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	expectedWork := uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+value.OrderID+":"+value.VendorID)).String()
	expectedCancel := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("order-vendor-cancel:%s:%s:%d:%s", value.OrderID, value.VendorID, value.OrderRevision, value.OrderStatus))).String()
	return value.SourceWorkIntentID == expectedWork && value.ID == expectedCancel
}

func (service *PostgresService) OrderWorkCancellationReady(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('supply.order_work_cancellations') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		               WHERE n.nspname='supply' AND p.proname='ingest_order_work_cancellation')`).Scan(&ready); err != nil {
		return fmt.Errorf("check order work cancellation readiness: %w", err)
	}
	if !ready {
		return errors.New("order work cancellation ingress is unavailable")
	}
	return nil
}

// IngestOrderWorkCancellation commits a tombstone or a terminal work change
// through the supply owner. Ordering may acknowledge only after this returns.
func (service *PostgresService) IngestOrderWorkCancellation(parent context.Context, value OrderWorkCancellation) (CancellationOutcome, error) {
	if parent == nil || !validOrderWorkCancellation(value) {
		return CancellationOutcome{}, ErrInvalidRequest
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return CancellationOutcome{}, fmt.Errorf("encode order work cancellation: %w", err)
	}
	digest := sha256.Sum256(payload)
	ctx, cancel := context.WithTimeout(parent, supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CancellationOutcome{}, fmt.Errorf("begin order work cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var result CancellationOutcome
	var workID *string
	err = tx.QueryRow(ctx, `
		SELECT work_id::text,cancel_outcome,was_replayed
		FROM supply.ingest_order_work_cancellation(
			$1::uuid,$2::uuid,$3::uuid,$4::char(2),$5::uuid,$6::uuid,
			$7::uuid,$8::bigint,$9::text,$10::char(64),$11::jsonb,$12::timestamptz)`,
		value.ID, value.SourceWorkIntentID, value.TenantID, value.Country,
		value.CustomerID, value.OrderID, value.VendorID, value.OrderRevision,
		value.OrderStatus, hex.EncodeToString(digest[:]), payload, service.clock().UTC()).Scan(
		&workID, &result.Outcome, &result.Replayed)
	if err != nil {
		return CancellationOutcome{}, mapOrderWorkIngressError(err)
	}
	if workID != nil {
		result.WorkID = *workID
	}
	if err := tx.Commit(ctx); err != nil {
		return CancellationOutcome{}, mapOrderWorkIngressError(err)
	}
	return result, nil
}
