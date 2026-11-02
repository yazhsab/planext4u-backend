package main

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/supply"
)

type vendorWorkSource interface {
	ClaimVendorWorkIntents(context.Context, int) ([]order.VendorWorkIntent, error)
	AckVendorWorkIntent(context.Context, string, string) error
	RetryVendorWorkIntent(context.Context, string, string, string) error
}

type vendorWorkOrderReader interface {
	Get(order.Scope, string) (order.Order, error)
}

type vendorWorkOwner interface {
	IngestOrderWork(context.Context, supply.OrderWorkIngress) (supply.WorkItem, bool, error)
}

type vendorWorkCancellationSource interface {
	ClaimVendorWorkCancellations(context.Context, int) ([]order.VendorWorkCancellationIntent, error)
	AckVendorWorkCancellation(context.Context, string, string) error
	RetryVendorWorkCancellation(context.Context, string, string, string) error
}

type vendorWorkCancellationOwner interface {
	IngestOrderWorkCancellation(context.Context, supply.OrderWorkCancellation) (supply.CancellationOutcome, error)
}

// deliverVendorWorkCancellations commits each ordering-owned revocation to the
// supply owner before acknowledging the source lease. The same immutable
// source can be replayed after a crash. A completed work item is recorded as
// a discrepancy so an operator can reconcile it instead of treating it as a
// successful cancellation.
func deliverVendorWorkCancellations(ctx context.Context, source vendorWorkCancellationSource, owner vendorWorkCancellationOwner, limit int) (int, int, error) {
	intents, err := source.ClaimVendorWorkCancellations(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	delivered, discrepancies := 0, 0
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return delivered, discrepancies, err
		}
		result, err := owner.IngestOrderWorkCancellation(ctx, supply.OrderWorkCancellation{
			ID: intent.ID, SourceWorkIntentID: intent.SourceWorkIntentID,
			TenantID: intent.TenantID, Country: intent.Country,
			CustomerID: intent.CustomerID, OrderID: intent.OrderID,
			VendorID: intent.VendorID, OrderRevision: intent.OrderRevision,
			OrderStatus: intent.OrderStatus,
		})
		if err != nil {
			if retryErr := source.RetryVendorWorkCancellation(ctx, intent.ID, intent.ClaimToken, vendorWorkCancellationFailureCode(err)); retryErr != nil {
				return delivered, discrepancies, retryErr
			}
			continue
		}
		if !validVendorWorkCancellationOutcome(result.Outcome) {
			if retryErr := source.RetryVendorWorkCancellation(ctx, intent.ID, intent.ClaimToken, "UNEXPECTED_OUTCOME"); retryErr != nil {
				return delivered, discrepancies, retryErr
			}
			continue
		}
		if err := source.AckVendorWorkCancellation(ctx, intent.ID, intent.ClaimToken); err != nil {
			return delivered, discrepancies, err
		}
		delivered++
		if result.Outcome == "ALREADY_COMPLETED" {
			discrepancies++
		}
	}
	return delivered, discrepancies, nil
}

func validVendorWorkCancellationOutcome(outcome string) bool {
	switch outcome {
	case "NO_WORK", "CANCELLED", "ALREADY_CANCELLED", "ALREADY_COMPLETED", "ALREADY_REJECTED":
		return true
	default:
		return false
	}
}

func vendorWorkCancellationFailureCode(err error) string {
	switch {
	case errors.Is(err, supply.ErrInvalidRequest):
		return "INVALID_SOURCE"
	case errors.Is(err, supply.ErrIdempotencyConflict), errors.Is(err, supply.ErrConflict):
		return "SOURCE_CONFLICT"
	default:
		return "SUPPLY_UNAVAILABLE"
	}
}

