package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/commerce"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

type quoteReplay struct {
	fingerprint string
	value       Quote
}

type placeReplay struct {
	fingerprint string
	value       PlaceResult
}

type placeCall struct {
	done chan struct{}
}

type addressReplay struct {
	fingerprint string
	value       Address
}

type refillProcess struct {
	scope Scope
	offer wallet.RefillOffer
}

type refillReplay struct {
	fingerprint string
	value       WalletRefillResult
}

type process struct {
	scope         Scope
	reservationID string
	orderID       string
	walletDebitID string
}

type Service struct {
	deps        Dependencies
	config      Configuration
	clock       func() time.Time
	mu          sync.Mutex
	quotes      map[string]Quote
	quoteReqs   map[string]quoteReplay
	placeReqs   map[string]placeReplay
	inflight    map[string]*placeCall
	processes   map[string]process
	addresses   map[string]Address
	addressReqs map[string]addressReplay
	refills     map[string]refillProcess
	refillReqs  map[string]refillReplay
}

func NewService(dependencies Dependencies, configuration Configuration, clock func() time.Time) (*Service, error) {
	if dependencies.Cart == nil || dependencies.Inventory == nil || dependencies.Wallet == nil || dependencies.Payment == nil || dependencies.Orders == nil || clock == nil || !validConfiguration(configuration) {
		return nil, ErrInvalidRequest
	}
	addresses := map[string]Address{}
	defaultScopes := map[string]bool{}
	for _, value := range configuration.Addresses {
		if value.Default {
			defaultScopes[scopeKey(Scope{TenantID: value.TenantID, Country: value.Country, CustomerID: value.CustomerID})] = true
		}
	}
	seenScopes := map[string]bool{}
	for _, value := range configuration.Addresses {
		if value.Revision < 1 {
			value.Revision = 1
		}
		value.Serviceable = addressServiceable(configuration.PostalZones, value.Country, value.PostalCode, value.Locality)
		valueScope := Scope{TenantID: value.TenantID, Country: value.Country, CustomerID: value.CustomerID}
		key := scopeKey(valueScope)
		if !seenScopes[key] && !defaultScopes[key] {
			value.Default = true
		}
		seenScopes[key] = true
		addresses[addressKey(valueScope, value.ID)] = value
	}
	return &Service{deps: dependencies, config: configuration, clock: clock, quotes: map[string]Quote{}, quoteReqs: map[string]quoteReplay{}, placeReqs: map[string]placeReplay{}, inflight: map[string]*placeCall{}, processes: map[string]process{}, addresses: addresses, addressReqs: map[string]addressReplay{}, refills: map[string]refillProcess{}, refillReqs: map[string]refillReplay{}}, nil
}

func (service *Service) WalletExperience(scope Scope) (wallet.Experience, error) {
	if !validScope(scope) {
		return wallet.Experience{}, ErrInvalidRequest
	}
	return service.deps.Wallet.Experience(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID})
}

func (service *Service) ApplyReferral(scope Scope, idempotencyKey, code string) (wallet.ReferralProfile, bool, error) {
	if !validScope(scope) {
		return wallet.ReferralProfile{}, false, ErrInvalidRequest
	}
	return service.deps.Wallet.ApplyReferral(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey, code)
}

func (service *Service) CreateWalletRefill(ctx context.Context, scope Scope, idempotencyKey, offerID string, method payment.Method) (WalletRefillResult, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(offerID) {
		return WalletRefillResult{}, false, ErrInvalidRequest
	}
	offer, err := service.deps.Wallet.RefillOffer(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, offerID)
	if err != nil || !containsString(offer.PaymentMethods, string(method)) {
		return WalletRefillResult{}, false, ErrPaymentMethod
	}
	fingerprint := offerID + "\x00" + string(method)
	requestKey := "wallet-refill\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	if replay, exists := service.refillReqs[requestKey]; exists {
		service.mu.Unlock()
		if replay.fingerprint != fingerprint {
			return WalletRefillResult{}, false, ErrIdempotencyConflict
		}
		return replay.value, true, nil
	}
	service.mu.Unlock()
	payer := payment.Payer{}
	if service.deps.Payers != nil {
		payer, err = service.deps.Payers.ResolvePayer(ctx, scope)
		if err != nil {
			return WalletRefillResult{}, false, err
		}
	} else if method == payment.MethodPaystack {
		return WalletRefillResult{}, false, ErrPaymentMethod
	}
	reference := deterministicID("wallet-refill", requestKey)
	paymentValue, _, err := service.deps.Payment.CreateWithPayer(ctx, payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-payment", reference, method, payment.Money{AmountMinor: offer.Price.AmountMinor, Currency: offer.Price.Currency}, payer)
	if err != nil {
		return WalletRefillResult{}, false, err
	}
	value := WalletRefillResult{Offer: offer, Payment: paymentValue}
	service.mu.Lock()
	if replay, exists := service.refillReqs[requestKey]; exists {
		service.mu.Unlock()
		if replay.fingerprint != fingerprint {
			return WalletRefillResult{}, false, ErrIdempotencyConflict
		}
		return replay.value, true, nil
	}
	service.refills[paymentValue.ID] = refillProcess{scope: scope, offer: offer}
	service.refillReqs[requestKey] = refillReplay{fingerprint: fingerprint, value: value}
	service.mu.Unlock()
	return value, false, nil
}

func (service *Service) HasWalletRefill(paymentID string) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	_, exists := service.refills[paymentID]
	return exists
}

