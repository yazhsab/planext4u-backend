package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

type process struct {
	scope         Scope
	reservationID string
	orderID       string
}

type Service struct {
	deps      Dependencies
	config    Configuration
	clock     func() time.Time
	mu        sync.Mutex
	quotes    map[string]Quote
	quoteReqs map[string]quoteReplay
	placeReqs map[string]placeReplay
	inflight  map[string]*placeCall
	processes map[string]process
}

func NewService(dependencies Dependencies, configuration Configuration, clock func() time.Time) (*Service, error) {
	if dependencies.Cart == nil || dependencies.Inventory == nil || dependencies.Wallet == nil || dependencies.Payment == nil || dependencies.Orders == nil || clock == nil || !validConfiguration(configuration) {
		return nil, ErrInvalidRequest
	}
	return &Service{deps: dependencies, config: configuration, clock: clock, quotes: map[string]Quote{}, quoteReqs: map[string]quoteReplay{}, placeReqs: map[string]placeReplay{}, inflight: map[string]*placeCall{}, processes: map[string]process{}}, nil
}

func (service *Service) Addresses(scope Scope) ([]Address, error) {
	if !validScope(scope) {
		return nil, ErrInvalidRequest
	}
	result := []Address{}
	for _, value := range service.config.Addresses {
		if ownsAddress(value, scope) {
			result = append(result, value)
		}
	}
	return result, nil
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
		service.processes[value.Payment.ID] = process{scope: scope, reservationID: value.Reservation.ID, orderID: value.Order.ID}
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
	paymentValue, _, err := service.deps.Payment.Create(paymentScope, idempotencyKey+"-payment", checkoutReference, method, payment.Money{AmountMinor: quoted.Total.AmountMinor, Currency: quoted.Total.Currency})
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
	return service.deps.Orders.Transition(orderScope, idempotencyKey, current.ID, current.Revision, order.StatusPlaced, "PLATFORM", "Payment captured")
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
	if !found {
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
		if !safeID(address.ID) || !validScope(Scope{TenantID: address.TenantID, Country: address.Country, CustomerID: address.CustomerID}) || address.Label == "" || address.PostalCode == "" || address.Locality == "" {
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
	for _, value := range service.config.Addresses {
		if value.ID == id && ownsAddress(value, scope) {
			return value, true
		}
	}
	return Address{}, false
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
