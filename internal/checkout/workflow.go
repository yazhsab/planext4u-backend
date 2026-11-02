package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
	"github.com/yazhsab/planext4u-backend/internal/workflow"
)

type placementIntent struct {
	Scope             Scope
	Quote             Quote
	Policy            PricingPolicy
	Method            payment.Method
	Payer             payment.Payer
	ReservationExpiry time.Time
	Stage             int
	Aborting          bool
	Aborted           bool
	Result            PlaceResult
}

func (service *Service) placeDurably(ctx context.Context, scope Scope, key, quoteID string, method payment.Method) (PlaceResult, bool, error) {
	journal := service.store.Journal()
	id := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID + ":" + quoteID
	fingerprint := quoteID + "\x00" + string(method)
	var result PlaceResult
	replay := false
	err := journal.Locked(ctx, id, func(session *workflow.Session) error {
		record, found, err := session.Load(ctx, id)
		if err != nil {
			return err
		}
		var intent placementIntent
		if found {
			if record.Key != key || record.Fingerprint != checkoutFingerprint(fingerprint) || record.TenantID != scope.TenantID || record.CustomerID != scope.CustomerID || record.Country != scope.Country {
				return ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.Payload, &intent); err != nil {
				return err
			}
			replay = true
		} else {
			quote, found, err := service.store.Quote(scope, quoteID)
			if err != nil {
				return err
			}
			if !found {
				return ErrQuoteNotFound
			}
			if !service.clock().Before(quote.ExpiresAt) {
				return ErrQuoteExpired
			}
			if !containsMethod(quote.PaymentMethods, method) {
				return ErrPaymentMethod
			}
			repriced, err := service.calculate(ctx, scope, "place-"+key, QuoteRequest{CartRevision: quote.CartRevision, AddressID: quote.Address.ID, DeliverySlot: quote.Delivery.ID, PromotionCode: quote.PromotionCode, WalletPoints: quote.WalletPointsRedeemed})
			if err != nil || quoteValueFingerprint(repriced) != quoteValueFingerprint(quote) {
				return ErrQuoteStale
			}
			configuration, err := service.configurationFor(ctx, scope)
			if err != nil {
				return err
			}
			policy, found := pricingPolicy(configuration, scope.Country)
			if !found || policy.Version != quote.PricingPolicyVersion {
				return ErrQuoteStale
			}
			payer := payment.Payer{}
			if service.deps.Payers != nil {
				payer, err = service.deps.Payers.ResolvePayer(ctx, scope)
				if err != nil {
					return err
				}
			} else if method == payment.MethodPaystack {
				return ErrPaymentMethod
			}
			intent = placementIntent{Scope: scope, Quote: quote, Policy: policy, Method: method, Payer: payer, ReservationExpiry: service.clock().UTC().Add(policy.ReservationTTL)}
			payload, err := json.Marshal(intent)
			if err != nil {
				return err
			}
			record = workflow.Record{ID: id, TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID, Key: key, Fingerprint: checkoutFingerprint(fingerprint), Payload: payload}
			if err := session.Insert(ctx, record); err != nil {
				if errors.Is(err, workflow.ErrConflict) {
					return ErrIdempotencyConflict
				}
				return err
			}
		}
		if record.Done {
			result = intent.Result
			if intent.Aborted {
				return ErrQuoteExpired
			}
			return nil
		}
		if err := service.resumePlacement(ctx, session, record, &intent); err != nil {
			return err
		}
		result = intent.Result
		return nil
	})
	return result, replay, err
}

func (service *Service) effect(name string) error {
	if service.afterEffect != nil {
		return service.afterEffect(name)
	}
	return nil
}