func (service *Service) FinalizeProviderWalletRefill(paymentID string) (wallet.LedgerEntry, bool, error) {
	if !safeID(paymentID) {
		return wallet.LedgerEntry{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	value, exists := service.refills[paymentID]
	service.mu.Unlock()
	if !exists {
		return wallet.LedgerEntry{}, false, payment.ErrPaymentNotFound
	}
	paymentValue, err := service.deps.Payment.Get(payment.Scope{TenantID: value.scope.TenantID, Country: value.scope.Country, CustomerID: value.scope.CustomerID}, paymentID)
	if err != nil || (paymentValue.Status != payment.StatusCaptured && paymentValue.Status != payment.StatusReconciled) {
		return wallet.LedgerEntry{}, false, payment.ErrReconciliation
	}
	points := value.offer.Points + value.offer.BonusPoints
	return service.deps.Wallet.Credit(wallet.Scope{TenantID: value.scope.TenantID, Country: value.scope.Country, CustomerID: value.scope.CustomerID}, "wallet-refill-credit-"+paymentID, "WALLET_REFILL", paymentID, points, service.clock().UTC().Add(value.offer.ExpiresAfter))
}

func (service *Service) Addresses(scope Scope) ([]Address, error) {
	if !validScope(scope) {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := []Address{}
	for _, value := range service.addresses {
		if ownsAddress(value, scope) {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Default != result[right].Default {
			return result[left].Default
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func (service *Service) CreateAddress(scope Scope, idempotencyKey string, input AddressInput) (Address, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !validAddressInput(input) {
		return Address{}, false, ErrInvalidRequest
	}
	fingerprint := addressFingerprint(input)
	requestKey := "address-create\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.addressReqs[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Address{}, false, ErrIdempotencyConflict
		}
		return replay.value, true, nil
	}
	if input.Default {
		service.clearDefaultAddressLocked(scope)
	}
	value := Address{
		ID: deterministicID("address", requestKey), Label: strings.TrimSpace(input.Label),
		Line1: strings.TrimSpace(input.Line1), Line2: strings.TrimSpace(input.Line2),
		PostalCode: strings.ToUpper(strings.TrimSpace(input.PostalCode)), Locality: strings.TrimSpace(input.Locality),
		Latitude: input.Latitude, Longitude: input.Longitude, Default: input.Default,
		Serviceable: addressServiceable(service.config.PostalZones, scope.Country, input.PostalCode, input.Locality),
		Revision:    1, TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID,
	}
	if !value.Default && !service.hasAddressLocked(scope) {
		value.Default = true
	}
	service.addresses[addressKey(scope, value.ID)] = value
	service.addressReqs[requestKey] = addressReplay{fingerprint: fingerprint, value: value}
	return value, false, nil
}

func (service *Service) UpdateAddress(scope Scope, idempotencyKey, addressID string, expectedRevision int64, input AddressInput) (Address, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(addressID) || expectedRevision < 1 || !validAddressInput(input) {
		return Address{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("%s\x00%d\x00%s", addressID, expectedRevision, addressFingerprint(input))
	requestKey := "address-update\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.addressReqs[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Address{}, false, ErrIdempotencyConflict
		}
		return replay.value, true, nil
	}
	value, exists := service.addresses[addressKey(scope, addressID)]
	if !exists {
		return Address{}, false, ErrAddressNotFound
	}
	if value.Revision != expectedRevision {
		return Address{}, false, ErrAddressConflict
	}
	if input.Default {
		service.clearDefaultAddressLocked(scope)
	}
	value.Label, value.Line1, value.Line2 = strings.TrimSpace(input.Label), strings.TrimSpace(input.Line1), strings.TrimSpace(input.Line2)
	value.PostalCode, value.Locality = strings.ToUpper(strings.TrimSpace(input.PostalCode)), strings.TrimSpace(input.Locality)
	value.Latitude, value.Longitude, value.Default = input.Latitude, input.Longitude, input.Default
	value.Serviceable = addressServiceable(service.config.PostalZones, scope.Country, value.PostalCode, value.Locality)
	value.Revision++
	service.addresses[addressKey(scope, addressID)] = value
	service.addressReqs[requestKey] = addressReplay{fingerprint: fingerprint, value: value}
	return value, false, nil
}

func (service *Service) DeleteAddress(scope Scope, idempotencyKey, addressID string, expectedRevision int64) (bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(addressID) || expectedRevision < 1 {
		return false, ErrInvalidRequest
	}
	requestKey := "address-delete\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.addressReqs[requestKey]; exists {
		if replay.fingerprint != addressID+"\x00"+fmt.Sprint(expectedRevision) {
			return false, ErrIdempotencyConflict
		}
		return true, nil
	}
	key := addressKey(scope, addressID)
	value, exists := service.addresses[key]
	if !exists {
		return false, ErrAddressNotFound
	}
	if value.Revision != expectedRevision {
		return false, ErrAddressConflict
	}
	delete(service.addresses, key)
	service.addressReqs[requestKey] = addressReplay{fingerprint: addressID + "\x00" + fmt.Sprint(expectedRevision), value: value}
	if value.Default {
		service.promoteDefaultAddressLocked(scope)
	}
	return false, nil
}

func (service *Service) DeliverySlots(scope Scope) ([]DeliverySlot, error) {
	if !validScope(scope) {
		return nil, ErrInvalidRequest
	}
	now := service.clock().UTC()
	result := []DeliverySlot{}
	for _, value := range service.config.Slots {
		if value.Country == scope.Country && value.Capacity > 0 && value.WindowStart.After(now) {
			result = append(result, value)
		}
	}
	return result, nil
}

func (service *Service) Quote(ctx context.Context, scope Scope, idempotencyKey string, request QuoteRequest) (Quote, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !validQuoteRequest(request) {
		return Quote{}, false, ErrInvalidRequest
	}
	fingerprint := quoteFingerprint(request)
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	if replay, exists := service.quoteReqs[requestKey]; exists {
		service.mu.Unlock()
		if replay.fingerprint != fingerprint {
			return Quote{}, false, ErrIdempotencyConflict
		}
		return cloneQuote(replay.value), true, nil
	}
	service.mu.Unlock()
	value, err := service.calculate(ctx, scope, idempotencyKey, request)
	if err != nil {
		return Quote{}, false, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.quoteReqs[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Quote{}, false, ErrIdempotencyConflict
		}
		return cloneQuote(replay.value), true, nil
	}
	service.quotes[value.ID] = cloneQuote(value)
	service.quoteReqs[requestKey] = quoteReplay{fingerprint: fingerprint, value: cloneQuote(value)}
	return cloneQuote(value), false, nil
}

func (service *Service) Place(ctx context.Context, scope Scope, idempotencyKey, quoteID string, method payment.Method) (PlaceResult, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(quoteID) {
		return PlaceResult{}, false, ErrInvalidRequest
	}
	fingerprint := quoteID + "\x00" + string(method)
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	for {
		service.mu.Lock()
		if replay, exists := service.placeReqs[requestKey]; exists {
			service.mu.Unlock()
			if replay.fingerprint != fingerprint {
				return PlaceResult{}, false, ErrIdempotencyConflict
			}
			return clonePlaceResult(replay.value), true, nil
		}
		if call, exists := service.inflight[requestKey]; exists {
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return PlaceResult{}, false, ctx.Err()
			case <-call.done:
				continue
			}
		}
		service.inflight[requestKey] = &placeCall{done: make(chan struct{})}
		service.mu.Unlock()
		break
	}
	value, err := service.place(ctx, scope, idempotencyKey, quoteID, method)
	service.mu.Lock()
	call := service.inflight[requestKey]
	delete(service.inflight, requestKey)
	if err == nil {
		service.placeReqs[requestKey] = placeReplay{fingerprint: fingerprint, value: clonePlaceResult(value)}
		walletDebitID := ""
		if value.WalletDebit != nil {
			walletDebitID = value.WalletDebit.ID
		}
		service.processes[value.Payment.ID] = process{scope: scope, reservationID: value.Reservation.ID, orderID: value.Order.ID, walletDebitID: walletDebitID}
	}
	close(call.done)
	service.mu.Unlock()
	return value, false, err
}

func (service *Service) place(ctx context.Context, scope Scope, idempotencyKey, quoteID string, method payment.Method) (PlaceResult, error) {
	service.mu.Lock()
	quoted, exists := service.quotes[quoteID]
	service.mu.Unlock()
	if !exists || quoted.scope != scope {
		return PlaceResult{}, ErrQuoteNotFound
	}
	now := service.clock().UTC()
	if !now.Before(quoted.ExpiresAt) {
		return PlaceResult{}, ErrQuoteExpired
	}
	if !containsMethod(quoted.PaymentMethods, method) {
		return PlaceResult{}, ErrPaymentMethod
	}
	repriced, err := service.calculate(ctx, scope, "place-"+idempotencyKey, QuoteRequest{CartRevision: quoted.CartRevision, AddressID: quoted.Address.ID, DeliverySlot: quoted.Delivery.ID, PromotionCode: quoted.PromotionCode, WalletPoints: quoted.WalletPointsRedeemed})
	if err != nil || quoteValueFingerprint(repriced) != quoteValueFingerprint(quoted) {
		return PlaceResult{}, ErrQuoteStale
	}
	checkoutReference := deterministicID("checkout", scopeKey(scope)+"\x00"+idempotencyKey)
	reservationLines := make([]inventory.Line, 0, len(quoted.Items))
	for _, line := range quoted.Items {
		reservationLines = append(reservationLines, inventory.Line{VariantID: line.VariantID, Quantity: line.Quantity})
	}
	inventoryScope := inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}
	reservation, _, err := service.deps.Inventory.Reserve(inventoryScope, idempotencyKey+"-stock", checkoutReference, reservationLines, now.Add(service.policy(scope.Country).ReservationTTL))
	if err != nil {
		return PlaceResult{}, err
	}
	var debit *wallet.LedgerEntry
	walletScope := wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	if quoted.WalletPointsRedeemed > 0 {
		entry, _, redeemErr := service.deps.Wallet.Redeem(walletScope, idempotencyKey+"-wallet", checkoutReference, quoted.WalletPointsRedeemed)
		if redeemErr != nil {
			_, _ = service.deps.Inventory.Release(inventoryScope, reservation.ID)
			return PlaceResult{}, redeemErr
		}
		debit = &entry
	}
	paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	payer := payment.Payer{}
	if service.deps.Payers != nil {
		payer, err = service.deps.Payers.ResolvePayer(ctx, scope)
		if err != nil {
			service.compensate(scope, idempotencyKey, checkoutReference, reservation.ID, debit)
			return PlaceResult{}, err
		}
	} else if method == payment.MethodPaystack {
		service.compensate(scope, idempotencyKey, checkoutReference, reservation.ID, debit)
		return PlaceResult{}, ErrPaymentMethod
	}
	paymentValue, _, err := service.deps.Payment.CreateWithPayer(ctx, paymentScope, idempotencyKey+"-payment", checkoutReference, method, payment.Money{AmountMinor: quoted.Total.AmountMinor, Currency: quoted.Total.Currency}, payer)
	if err != nil {
		service.compensate(scope, idempotencyKey, checkoutReference, reservation.ID, debit)
		return PlaceResult{}, err
	}
	snapshot := service.orderSnapshot(quoted, reservation.ID, paymentValue)
	orderScope := order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	orderValue, _, err := service.deps.Orders.Create(orderScope, idempotencyKey+"-order", snapshot, paymentValue.Status == payment.StatusCaptured || paymentValue.Status == payment.StatusReconciled)
	if err != nil {
		service.compensate(scope, idempotencyKey, checkoutReference, reservation.ID, debit)
		return PlaceResult{}, err
	}
	if orderValue.Status == order.StatusPlaced {
		if _, err = service.deps.Inventory.Commit(inventoryScope, reservation.ID); err != nil {
			return PlaceResult{}, err
		}
		reservation, _ = service.deps.Inventory.Commit(inventoryScope, reservation.ID)
	}
	service.drainOrderNotifications(ctx)
	return PlaceResult{Quote: quoted, Reservation: reservation, Payment: paymentValue, Order: orderValue, WalletDebit: debit}, nil
}

func (service *Service) FinalizeCapturedPayment(scope Scope, idempotencyKey, paymentID string) (order.Order, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(paymentID) {
		return order.Order{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	value, exists := service.processes[paymentID]
	service.mu.Unlock()
	if !exists || value.scope != scope {
		return order.Order{}, false, payment.ErrPaymentNotFound
	}
	paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	paymentValue, err := service.deps.Payment.Get(paymentScope, paymentID)
	if err != nil || (paymentValue.Status != payment.StatusCaptured && paymentValue.Status != payment.StatusReconciled) {
		return order.Order{}, false, payment.ErrReconciliation
	}
	if _, err := service.deps.Inventory.Commit(inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}, value.reservationID); err != nil {
		return order.Order{}, false, err
	}
	orderScope := order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	current, err := service.deps.Orders.Get(orderScope, value.orderID)
	if err != nil {
		return order.Order{}, false, err
	}
	if current.Status == order.StatusPlaced {
		return current, true, nil
	}
	placed, replayed, err := service.deps.Orders.Transition(orderScope, idempotencyKey, current.ID, current.Revision, order.StatusPlaced, "PLATFORM", "Payment captured")
	if err == nil {
		_, _ = service.deps.Wallet.ActivateReferral(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, paymentID)
		service.drainOrderNotifications(context.Background())
	}
	return placed, replayed, err
}

// FinalizeProviderPayment is restricted to the signed provider-webhook path.
// It resolves customer scope from the checkout process instead of trusting
// caller-supplied identity headers.
func (service *Service) FinalizeProviderPayment(idempotencyKey, paymentID string) (order.Order, bool, error) {
	if !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(paymentID) {
		return order.Order{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	value, exists := service.processes[paymentID]
	service.mu.Unlock()
	if !exists {
		return order.Order{}, false, payment.ErrPaymentNotFound
	}
	return service.FinalizeCapturedPayment(value.scope, idempotencyKey, paymentID)
}

// ApproveReturn performs the complete return-receipt workflow. Provider
// refunds are submitted before local restock and wallet restoration; every
// local step uses a deterministic idempotency key so a worker can safely retry
// after any interruption.
func (service *Service) ApproveReturn(ctx context.Context, scope Scope, idempotencyKey, orderID string, expectedRevision int64, reason string) (ReturnResult, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderID) || expectedRevision < 1 || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return ReturnResult{}, false, ErrInvalidRequest
	}
	orderScope := order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	current, err := service.deps.Orders.Get(orderScope, orderID)
	if err != nil {
		return ReturnResult{}, false, err
	}
	if current.Status == order.StatusRefunded {
		return ReturnResult{Order: current}, true, nil
	}
	approved, replayed, err := service.deps.Orders.DecideReturn(orderScope, idempotencyKey+"-decision", orderID, expectedRevision, true, reason)
	if err != nil {
		return ReturnResult{}, false, err
	}
	result, err := service.receiveApprovedReturn(ctx, scope, idempotencyKey, approved)
	return result, replayed, err
}

func (service *Service) receiveApprovedReturn(ctx context.Context, scope Scope, idempotencyKey string, approved order.Order) (ReturnResult, error) {
	if approved.Return == nil || approved.Status != order.StatusReturnApproved {
		return ReturnResult{}, order.ErrInvalidTransition
	}
	orderValueScope := order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	service.mu.Lock()
	checkoutProcess, exists := service.processes[approved.Snapshot.PaymentID]
	service.mu.Unlock()
	if !exists || checkoutProcess.scope != scope || checkoutProcess.orderID != approved.ID || checkoutProcess.reservationID != approved.Snapshot.ReservationID {
		return ReturnResult{}, payment.ErrPaymentNotFound
	}
	paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	paymentValue, err := service.deps.Payment.Get(paymentScope, approved.Snapshot.PaymentID)
	if err != nil {
		return ReturnResult{}, err
	}
	walletPoints, walletMinor, err := service.refundTenderSplit(approved, checkoutProcess, paymentValue.Method)
	if err != nil {
		return ReturnResult{}, err
	}
	providerMinor := approved.Return.RefundAmount.AmountMinor - walletMinor
	var providerRefund *payment.Payment
	if providerMinor > 0 {
		if paymentValue.Method == payment.MethodCOD && paymentValue.Status == payment.StatusAuthorisationPending {
			paymentValue, err = service.deps.Payment.MarkCODCollected(paymentScope, paymentValue.ID)
			if err != nil {
				return ReturnResult{}, err
			}
		}
		value, refundErr := service.deps.Payment.RequestRefundWithProvider(ctx, paymentScope, paymentValue.ID, payment.Money{AmountMinor: providerMinor, Currency: approved.Return.RefundAmount.Currency}, approved.Return.Reason)
		if refundErr != nil {
			return ReturnResult{Order: approved, Payment: &value, Pending: true}, refundErr
		}
		providerRefund = &value
	}
	lines := make([]inventory.Line, 0, len(approved.Return.Lines))
	for _, line := range approved.Return.Lines {
		lines = append(lines, inventory.Line{VariantID: line.VariantID, Quantity: line.Quantity})
	}
	restocked, _, err := service.deps.Inventory.Restock(inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}, idempotencyKey+"-restock", checkoutProcess.reservationID, lines)
	if err != nil {
		return ReturnResult{Order: approved, Payment: providerRefund, Pending: true}, err
	}
	var walletRefund *wallet.LedgerEntry
	if walletPoints > 0 {
		entry, _, refundErr := service.deps.Wallet.RefundDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet", checkoutProcess.walletDebitID, approved.Return.ID, walletPoints)
		if refundErr != nil {
			return ReturnResult{Order: approved, Payment: providerRefund, Inventory: restocked, Pending: true}, refundErr
		}
		walletRefund = &entry
	}
	returned, _, err := service.deps.Orders.Transition(order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-received", approved.ID, approved.Revision, order.StatusReturned, "PLATFORM", "Returned items received")
	if err != nil {
		return ReturnResult{Order: approved, Payment: providerRefund, WalletRefund: walletRefund, Inventory: restocked, Pending: true}, err
	}
	pending := providerRefund != nil && providerRefund.Status != payment.StatusRefunded
	if !pending {
		refundReference := "refund-wallet-" + returned.Return.ID
		if providerRefund != nil {
			refundReference = providerRefund.ProviderRefundReference
			if refundReference == "" {
				refundReference = providerRefund.ID + "-refund"
			}
		} else if walletRefund != nil {
			refundReference = walletRefund.ID
		}
		returned, _, err = service.deps.Orders.RecordRefund(orderValueScope, idempotencyKey+"-refunded", returned.ID, returned.Revision, refundReference, returned.Return.RefundAmount)
		if err != nil {
			return ReturnResult{Order: returned, Payment: providerRefund, WalletRefund: walletRefund, Inventory: restocked, Pending: true}, err
		}
	}
	service.drainOrderNotifications(ctx)
	return ReturnResult{Order: returned, Payment: providerRefund, WalletRefund: walletRefund, Inventory: restocked, Pending: pending}, nil
}

// FinalizeProviderRefund completes the order ledger after a signed provider
// webhook confirms the asynchronous refund.
func (service *Service) FinalizeProviderRefund(idempotencyKey, paymentID string) (order.Order, bool, error) {
	if !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(paymentID) {
		return order.Order{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	checkoutProcess, exists := service.processes[paymentID]
	service.mu.Unlock()
	if !exists {
		return order.Order{}, false, payment.ErrPaymentNotFound
	}
	scope := checkoutProcess.scope
	paymentValue, err := service.deps.Payment.Get(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, paymentID)
	if err != nil || paymentValue.Status != payment.StatusRefunded {
		return order.Order{}, false, payment.ErrReconciliation
	}
	orderScope := order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	current, err := service.deps.Orders.Get(orderScope, checkoutProcess.orderID)
	if err != nil {
		return order.Order{}, false, err
	}
	if current.Status == order.StatusRefunded && current.Return != nil {
		return current, true, nil
	}
	if current.Status == order.StatusCancelled {
		return current, true, nil
	}
	if current.Status != order.StatusReturned || current.Return == nil {
		return order.Order{}, false, order.ErrInvalidTransition
	}
	reference := paymentValue.ProviderRefundReference
	if reference == "" {
		reference = paymentValue.ID + "-refund"
	}
	refunded, replayed, err := service.deps.Orders.RecordRefund(orderScope, idempotencyKey, current.ID, current.Revision, reference, current.Return.RefundAmount)
	if err == nil {
		service.drainOrderNotifications(context.Background())
	}
	return refunded, replayed, err
}

// FinalizeCancellation is the platform/admin half of a customer cancellation
// request. It cancels an uncaptured payment or submits a full provider refund,
// restores inventory and reverses redeemed points exactly once.
func (service *Service) FinalizeCancellation(ctx context.Context, scope Scope, idempotencyKey, orderID string, expectedRevision int64, reason string) (CancellationResult, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderID) || expectedRevision < 1 || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return CancellationResult{}, false, ErrInvalidRequest
	}
	orderValueScope := order.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	current, err := service.deps.Orders.Get(orderValueScope, orderID)
	if err != nil {
		return CancellationResult{}, false, err
	}
	if current.Status == order.StatusCancelled {
		paymentValue, paymentErr := service.deps.Payment.Get(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, current.Snapshot.PaymentID)
		return CancellationResult{Order: current, Payment: paymentValue, RefundPending: paymentValue.Status == payment.StatusRefundSubmitted}, true, paymentErr
	}
	if current.Status != order.StatusCancelRequested || current.Revision != expectedRevision {
		return CancellationResult{}, false, order.ErrRevisionConflict
	}
	service.mu.Lock()
	checkoutProcess, exists := service.processes[current.Snapshot.PaymentID]
	service.mu.Unlock()
	if !exists || checkoutProcess.scope != scope {
		return CancellationResult{}, false, payment.ErrPaymentNotFound
	}
	paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
	paymentValue, err := service.deps.Payment.Get(paymentScope, current.Snapshot.PaymentID)
	if err != nil {
		return CancellationResult{}, false, err
	}
	refundPending := false
	switch paymentValue.Status {
	case payment.StatusCaptured, payment.StatusReconciled, payment.StatusRefundFailed:
		paymentValue, err = service.deps.Payment.RequestRefundWithProvider(ctx, paymentScope, paymentValue.ID, paymentValue.Amount, reason)
		if err != nil {
			return CancellationResult{Order: current, Payment: paymentValue, RefundPending: true}, false, err
		}
		refundPending = paymentValue.Status != payment.StatusRefunded
	default:
		paymentValue, err = service.deps.Payment.CancelUncaptured(paymentScope, paymentValue.ID)
		if err != nil {
			return CancellationResult{}, false, err
		}
	}
	inventoryScope := inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}
	reservation, err := service.deps.Inventory.Get(inventoryScope, checkoutProcess.reservationID)
	if err != nil {
		return CancellationResult{Order: current, Payment: paymentValue, RefundPending: refundPending}, false, err
	}
	switch reservation.State {
	case inventory.StateReserved:
		reservation, err = service.deps.Inventory.Release(inventoryScope, reservation.ID)
	case inventory.StateCommitted:
		reservation, _, err = service.deps.Inventory.Restock(inventoryScope, idempotencyKey+"-restock", reservation.ID, reservation.Lines)
	case inventory.StateReleased:
		err = nil
	default:
		err = inventory.ErrInvalidTransition
	}
	if err != nil {
		return CancellationResult{Order: current, Payment: paymentValue, Inventory: reservation, RefundPending: refundPending}, false, err
	}
	var walletRefund *wallet.LedgerEntry
	if checkoutProcess.walletDebitID != "" {
		entry, _, reverseErr := service.deps.Wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet", checkoutProcess.walletDebitID, current.ID)
		if reverseErr != nil && !errors.Is(reverseErr, wallet.ErrAlreadyReversed) {
			return CancellationResult{Order: current, Payment: paymentValue, Inventory: reservation, RefundPending: refundPending}, false, reverseErr
		}
		if reverseErr == nil {
			walletRefund = &entry
		}
	}
	cancelled, replayed, err := service.deps.Orders.Transition(orderValueScope, idempotencyKey+"-cancelled", current.ID, current.Revision, order.StatusCancelled, "PLATFORM", reason)
	if err == nil {
		service.drainOrderNotifications(ctx)
	}
	return CancellationResult{Order: cancelled, Payment: paymentValue, WalletRefund: walletRefund, Inventory: reservation, RefundPending: refundPending}, replayed, err
}

func (service *Service) drainOrderNotifications(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	_, _ = service.deps.Orders.ProcessNotifications(ctx, 100)
}

func (service *Service) refundTenderSplit(value order.Order, checkoutProcess process, method payment.Method) (int64, int64, error) {
	if value.Return == nil {
		return 0, 0, order.ErrReturnNotFound
	}
	walletApplied := value.Snapshot.WalletApplied.AmountMinor
	if walletApplied == 0 {
		return 0, 0, nil
	}
	if checkoutProcess.walletDebitID == "" {
		return 0, 0, wallet.ErrEntryNotFound
	}
	pool := value.Snapshot.Subtotal.AmountMinor - value.Snapshot.Discount.AmountMinor + value.Snapshot.Tax.AmountMinor
	if pool < 1 {
		return 0, 0, ErrInvalidRequest
	}
	walletMinor := value.Return.RefundAmount.AmountMinor * walletApplied / pool
	if value.Return.RefundAmount.AmountMinor == pool || method == payment.MethodWallet {
		walletMinor = value.Return.RefundAmount.AmountMinor
		if walletMinor > walletApplied {
			walletMinor = walletApplied
		}
	}
	pointValue := service.policy(checkoutProcess.scope.Country).WalletPointValueMinor
	if pointValue < 1 {
		return 0, 0, ErrInvalidRequest
	}
	points := walletMinor / pointValue
	walletMinor = points * pointValue
	return points, walletMinor, nil
}

func (service *Service) calculate(ctx context.Context, scope Scope, seed string, request QuoteRequest) (Quote, error) {
	cart, err := service.deps.Cart.Price(ctx, commerce.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, request.CartRevision)
	if err != nil || len(cart.Items) == 0 {
		return Quote{}, ErrQuoteStale
	}
	for _, line := range cart.Items {
		if !line.Available {
			return Quote{}, ErrQuoteStale
		}
	}
	address, found := service.address(scope, request.AddressID)
	if !found || !address.Serviceable {
		return Quote{}, ErrAddressNotFound
	}
	slot, found := service.slot(scope.Country, request.DeliverySlot)
	if !found || !slot.WindowStart.After(service.clock().UTC()) {
		return Quote{}, ErrSlotNotAvailable
	}
	policy := service.policy(scope.Country)
	discount := int64(0)
	if request.PromotionCode != "" {
		promotion, promotionFound := service.promotion(scope.Country, request.PromotionCode)
		now := service.clock().UTC()
		if !promotionFound || cart.Subtotal.AmountMinor < promotion.MinimumSubtotal || now.Before(promotion.StartsAt) || !now.Before(promotion.EndsAt) {
			return Quote{}, ErrPromotionInvalid
		}
		discount = cart.Subtotal.AmountMinor * promotion.DiscountBasisPts / 10000
		if promotion.MaximumDiscount > 0 && discount > promotion.MaximumDiscount {
			discount = promotion.MaximumDiscount
		}
	}
	taxable := cart.Subtotal.AmountMinor - discount
	tax := (taxable*policy.TaxBasisPoints + 5000) / 10000
	fees := policy.PlatformFeeMinor + slot.Fee.AmountMinor
	gross := taxable + tax + fees
	account, err := service.deps.Wallet.Account(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID})
	if err != nil {
		return Quote{}, err
	}
	points := request.WalletPoints
	if points > account.Balance {
		points = account.Balance
	}
	maxPoints := gross / policy.WalletPointValueMinor
	if points > maxPoints {
		points = maxPoints
	}
	walletApplied := points * policy.WalletPointValueMinor
	total := gross - walletApplied
	methods := paymentMethods(scope.Country, total, policy.WalletMode)
	if policy.WalletMode == WalletPointsOnly && total != 0 {
		return Quote{}, ErrPaymentMethod
	}
	now := service.clock().UTC()
	warnings := []string{}
	if cart.PricingStatus == "REPRICED" {
		warnings = append(warnings, "CART_REPRICED")
	}
	return Quote{
		ID: deterministicID("quote", scopeKey(scope)+"\x00"+seed), CartRevision: cart.Revision, Items: append([]commerce.CartLine(nil), cart.Items...), Address: address, Delivery: slot,
		Subtotal: money(cart.Subtotal.AmountMinor, cart.Subtotal.Currency), Discount: money(discount, cart.Subtotal.Currency), Tax: money(tax, cart.Subtotal.Currency), Fees: money(fees, cart.Subtotal.Currency), WalletApplied: money(walletApplied, cart.Subtotal.Currency), WalletPointsRedeemed: points, Total: money(total, cart.Subtotal.Currency),
		PromotionCode: request.PromotionCode, PricingPolicyVersion: policy.Version, PaymentMethods: methods, Warnings: warnings, AllowedActions: []string{"PLACE_ORDER", "EDIT_CHECKOUT"}, ExpiresAt: now.Add(policy.QuoteTTL), CreatedAt: now, scope: scope,
	}, nil
}

func (service *Service) compensate(scope Scope, idempotencyKey, reference, reservationID string, debit *wallet.LedgerEntry) {
	_, _ = service.deps.Inventory.Release(inventory.Scope{TenantID: scope.TenantID, Country: scope.Country}, reservationID)
	if debit != nil {
		_, _, _ = service.deps.Wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet-reversal", debit.ID, reference)
	}
}

func (service *Service) orderSnapshot(value Quote, reservationID string, paymentValue payment.Payment) order.CheckoutSnapshot {
	discounts := allocate(value.Items, value.Discount.AmountMinor)
	taxes := allocate(value.Items, value.Tax.AmountMinor)
	lines := make([]order.LineSnapshot, 0, len(value.Items))
	for index, item := range value.Items {
		lines = append(lines, order.LineSnapshot{VariantID: item.VariantID, ItemID: item.ItemID, VendorID: item.VendorID, ItemName: item.ItemName, VariantName: item.VariantName, Quantity: item.Quantity, UnitPrice: order.Money{AmountMinor: item.UnitPrice.AmountMinor, Currency: item.UnitPrice.Currency}, LineTotal: order.Money{AmountMinor: item.LineTotal.AmountMinor, Currency: item.LineTotal.Currency}, DiscountMinor: discounts[index], TaxMinor: taxes[index]})
	}
	return order.CheckoutSnapshot{CartRevision: value.CartRevision, Lines: lines, Address: order.AddressSnapshot{AddressID: value.Address.ID, Label: value.Address.Label, PostalCode: value.Address.PostalCode, Locality: value.Address.Locality}, Delivery: order.DeliverySnapshot{SlotID: value.Delivery.ID, WindowStart: value.Delivery.WindowStart, WindowEnd: value.Delivery.WindowEnd, Fee: order.Money{AmountMinor: value.Delivery.Fee.AmountMinor, Currency: value.Delivery.Fee.Currency}}, Subtotal: orderMoney(value.Subtotal), Discount: orderMoney(value.Discount), Tax: orderMoney(value.Tax), Fees: orderMoney(value.Fees), WalletApplied: orderMoney(value.WalletApplied), Total: orderMoney(value.Total), PromotionCode: value.PromotionCode, PricingPolicyVersion: value.PricingPolicyVersion, ReservationID: reservationID, PaymentID: paymentValue.ID, PaymentMethod: string(paymentValue.Method)}
}

func allocate(items []commerce.CartLine, total int64) []int64 {
	result := make([]int64, len(items))
	if total == 0 || len(items) == 0 {
		return result
	}
	var subtotal int64
	for _, item := range items {
		subtotal += item.LineTotal.AmountMinor
	}
	var allocated int64
	for index, item := range items {
		if index == len(items)-1 {
			result[index] = total - allocated
		} else {
			result[index] = total * item.LineTotal.AmountMinor / subtotal
			allocated += result[index]
		}
	}
	return result
}

func validConfiguration(value Configuration) bool {
	if len(value.Policies) == 0 {
		return false
	}
	for _, policy := range value.Policies {
		if len(policy.Country) != 2 || !safeID(policy.Version) || policy.TaxBasisPoints < 0 || policy.TaxBasisPoints > 10000 || policy.PlatformFeeMinor < 0 || policy.WalletPointValueMinor < 1 || (policy.WalletMode != WalletHybrid && policy.WalletMode != WalletPointsOnly) || policy.QuoteTTL <= 0 || policy.QuoteTTL > 30*time.Minute || policy.ReservationTTL <= 0 || policy.ReservationTTL > 30*time.Minute {
			return false
		}
	}
	for _, address := range value.Addresses {
		if !safeID(address.ID) || !validScope(Scope{TenantID: address.TenantID, Country: address.Country, CustomerID: address.CustomerID}) || address.Label == "" || strings.TrimSpace(address.Line1) == "" || address.PostalCode == "" || address.Locality == "" {
			return false
		}
	}
	for _, slot := range value.Slots {
		if !safeID(slot.ID) || len(slot.Country) != 2 || !slot.WindowEnd.After(slot.WindowStart) || slot.Capacity < 0 || slot.Fee.AmountMinor < 0 || len(slot.Fee.Currency) != 3 {
			return false
		}
	}
	for _, promotion := range value.Promotions {
		if !safeID(promotion.Code) || len(promotion.Country) != 2 || promotion.MinimumSubtotal < 0 || promotion.DiscountBasisPts < 0 || promotion.DiscountBasisPts > 10000 || promotion.MaximumDiscount < 0 || !promotion.EndsAt.After(promotion.StartsAt) {
			return false
		}
	}
	for _, zone := range value.PostalZones {
		if len(zone.Country) != 2 || strings.ToUpper(zone.Country) != zone.Country || strings.TrimSpace(zone.PostalCode) == "" || strings.TrimSpace(zone.Locality) == "" {
			return false
		}
	}
	return true
}

func validQuoteRequest(value QuoteRequest) bool {
	return value.CartRevision >= 0 && safeID(value.AddressID) && safeID(value.DeliverySlot) && (value.PromotionCode == "" || safeID(value.PromotionCode)) && value.WalletPoints >= 0
}
func validScope(value Scope) bool {
	return safeID(value.TenantID) && safeID(value.CustomerID) && len(value.Country) == 2 && value.Country == strings.ToUpper(value.Country)
}
func safeID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}
func scopeKey(value Scope) string {
	return value.TenantID + "\x00" + value.Country + "\x00" + value.CustomerID
}
func ownsAddress(value Address, scope Scope) bool {
	return value.TenantID == scope.TenantID && value.Country == scope.Country && value.CustomerID == scope.CustomerID
}
func (service *Service) address(scope Scope, id string) (Address, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, found := service.addresses[addressKey(scope, id)]
	return value, found
}
func (service *Service) slot(country, id string) (DeliverySlot, bool) {
	for _, value := range service.config.Slots {
		if value.ID == id && value.Country == country && value.Capacity > 0 {
			return value, true
		}
	}
	return DeliverySlot{}, false
}
func (service *Service) promotion(country, code string) (Promotion, bool) {
	for _, value := range service.config.Promotions {
		if value.Code == code && value.Country == country {
			return value, true
		}
	}
	return Promotion{}, false
}

