package booking

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

// HandlePayment is reachable only from verified webhooks/reconciliation and
// resolves identity from the booking. Expired captures are refunded, not filled.
func (service *PostgresService) HandlePayment(ctx context.Context, paymentID string) (bool, error) {
	var id string
	err := service.pool.QueryRow(ctx, `SELECT id::text FROM booking.bookings WHERE payment_id=$1`, paymentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	value, err := service.loadBooking(ctx, service.pool, id, false)
	if err != nil {
		return true, err
	}
	pScope := payment.Scope{TenantID: value.scope.TenantID, Country: value.scope.Country, CustomerID: value.scope.CustomerID}
	p, err := service.payments.Get(pScope, paymentID)
	if err != nil {
		return true, err
	}
	if !paymentCaptured(p.Status) {
		return true, nil
	}
	if value.Status == StatusPendingPayment && value.paymentExpiresAt.After(service.Now()) {
		_, _, err = service.ConfirmPayment(value.scope, "provider-capture-"+paymentID, id, value.Revision)
		return true, err
	}
	if value.Status != StatusPendingPayment && value.Status != StatusCancelled {
		return true, nil
	}
	_, _, err = service.mutateCustomer(value.scope, "late-capture-refund-"+paymentID, id, value.Revision, "late-capture", paymentID, func(_ context.Context, _ pgx.Tx, current *Booking) error {
		refunded, err := service.payments.RequestRefund(pScope, paymentID, p.Amount)
		if err != nil {
			return err
		}
		current.Payment = refunded
		if current.walletDebit != nil {
			if service.wallet == nil {
				return ErrPolicyDenied
			}
			_, _, err = service.wallet.ReverseDebit(wallet.Scope{TenantID: value.scope.TenantID, Country: value.scope.Country, CustomerID: value.scope.CustomerID}, "late-booking-wallet-"+paymentID, current.walletDebit.ID, id)
			if err != nil && !errors.Is(err, wallet.ErrAlreadyReversed) {
				return err
			}
		}
		if current.Status != StatusCancelled {
			service.transition(current, StatusCancelled, "PAYMENT", "Payment captured after booking window expired")
		}
		return nil
	})
	return true, err
}

func (service *PostgresService) RecoverPayments(ctx context.Context, limit int) error {
	rows, err := service.pool.Query(ctx, `SELECT b.payment_id::text FROM booking.bookings b JOIN payment.payments p ON p.id=b.payment_id WHERE b.status IN ('PENDING_PAYMENT','CANCELLED') AND p.status IN ('CAPTURED','RECONCILED') ORDER BY b.updated_at LIMIT $1`, limit)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if _, err := service.HandlePayment(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