func (service *Service) resumePlacement(ctx context.Context, session *workflow.Session, record workflow.Record, intent *placementIntent) error {
	scope, key := intent.Scope, record.Key
	operationKey := deterministicID("checkout-command", scopeKey(scope)+"\x00"+key)
	intent.Quote.scope = scope
	invScope := inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}
	walletScope := wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	reference := deterministicID("checkout", scopeKey(scope)+"\x00"+key)
	if !service.clock().Before(intent.ReservationExpiry) {
		committed := false
		if intent.Result.Reservation.ID != "" {
			current, err := service.deps.Inventory.Get(invScope, intent.Result.Reservation.ID)
			if err != nil {
				return err
			}
			committed = current.State == inventory.StateCommitted
		}
		intent.Aborting = !committed
	}
	save := func() error { return session.Save(ctx, record, intent) }
	// Stage is advanced only after the effect and checkpoint. A crash in that
	// gap replays the same immutable request against the owner's idempotency log.
	if intent.Stage == 0 {
		lines := make([]inventory.Line, 0, len(intent.Quote.Items))
		for _, line := range intent.Quote.Items {
			lines = append(lines, inventory.Line{VariantID: line.VariantID, Quantity: line.Quantity})
		}
		value, _, err := service.deps.Inventory.Reserve(invScope, operationKey+"-stock", reference, lines, intent.ReservationExpiry)
		if err != nil {
			if intent.Aborting && errors.Is(err, inventory.ErrInvalidRequest) {
				intent.Aborted = true
				record.Done = true
				return save()
			}
			return err
		}
		if err := service.effect("stock-reserved"); err != nil {
			return err
		}
		intent.Result.Quote = intent.Quote
		intent.Result.Reservation = value
		intent.Stage = 1
		if err := save(); err != nil {
			return err
		}
	}
	if intent.Stage == 1 && !intent.Aborting {
		if intent.Quote.WalletPointsRedeemed > 0 {
			value, _, err := service.deps.Wallet.Redeem(walletScope, operationKey+"-wallet", reference, intent.Quote.WalletPointsRedeemed)
			if err != nil {
				return err
			}
			if err := service.effect("wallet-debited"); err != nil {
				return err
			}
			intent.Result.WalletDebit = &value
		}
		intent.Stage = 2
		if err := save(); err != nil {
			return err
		}
	}
	// A debit may have committed before its checkpoint. Replay it to discover
	// its durable entry before compensation; reversal is itself idempotent.
	if intent.Stage == 1 && intent.Aborting && intent.Quote.WalletPointsRedeemed > 0 {
		value, _, err := service.deps.Wallet.Redeem(walletScope, operationKey+"-wallet", reference, intent.Quote.WalletPointsRedeemed)
		if err == nil {
			intent.Result.WalletDebit = &value
		} else if !errors.Is(err, wallet.ErrInsufficientBalance) {
			return err
		}
	}
	if intent.Stage == 2 {
		value, _, err := service.deps.Payment.CreateWithPayer(ctx, paymentScope, operationKey+"-payment", reference, intent.Method, payment.Money{AmountMinor: intent.Quote.Total.AmountMinor, Currency: intent.Quote.Total.Currency}, intent.Payer)
		if err != nil {
			return err
		}
		if err := service.effect("payment-created"); err != nil {
			return err
		}
		intent.Result.Payment = value
		intent.Stage = 3
		if err := save(); err != nil {
			return err
		}
	}
	if intent.Stage == 3 && !intent.Aborting {
		snapshot := service.orderSnapshot(intent.Quote, intent.Result.Reservation.ID, intent.Result.Payment, intent.Policy)
		// Always create prepaid orders pending. Capture is applied through the
		// durable process after the owner link exists, including early webhooks.
		value, _, err := service.deps.Orders.Create(orderScope(scope), operationKey+"-order", snapshot, intent.Method == payment.MethodWallet)
		if err != nil {
			return err
		}
		if err := service.effect("order-created"); err != nil {
			return err
		}
		intent.Result.Order = value
		intent.Stage = 4
		if err := save(); err != nil {
			return err
		}
	}
	// Recover a possibly committed order even if the reservation elapsed.
	if intent.Stage == 3 && intent.Aborting {
		snapshot := service.orderSnapshot(intent.Quote, intent.Result.Reservation.ID, intent.Result.Payment, intent.Policy)
		value, _, err := service.deps.Orders.Create(orderScope(scope), operationKey+"-order", snapshot, intent.Method == payment.MethodWallet)
		if err != nil {
			return err
		}
		intent.Result.Order = value
		intent.Stage = 4
		if err := save(); err != nil {
			return err
		}
	}
	if intent.Aborting {
		return service.abortPlacement(ctx, session, record, intent)
	}
	if intent.Stage == 4 && intent.Result.Order.Status == order.StatusPlaced {
		value, err := service.deps.Inventory.Commit(invScope, intent.Result.Reservation.ID)
		if err != nil {
			return err
		}
		if err := service.effect("stock-committed"); err != nil {
			return err
		}
		intent.Result.Reservation = value
	}
	debitID := ""
	if intent.Result.WalletDebit != nil {
		debitID = intent.Result.WalletDebit.ID
	}
	value, _, err := service.store.SavePlace(scope, record.Key, intent.Quote.ID+"\x00"+string(intent.Method), intent.Result, process{scope: scope, reservationID: intent.Result.Reservation.ID, orderID: intent.Result.Order.ID, walletDebitID: debitID})
	if err != nil {
		return err
	}
	if err := service.effect("process-linked"); err != nil {
		return err
	}
	intent.Result = value
	intent.Stage = 5
	record.Done = true
	if err := save(); err != nil {
		return err
	}
	service.drainOrderNotifications(ctx)
	return nil
}