// deliverVendorWorkIntents checks the current order before calling the supply
// owner and acknowledges the source only after supply has committed. A
// cancellation between that check and the owner commit is closed by the
// cancellation worker on its next pass.
func deliverVendorWorkIntents(ctx context.Context, source vendorWorkSource, orders vendorWorkOrderReader, owner vendorWorkOwner, limit int) (int, error) {
	intents, err := source.ClaimVendorWorkIntents(ctx, limit)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return delivered, err
		}
		scope := order.Scope{TenantID: intent.TenantID, Country: intent.Country, CustomerID: intent.CustomerID}
		current, err := orders.Get(scope, intent.OrderID)
		if err != nil {
			if retryErr := source.RetryVendorWorkIntent(ctx, intent.ID, intent.ClaimToken, "ORDER_LOOKUP_FAILED"); retryErr != nil {
				return delivered, retryErr
			}
			continue
		}
		if !vendorWorkIntentMatchesOrder(intent, current) {
			if retryErr := source.RetryVendorWorkIntent(ctx, intent.ID, intent.ClaimToken, "SOURCE_MISMATCH"); retryErr != nil {
				return delivered, retryErr
			}
			continue
		}
		if !vendorWorkOrderActive(current.Status) {
			// Cancellation is terminal in the ordering state machine. The
			// cancellation outbox remains independently pending until supply
			// commits its tombstone, so the superseded placement can be closed.
			if current.Status == order.StatusCancelRequested || current.Status == order.StatusCancelled {
				if err := source.AckVendorWorkIntent(ctx, intent.ID, intent.ClaimToken); err != nil {
					return delivered, err
				}
				continue
			}
			if retryErr := source.RetryVendorWorkIntent(ctx, intent.ID, intent.ClaimToken, "ORDER_NOT_ACTIVE"); retryErr != nil {
				return delivered, retryErr
			}
			continue
		}
		request := supply.OrderWorkIngress{
			IntentID: intent.ID, TenantID: intent.TenantID, Country: intent.Country,
			CustomerID: intent.CustomerID, OrderID: intent.OrderID, VendorID: intent.VendorID,
			OrderRevision: intent.OrderRevision, Lines: intent.Lines,
			Currency: intent.Currency, ProductTaxTreatment: intent.ProductTaxTreatment,
		}
		if _, _, err := owner.IngestOrderWork(ctx, request); err != nil {
			if errors.Is(err, supply.ErrOrderWorkCancelled) {
				// The supply tombstone is committed. A stale placement claim
				// cannot be ingested on replay, including after this ack crashes.
				if ackErr := source.AckVendorWorkIntent(ctx, intent.ID, intent.ClaimToken); ackErr != nil {
					return delivered, ackErr
				}
				continue
			}
			if retryErr := source.RetryVendorWorkIntent(ctx, intent.ID, intent.ClaimToken, vendorWorkFailureCode(err)); retryErr != nil {
				return delivered, retryErr
			}
			continue
		}
		if err := source.AckVendorWorkIntent(ctx, intent.ID, intent.ClaimToken); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

func vendorWorkOrderActive(status order.Status) bool {
	switch status {
	case order.StatusPlaced, order.StatusAccepted, order.StatusPacking,
		order.StatusReadyForHandover, order.StatusAssigned, order.StatusPickedUp,
		order.StatusOutForDelivery, order.StatusDeliveredPendingConfirmation:
		return true
	default:
		return false
	}
}

func vendorWorkIntentMatchesOrder(intent order.VendorWorkIntent, current order.Order) bool {
	if current.ID != intent.OrderID || current.Revision < intent.OrderRevision ||
		current.Snapshot.Total.Currency != intent.Currency ||
		current.Snapshot.ProductTaxTreatment != intent.ProductTaxTreatment ||
		intent.ID != uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+intent.OrderID+":"+intent.VendorID)).String() {
		return false
	}
	lines := make([]order.LineSnapshot, 0, len(intent.Lines))
	for _, line := range current.Snapshot.Lines {
		if line.VendorID == intent.VendorID {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 || len(lines) != len(intent.Lines) {
		return false
	}
	for index := range lines {
		if lines[index] != intent.Lines[index] {
			return false
		}
	}
	return true
}

func vendorWorkFailureCode(err error) string {
	switch {
	case errors.Is(err, supply.ErrOrderWorkVendorInactive):
		return "VENDOR_NOT_ACTIVE"
	case errors.Is(err, supply.ErrInvalidRequest):
		return "INVALID_SOURCE"
	case errors.Is(err, supply.ErrIdempotencyConflict), errors.Is(err, supply.ErrConflict):
		return "SOURCE_CONFLICT"
	default:
		return "SUPPLY_UNAVAILABLE"
	}
}
