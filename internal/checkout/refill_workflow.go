package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
	"github.com/yazhsab/planext4u-backend/internal/workflow"
)

type refillIntent struct {
	OfferExpiresAfter time.Duration
	Kind              string
	Scope             Scope
	Key               string
	Method            payment.Method
	Payer             payment.Payer
	Result            WalletRefillResult
}

func (service *Service) createRefillDurably(ctx context.Context, scope Scope, key, offerID string, method payment.Method) (WalletRefillResult, bool, error) {
	id := "refill:" + deterministicID("workflow", scopeKey(scope)+"\x00"+key)
	fingerprint := offerID + "\x00" + string(method)
	var result WalletRefillResult
	replay := false
	err := service.store.Journal().Locked(ctx, id, func(session *workflow.Session) error {
		record, found, err := session.Load(ctx, id)
		if err != nil {
			return err
		}
		var intent refillIntent
		if found {
			if record.Fingerprint != checkoutFingerprint(fingerprint) || record.TenantID != scope.TenantID || record.Country != scope.Country || record.CustomerID != scope.CustomerID {
				return ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.Payload, &intent); err != nil {
				return err
			}
			intent.Result.Offer.ExpiresAfter = intent.OfferExpiresAfter
			replay = true
		} else {
			offer, err := service.deps.Wallet.RefillOffer(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, offerID)
			if err != nil || !containsString(offer.PaymentMethods, string(method)) {
				return ErrPaymentMethod
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
			intent = refillIntent{OfferExpiresAfter: offer.ExpiresAfter, Kind: "WALLET_REFILL", Scope: scope, Key: key, Method: method, Payer: payer, Result: WalletRefillResult{Offer: offer}}
			payload, err := json.Marshal(intent)
			if err != nil {
				return err
			}
			record = workflow.Record{ID: id, TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID, Key: "refill:" + key, Fingerprint: checkoutFingerprint(fingerprint), Payload: payload}
			if err := session.Insert(ctx, record); err != nil {
				if errors.Is(err, workflow.ErrConflict) {
					return ErrIdempotencyConflict
				}
				return err
			}
		}
		if !record.Done {
			if intent.Result.Payment.ID == "" {
				ownerKey := deterministicID("refill-payment", scopeKey(scope)+"\x00"+key)
				reference := deterministicID("wallet-refill", "wallet-refill\x00"+scopeKey(scope)+"\x00"+key)
				p, _, err := service.deps.Payment.CreateWithPayer(ctx, payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, ownerKey, reference, method, payment.Money{AmountMinor: intent.Result.Offer.Price.AmountMinor, Currency: intent.Result.Offer.Price.Currency}, intent.Payer)
				if err != nil {
					return err
				}
				if err := service.effect("refill-payment-created"); err != nil {
					return err
				}
				intent.Result.Payment = p
				if err := session.Save(ctx, record, intent); err != nil {
					return err
				}
			}
			saved, _, err := service.store.SaveRefill(scope, key, fingerprint, intent.Result, refillProcess{scope: scope, offer: intent.Result.Offer})
			if err != nil {
				return err
			}
			if err := service.effect("refill-process-linked"); err != nil {
				return err
			}
			intent.Result = saved
			record.Done = true
			if err := session.Save(ctx, record, intent); err != nil {
				return err
			}
		}
		result = intent.Result
		return nil
	})
	return result, replay, err
}