func (service *Service) abortPlacement(ctx context.Context, session *workflow.Session, record workflow.Record, intent *placementIntent) error {
	scope, key := intent.Scope, record.Key
	operationKey := deterministicID("checkout-command", scopeKey(scope)+"\x00"+key)
	pScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	if intent.Result.Payment.ID != "" {
		value, err := service.deps.Payment.Get(pScope, intent.Result.Payment.ID)
		if err != nil {
			return err
		}
		switch value.Status {
		case payment.StatusCaptured, payment.StatusReconciled, payment.StatusRefundFailed:
			_, err = service.deps.Payment.RequestRefundWithProvider(ctx, pScope, value.ID, value.Amount, "Checkout recovery reservation expired")
		case payment.StatusRefunded, payment.StatusRefundSubmitted, payment.StatusCancelled:
		default:
			_, err = service.deps.Payment.CancelUncaptured(pScope, value.ID)
		}
		if err != nil {
			return err
		}
		if err := service.effect("checkout-payment-compensated"); err != nil {
			return err
		}
	}
	if intent.Result.WalletDebit != nil {
		if _, _, err := service.deps.Wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, operationKey+"-wallet-reversal", intent.Result.WalletDebit.ID, deterministicID("checkout", scopeKey(scope)+"\x00"+key)); err != nil {
			return err
		}
		if err := service.effect("checkout-wallet-reversed"); err != nil {
			return err
		}
	}
	invScope := inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}
	reservation, err := service.deps.Inventory.Get(invScope, intent.Result.Reservation.ID)
	if err != nil {
		return err
	}
	if reservation.State == inventory.StateCommitted {
		_, _, err = service.deps.Inventory.Restock(invScope, operationKey+"-recovery-restock", reservation.ID, reservation.Lines)
	} else if reservation.State == inventory.StateReserved {
		_, err = service.deps.Inventory.Release(invScope, reservation.ID)
	}
	if err != nil {
		return err
	}
	if err := service.effect("checkout-stock-restored"); err != nil {
		return err
	}
	if intent.Result.Order.ID != "" {
		current, err := service.deps.Orders.Get(orderScope(scope), intent.Result.Order.ID)
		if err != nil {
			return err
		}
		if current.Status == order.StatusPlaced {
			current, _, err = service.deps.Orders.Transition(orderScope(scope), operationKey+"-recovery-request", current.ID, current.Revision, order.StatusCancelRequested, "CUSTOMER", "Checkout recovery expired")
			if err != nil {
				return err
			}
		}
		if current.Status != order.StatusCancelled {
			current, _, err = service.deps.Orders.Transition(orderScope(scope), operationKey+"-recovery-cancel", current.ID, current.Revision, order.StatusCancelled, "PLATFORM", "Checkout recovery expired")
			if err != nil {
				return err
			}
		}
		intent.Result.Order = current
		if err := service.effect("checkout-order-cancelled"); err != nil {
			return err
		}
	}
	intent.Aborted = true
	record.Done = true
	return session.Save(ctx, record, intent)
}

// RecoverPlacements is called at startup and each worker interval. It needs no
// customer retry and does not revalidate an already accepted quote's expiry.
func (service *Service) RecoverPlacements(ctx context.Context, limit int) (int, error) {
	if service.store == nil {
		return 0, nil
	}
	records, err := service.store.Journal().Pending(ctx, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	var failures []error
	for _, record := range records {
		var envelope struct{ Kind string }
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			failures = append(failures, err)
			continue
		}
		if envelope.Kind == "WALLET_REFILL" {
			var intent refillIntent
			if err := json.Unmarshal(record.Payload, &intent); err != nil {
				failures = append(failures, err)
				continue
			}
			if _, _, err := service.createRefillDurably(ctx, intent.Scope, intent.Key, intent.Result.Offer.ID, intent.Method); err != nil {
				failures = append(failures, err)
			} else {
				count++
			}
			continue
		}
		var intent placementIntent
		if err := json.Unmarshal(record.Payload, &intent); err != nil {
			failures = append(failures, err)
			continue
		}
		_, _, err := service.placeDurably(ctx, intent.Scope, record.Key, intent.Quote.ID, intent.Method)
		if err != nil && !errors.Is(err, ErrQuoteExpired) {
			failures = append(failures, err)
		} else {
			count++
		}
	}
	return count, errors.Join(failures...)
}