func validAddressInput(value AddressInput) bool {
	return len(strings.TrimSpace(value.Label)) >= 1 && len(strings.TrimSpace(value.Label)) <= 60 &&
		len(strings.TrimSpace(value.Line1)) >= 3 && len(strings.TrimSpace(value.Line1)) <= 240 &&
		len(strings.TrimSpace(value.Line2)) <= 240 && len(strings.TrimSpace(value.PostalCode)) >= 3 && len(strings.TrimSpace(value.PostalCode)) <= 12 &&
		len(strings.TrimSpace(value.Locality)) >= 2 && len(strings.TrimSpace(value.Locality)) <= 100 &&
		!math.IsNaN(value.Latitude) && !math.IsNaN(value.Longitude) && value.Latitude >= -90 && value.Latitude <= 90 && value.Longitude >= -180 && value.Longitude <= 180
}

func addressFingerprint(value AddressInput) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%.6f\x00%.6f\x00%t", strings.TrimSpace(value.Label), strings.TrimSpace(value.Line1), strings.TrimSpace(value.Line2), strings.ToUpper(strings.TrimSpace(value.PostalCode)), strings.TrimSpace(value.Locality), value.Latitude, value.Longitude, value.Default)))
	return hex.EncodeToString(digest[:])
}

func addressServiceable(zones []PostalZone, country, postalCode, locality string) bool {
	postalCode = strings.ToUpper(strings.TrimSpace(postalCode))
	locality = strings.TrimSpace(locality)
	for _, zone := range zones {
		if zone.Country == country && strings.EqualFold(zone.PostalCode, postalCode) && strings.EqualFold(zone.Locality, locality) {
			return true
		}
	}
	return len(zones) == 0
}

