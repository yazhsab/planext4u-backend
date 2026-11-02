package food

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	pay "github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
	"github.com/yazhsab/planext4u-backend/internal/workflow"
)

type foodIntent struct {
	Actor   Actor
	CartID  string
	Order   Order
	Points  int64
	DebitID string
	Stage   int
}

func allowedFoodMethod(country string, method pay.Method) bool {
	return method == pay.MethodWallet || country == "IN" && method == pay.MethodRazorpay || country == "NG" && method == pay.MethodPaystack
}
func (service *PostgresService) journal() workflow.Store {
	return workflow.Store{Pool: service.pool, Schema: "food"}
}
func (service *PostgresService) effect(stage string) error {
	if service.afterEffect != nil {
		return service.afterEffect(stage)
	}
	return nil
}

func (service *PostgresService) createDurably(ctx context.Context, actor Actor, key string, request CreateOrderRequest) (Order, bool, error) {
	var result Order
	replay := false
	err := service.journal().Locked(ctx, request.CartID, func(session *workflow.Session) error {
		record, found, err := session.Load(ctx, request.CartID)
		if err != nil {
			return err
		}
		var intent foodIntent
		if found {
			if record.TenantID != actor.TenantID || record.Country != actor.Country || record.CustomerID != actor.Subject {
				return ErrForbidden
			}
			if record.Key != key {
				return ErrConflict
			}
			if record.Fingerprint != digest(request) {
				return ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.Payload, &intent); err != nil {
				return err
			}
			replay = true
		} else {
			if !contains(service.AvailablePaymentMethods(actor), request.PaymentMethod) {
				return ErrInvalidRequest
			}
			cart, err := loadCart(ctx, service.pool, request.CartID, false)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if cart.tenantID != actor.TenantID || cart.country != actor.Country || cart.CustomerID != actor.Subject {
				return ErrForbidden
			}
			if !cart.ExpiresAt.After(service.Now()) {
				return ErrConflict
			}
			restaurant, err := loadRestaurant(ctx, service.pool, cart.Restaurant.ID, actor.TenantID, actor.Country, false)
			if err != nil {
				return err
			}
			if !restaurant.Open || !beforeCutoff(service.Now(), restaurant.AcceptUntilMinute) {
				return ErrRestaurantClosed
			}
			policy, err := loadFoodPolicy(ctx, service.pool, actor.TenantID, actor.Country)
			if err != nil {
				return err
			}
			// Existing orders created before the journal migration still own the cart.
			var used bool
			if err := service.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM food.orders WHERE source_cart_id=$1)`, cart.ID).Scan(&used); err != nil {
				return err
			}
			if used {
				return ErrConflict
			}
			now := service.Now()
			value := Order{ID: foodUUID("order", actor.TenantID+":"+actor.Country+":"+actor.Subject+":"+key), CustomerID: actor.Subject, RestaurantID: restaurant.ID, Revision: 1, Status: StatusAwaitingPayment, Lines: cloneLines(cart.Lines), Subtotal: cart.Subtotal, DeliveryFee: cart.DeliveryFee, Tax: cart.Tax, Total: cart.Total, Payment: Payment{Method: request.PaymentMethod}, PostalCode: cart.PostalCode, PricingVersion: cart.PricingVersion, AcceptBy: now.Add(policy.cartTTL), CreatedAt: now, UpdatedAt: now}
			intent = foodIntent{Actor: actor, CartID: cart.ID, Order: value, Points: (cart.Total.AmountMinor + policy.walletPointValueMinor - 1) / policy.walletPointValueMinor}
			payload, err := json.Marshal(intent)
			if err != nil {
				return err
			}
			record = workflow.Record{ID: cart.ID, TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject, Key: key, Fingerprint: digest(request), Payload: payload}
			if err := session.Insert(ctx, record); err != nil {
				if errors.Is(err, workflow.ErrConflict) {
					return ErrIdempotencyConflict
				}
				return err
			}
		}
		intent.Order.tenantID, intent.Order.country, intent.Order.walletDebitID = actor.TenantID, actor.Country, intent.DebitID
		if record.Done {
			result = intent.Order
			return nil
		}
		if err := service.resumeFood(ctx, session, record, &intent); err != nil {
			return err
		}
		result = intent.Order
		return nil
	})
	return result, replay, err
}

func (service *PostgresService) resumeFood(ctx context.Context, session *workflow.Session, record workflow.Record, intent *foodIntent) error {
	actor, key := intent.Actor, record.Key
	operationKey := foodUUID("command", actor.TenantID+":"+actor.Country+":"+actor.Subject+":"+key)
	value := &intent.Order
	save := func() error { return session.Save(ctx, record, intent) }
	if intent.Stage == 0 {
		if value.Payment.Method == string(pay.MethodWallet) {
			if service.wallet == nil {
				return ErrForbidden
			}
			entry, _, err := service.wallet.Redeem(wallet.Scope{TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject}, operationKey+"-wallet", value.ID, intent.Points)
			if err != nil {
				return err
			}
			if err := service.effect("wallet-debited"); err != nil {
				return err
			}
			intent.DebitID = entry.ID
			value.walletDebitID = entry.ID
		}
		intent.Stage = 1
		if err := save(); err != nil {
			return err
		}
	}
	if intent.Stage == 1 {
		paymentValue, _, err := service.payments.Create(pay.Scope{TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject}, operationKey+"-payment", value.ID, pay.Method(value.Payment.Method), pay.Money{AmountMinor: value.Total.AmountMinor, Currency: value.Total.Currency})
		if err != nil {
			return err
		}
		if err := service.effect("payment-created"); err != nil {
			return err
		}
		value.Payment.Status, value.Payment.Reference = string(paymentValue.Status), paymentValue.ID
		if paymentValue.Status == pay.StatusCaptured || paymentValue.Status == pay.StatusReconciled {
			policy, err := loadFoodPolicy(ctx, service.pool, actor.TenantID, actor.Country)
			if err != nil {
				return err
			}
			value.Status = StatusPendingRestaurant
			value.AcceptBy = service.Now().Add(policy.acceptanceTTL)
		}
		value.AllowedActions = restaurantActions(value.Status)
		value.Timeline = []OrderEvent{{Status: value.Status, Actor: actor.Subject, CreatedAt: service.Now()}}
		intent.Stage = 2
		if err := save(); err != nil {
			return err
		}
	}
	if intent.Stage == 2 {
		// The aggregate, timeline and idempotency response commit atomically. No
		// wallet/provider call occurs inside this transaction.
		tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := foodCommandLock(ctx, tx, actor, "order", key); err != nil {
			return err
		}
		var replay Order
		found, err := loadFoodReplay(ctx, tx, actor, "order", key, record.Fingerprint, &replay)
		if err != nil {
			return err
		}
		if !found {
			if err := insertFoodOrder(ctx, tx, *value, intent.CartID); err != nil {
				return err
			}
			if err := appendFoodTimeline(ctx, tx, value.ID, value.Timeline); err != nil {
				return err
			}
			if err := storeFoodReplay(ctx, tx, actor, "order", key, record.Fingerprint, *value, service.Now()); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if err := service.effect("order-created"); err != nil {
			return err
		}
		intent.Stage = 3
		record.Done = true
		if err := save(); err != nil {
			return err
		}
	}
	return nil
}

func (service *PostgresService) RecoverCheckouts(ctx context.Context, limit int) (int, error) {
	records, err := service.journal().Pending(ctx, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	var failures []error
	for _, record := range records {
		var intent foodIntent
		if err := json.Unmarshal(record.Payload, &intent); err != nil {
			failures = append(failures, err)
			continue
		}
		_, _, err := service.createDurably(ctx, intent.Actor, record.Key, CreateOrderRequest{CartID: intent.CartID, PaymentMethod: intent.Order.Payment.Method})
		if err != nil {
			failures = append(failures, err)
		} else {
			count++
		}
	}
	return count, errors.Join(failures...)
}

// HandlePayment resolves the customer from the durable owner record. Callers
// only reach it after signature verification or server-side reconciliation.
func (service *PostgresService) HandlePayment(ctx context.Context, paymentID string) (bool, error) {
	found := false
	err := workflow.WithCapacity(ctx, service.pool, func() error {
		var err error
		found, err = service.handlePaymentWithCapacity(ctx, paymentID)
		return err
	})
	return found, err
}

func (service *PostgresService) handlePaymentWithCapacity(ctx context.Context, paymentID string) (bool, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM food.orders WHERE payment_reference=$1 FOR UPDATE`, paymentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	value, err := loadFoodOrder(ctx, tx, id, false)
	if err != nil {
		return true, err
	}
	paymentValue, err := service.payments.Get(pay.Scope{TenantID: value.tenantID, Country: value.country, CustomerID: value.CustomerID}, paymentID)
	if err != nil {
		return true, err
	}
	oldStatus := value.Status
	value.Payment.Status = string(paymentValue.Status)
	if value.Payment.RefundState != "" && (paymentValue.Status == pay.StatusRefunded || paymentValue.Status == pay.StatusCancelled) {
		value.Payment.RefundState = "COMPLETE"
	}
	captured := paymentValue.Status == pay.StatusCaptured || paymentValue.Status == pay.StatusReconciled
	if captured && value.Status == StatusAwaitingPayment && value.AcceptBy.After(service.Now()) {
		policy, err := loadFoodPolicy(ctx, tx, value.tenantID, value.country)
		if err != nil {
			return true, err
		}
		value.Status = StatusPendingRestaurant
		value.AcceptBy = service.Now().Add(policy.acceptanceTTL)
	} else if captured && (value.Status == StatusTimedOut || value.Status == StatusCancelled || value.Status == StatusRejected || value.Status == StatusAwaitingPayment) {
		if value.Status == StatusAwaitingPayment {
			value.Status = StatusCancelled
		}
		value.Payment.RefundState = "PENDING"
	} else if value.Status == StatusAwaitingPayment && (paymentValue.Status == pay.StatusFailedFinal || paymentValue.Status == pay.StatusCancelled) {
		value.Status = StatusCancelled
	}
	if oldStatus != value.Status {
		value.Revision++
		value.UpdatedAt = service.Now()
		if err := appendFoodTimeline(ctx, tx, id, []OrderEvent{{Status: value.Status, Actor: "PAYMENT", CreatedAt: service.Now()}}); err != nil {
			return true, err
		}
	}
	if err := updateFoodOrder(ctx, tx, value); err != nil {
		return true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, err
	}
	if value.Payment.RefundState == "PENDING" {
		_, err = service.recoverCompensation(ctx, id)
	}
	return true, err
}

func authorizeAssignedRider(ctx context.Context, query foodQuerier, actor Actor, value Order, requiredStatus string) error {
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return ErrForbidden
	}
	var available bool
	if err := query.QueryRow(ctx, `SELECT to_regclass('fulfillment.delivery_tasks') IS NOT NULL`).Scan(&available); err != nil {
		return err
	}
	if !available {
		return ErrForbidden
	}
	var owns bool
	err := query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND order_id=$3 AND order_type='FOOD' AND assigned_rider_identity_id=$4 AND status IN ('ASSIGNED','PICKED_UP','DELIVERED') AND ($5='' OR status=$5))`, actor.TenantID, actor.Country, value.ID, actor.Subject, requiredStatus).Scan(&owns)
	if err != nil {
		return err
	}
	if !owns {
		return ErrForbidden
	}
	return nil
}

// PaymentMethods is the same country policy enforced by checkout creation.
func PaymentMethods(country string) []string {
	methods := []string{string(pay.MethodWallet)}
	if country == "IN" {
		methods = append(methods, string(pay.MethodRazorpay))
	}
	if country == "NG" {
		methods = append(methods, string(pay.MethodPaystack))
	}
	return methods
}

func (service *Service) AvailablePaymentMethods(actor Actor) []string {
	return PaymentMethods(actor.Country)
}
func (service *PostgresService) AvailablePaymentMethods(actor Actor) []string {
	result := []string{}
	for _, method := range PaymentMethods(actor.Country) {
		if method == string(pay.MethodWallet) && service.wallet == nil {
			continue
		}
		if adapter, ok := service.payments.(interface{ Supports(string, pay.Method) bool }); ok && !adapter.Supports(actor.Country, pay.Method(method)) {
			continue
		}
		result = append(result, method)
	}
	return result
}

func (service *PostgresService) RecoverPayments(ctx context.Context, limit int) error {
	rows, err := service.pool.Query(ctx, `SELECT o.payment_reference FROM food.orders o JOIN payment.payments p ON p.id::text=o.payment_reference WHERE o.payment_status<>p.status OR o.refund_state='PENDING' ORDER BY o.updated_at LIMIT $1`, limit)
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

// The terminal order plus PENDING refund flag is committed before any reversal
// or provider call. It prevents fulfilment and survives an interrupted refund.
func (service *PostgresService) recoverCompensation(ctx context.Context, id string) (Order, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, err := loadFoodOrder(ctx, tx, id, true)
	if err != nil {
		return Order{}, err
	}
	if value.Payment.RefundState != "PENDING" {
		return value, nil
	}
	if err := service.compensate(&value, "food-compensation-"+id); err != nil {
		return Order{}, err
	}
	value.Revision++
	value.UpdatedAt = service.Now()
	if err := updateFoodOrder(ctx, tx, value); err != nil {
		return Order{}, err
	}
	if err := appendFoodTimeline(ctx, tx, id, []OrderEvent{{Status: value.Status, Actor: "SYSTEM", Reason: "Financial compensation recorded", CreatedAt: service.Now()}}); err != nil {
		return Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	return value, nil
}
