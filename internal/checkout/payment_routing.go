package checkout

import (
	"context"
	"errors"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
)

// SetPaymentOwners is configured once at startup. Each owner resolves identity
// from its persisted aggregate, never from webhook headers or request claims.
func (service *Service) SetPaymentOwners(owners ...func(context.Context, string) (bool, error)) {
	service.paymentOwners = owners
}

func (service *Service) RecoverPaymentFinalizations(ctx context.Context, limit int) error {
	if service.store == nil {
		return nil
	}
	ids, err := service.store.PendingFinalizations(ctx, limit)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		proc, found, err := service.checkoutProcess(id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !found {
			if _, _, err := service.FinalizeProviderWalletRefill(id); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		p, err := service.deps.Payment.Get(payment.Scope{TenantID: proc.scope.TenantID, Country: proc.scope.Country, CustomerID: proc.scope.CustomerID}, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if p.Status == payment.StatusCaptured || p.Status == payment.StatusReconciled || p.Status == payment.StatusRefunded {
			if err := service.ApplyPayment(ctx, "recovery-finalize-"+id, p); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		current, err := service.deps.Orders.Get(orderScope(proc.scope), proc.orderID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current.Status == order.StatusPendingPayment {
			current, _, err = service.deps.Orders.Transition(orderScope(proc.scope), "expiry-request-"+id, current.ID, current.Revision, order.StatusCancelRequested, "CUSTOMER", "Payment or reservation expired")
		}
		if err == nil && current.Status == order.StatusCancelRequested {
			_, _, err = service.FinalizeCancellation(ctx, proc.scope, "expiry-cancel-"+id, current.ID, current.Revision, "Payment or reservation expired")
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (service *Service) ApplyPayment(ctx context.Context, key string, value payment.Payment) error {
	for _, owner := range service.paymentOwners {
		found, err := owner(ctx, value.ID)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
	}
	if process, found, err := service.checkoutProcess(value.ID); err != nil {
		return err
	} else if found {
		latest, err := service.deps.Payment.Get(payment.Scope{TenantID: process.scope.TenantID, Country: process.scope.Country, CustomerID: process.scope.CustomerID}, value.ID)
		if err != nil {
			return err
		}
		value = latest
	}
	if value.Status == payment.StatusCaptured || value.Status == payment.StatusReconciled {
		if service.HasWalletRefill(value.ID) {
			_, _, err := service.FinalizeProviderWalletRefill(value.ID)
			return err
		}
		_, _, err := service.FinalizeProviderPayment(key, value.ID)
		return err
	}
	if value.Status == payment.StatusRefunded {
		_, _, err := service.FinalizeProviderRefund(key, value.ID)
		return err
	}
	return nil
}