func addressKey(scope Scope, id string) string { return scopeKey(scope) + "\x00" + id }

func (service *Service) hasAddressLocked(scope Scope) bool {
	for _, value := range service.addresses {
		if ownsAddress(value, scope) {
			return true
		}
	}
	return false
}

func (service *Service) clearDefaultAddressLocked(scope Scope) {
	for key, value := range service.addresses {
		if ownsAddress(value, scope) && value.Default {
			value.Default = false
			value.Revision++
			service.addresses[key] = value
		}
	}
}

func (service *Service) promoteDefaultAddressLocked(scope Scope) {
	keys := []string{}
	for key, value := range service.addresses {
		if ownsAddress(value, scope) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	value := service.addresses[keys[0]]
	value.Default = true
	value.Revision++
	service.addresses[keys[0]] = value
}
func (service *Service) policy(country string) PricingPolicy {
	for _, value := range service.config.Policies {
		if value.Country == country {
			return value
		}
	}
	return PricingPolicy{}
}
func paymentMethods(country string, total int64, mode WalletMode) []payment.Method {
	if total == 0 {
		return []payment.Method{payment.MethodWallet}
	}
	if mode == WalletPointsOnly {
		return []payment.Method{}
	}
	if country == "IN" {
		return []payment.Method{payment.MethodRazorpay, payment.MethodCOD}
	}
	if country == "NG" {
		return []payment.Method{payment.MethodPaystack, payment.MethodCOD}
	}
	return []payment.Method{payment.MethodCOD}
}
func containsMethod(values []payment.Method, expected payment.Method) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func money(amount int64, currency string) Money {
	return Money{AmountMinor: amount, Currency: currency}
}
func orderMoney(value Money) order.Money {
	return order.Money{AmountMinor: value.AmountMinor, Currency: value.Currency}
}
func deterministicID(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}
func quoteFingerprint(value QuoteRequest) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%d", value.CartRevision, value.AddressID, value.DeliverySlot, value.PromotionCode, value.WalletPoints)
}
func quoteValueFingerprint(value Quote) string {
	return fmt.Sprintf("%+v\x00%+v\x00%+v\x00%+v\x00%+v\x00%+v\x00%d\x00%+v\x00%s\x00%s", value.Items, value.Subtotal, value.Discount, value.Tax, value.Fees, value.WalletApplied, value.WalletPointsRedeemed, value.Total, value.PaymentMethods, value.PricingPolicyVersion)
}
func cloneQuote(value Quote) Quote {
	value.Items = append([]commerce.CartLine(nil), value.Items...)
	value.PaymentMethods = append([]payment.Method(nil), value.PaymentMethods...)
	value.Warnings = append([]string(nil), value.Warnings...)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}
func clonePlaceResult(value PlaceResult) PlaceResult {
	value.Quote = cloneQuote(value.Quote)
	if value.WalletDebit != nil {
		copy := *value.WalletDebit
		value.WalletDebit = &copy
	}
	return value
}
