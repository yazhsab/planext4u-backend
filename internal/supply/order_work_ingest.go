package supply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yazhsab/planext4u-backend/internal/order"
)

var ErrOrderWorkVendorInactive = errors.New("order work vendor is not active")

// OrderWorkIngress is the supply-owned boundary for an ordering outbox claim.
// It is intentionally absent from the public vendor handler. The full source
// payload is fingerprinted and committed with exactly one vendor work item.
type OrderWorkIngress struct {
	IntentID            string               `json:"intent_id"`
	TenantID            string               `json:"tenant_id"`
	Country             string               `json:"country"`
	CustomerID          string               `json:"customer_id"`
	OrderID             string               `json:"order_id"`
	VendorID            string               `json:"vendor_id"`
	OrderRevision       int64                `json:"order_revision"`
	Lines               []order.LineSnapshot `json:"lines"`
	Currency            string               `json:"currency"`
	ProductTaxTreatment string               `json:"product_tax_treatment"`
}

func (service *PostgresService) OrderWorkIngressReady(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('supply.order_work_ingestions') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		               WHERE n.nspname='supply' AND p.proname='ingest_order_work_intent')`).Scan(&ready); err != nil {
		return fmt.Errorf("check order work ingress readiness: %w", err)
	}
	if !ready {
		return errors.New("order work ingress is unavailable")
	}
	return nil
}

// IngestOrderWork commits through the narrowly granted supply-owned function.
// Its return only follows the supply commit, allowing the ordering worker to
// acknowledge its source intent without losing a placed order on a crash.
func (service *PostgresService) IngestOrderWork(parent context.Context, request OrderWorkIngress) (WorkItem, bool, error) {
	charge, err := orderWorkCharge(request)
	if parent == nil || err != nil {
		return WorkItem{}, false, ErrInvalidRequest
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return WorkItem{}, false, fmt.Errorf("encode order work ingress: %w", err)
	}
	digest := sha256.Sum256(payload)
	ctx, cancel := context.WithTimeout(parent, supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return WorkItem{}, false, fmt.Errorf("begin order work ingress: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.clock().UTC()
	var workID string
	var replayed bool
	err = tx.QueryRow(ctx, `
		SELECT work_id::text,was_replayed
		FROM supply.ingest_order_work_intent($1::uuid,$2::uuid,$3::char(2),$4::uuid,
			$5::uuid,$6::uuid,$7::bigint,$8::char(64),$9::jsonb,$10::bigint,
			$11::char(3),$12::timestamptz)`,
		request.IntentID, request.TenantID, request.Country, request.CustomerID,
		request.OrderID, request.VendorID, request.OrderRevision,
		hex.EncodeToString(digest[:]), payload, charge, request.Currency, now).Scan(&workID, &replayed)
	if err != nil {
		return WorkItem{}, false, mapOrderWorkIngressError(err)
	}
	var value WorkItem
	err = tx.QueryRow(ctx, `
		SELECT id::text,vendor_identity_id::text,reference_type,reference_id::text,
		       status,total_minor,currency,customer_label,updated_at
		FROM supply.work_items
		WHERE id=$1 AND tenant_id=$2 AND country=$3 AND vendor_identity_id=$4`,
		workID, request.TenantID, request.Country, request.VendorID).Scan(
		&value.ID, &value.VendorID, &value.ReferenceType, &value.ReferenceID,
		&value.Status, &value.Total.AmountMinor, &value.Total.Currency,
		&value.CustomerLabel, &value.UpdatedAt)
	if err != nil {
		return WorkItem{}, false, fmt.Errorf("load ingested vendor work: %w", err)
	}
	value.tenantID, value.country = request.TenantID, request.Country
	value.AllowedActions = workActions(value.Status)
	if err := tx.Commit(ctx); err != nil {
		return WorkItem{}, false, mapOrderWorkIngressError(err)
	}
	return value, replayed, nil
}

// WorkItem.Total represents this vendor's customer-facing merchandise charge,
// excluding shared delivery/platform fees and wallet allocation. Commission
// remains in the immutable lines for the later settlement owner.
func orderWorkCharge(value OrderWorkIngress) (int64, error) {
	if !canonicalWorkUUID(value.IntentID) || !canonicalWorkUUID(value.TenantID) ||
		!canonicalWorkUUID(value.CustomerID) || !canonicalWorkUUID(value.OrderID) ||
		!canonicalWorkUUID(value.VendorID) || len(value.Country) != 2 ||
		value.Country != strings.ToUpper(value.Country) || value.OrderRevision < 1 ||
		len(value.Lines) == 0 || len(value.Lines) > 100 ||
		(value.ProductTaxTreatment != "INCLUSIVE" && value.ProductTaxTreatment != "EXCLUSIVE") ||
		len(value.Currency) != 3 || value.Currency != strings.ToUpper(value.Currency) {
		return 0, ErrInvalidRequest
	}
	expectedIntent := uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+value.OrderID+":"+value.VendorID)).String()
	if value.IntentID != expectedIntent {
		return 0, ErrInvalidRequest
	}
	var charge int64
	for _, line := range value.Lines {
		if line.VendorID != value.VendorID || !safeID(line.ItemID) || !safeID(line.VariantID) ||
			line.Quantity < 1 || line.Quantity > 9999 ||
			line.UnitPrice.Currency != value.Currency || line.LineTotal.Currency != value.Currency ||
			line.UnitPrice.AmountMinor < 0 || line.LineTotal.AmountMinor < 0 ||
			line.UnitPrice.AmountMinor > math.MaxInt64/int64(line.Quantity) ||
			line.LineTotal.AmountMinor != line.UnitPrice.AmountMinor*int64(line.Quantity) ||
			line.DiscountMinor < 0 || line.DiscountMinor > line.LineTotal.AmountMinor ||
			line.TaxMinor < 0 || line.CommissionMinor < 0 ||
			line.CommissionMinor > line.LineTotal.AmountMinor {
			return 0, ErrInvalidRequest
		}
		lineCharge := line.LineTotal.AmountMinor - line.DiscountMinor
		if value.ProductTaxTreatment == "EXCLUSIVE" {
			if line.TaxMinor > math.MaxInt64-lineCharge {
				return 0, ErrInvalidRequest
			}
			lineCharge += line.TaxMinor
		}
		if charge > math.MaxInt64-lineCharge {
			return 0, ErrInvalidRequest
		}
		charge += lineCharge
	}
	return charge, nil
}

func canonicalWorkUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func mapOrderWorkIngressError(err error) error {
	var database *pgconn.PgError
	if errors.As(err, &database) {
		switch database.Code {
		case "PX101":
			return ErrIdempotencyConflict
		case "PX102":
			return ErrOrderWorkVendorInactive
		case "PX103":
			return ErrOrderWorkCancelled
		case "23505":
			return ErrConflict
		case "22023":
			return ErrInvalidRequest
		}
	}
	return fmt.Errorf("ingest order work: %w", err)
}
