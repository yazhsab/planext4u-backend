package order

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type replayRecord struct {
	fingerprint string
	value       Order
}

type Service struct {
	clock    func() time.Time
	mu       sync.Mutex
	orders   map[string]Order
	requests map[string]replayRecord
}

func NewService(clock func() time.Time) (*Service, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{clock: clock, orders: map[string]Order{}, requests: map[string]replayRecord{}}, nil
}

func (service *Service) Create(scope Scope, idempotencyKey string, snapshot CheckoutSnapshot, paymentCaptured bool) (Order, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !validSnapshot(snapshot) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := snapshotFingerprint(snapshot, paymentCaptured)
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	if value, replayed, err := service.replayLocked(requestKey, fingerprint); replayed || err != nil {
		return value, replayed, err
	}
	digest := sha256.Sum256([]byte(requestKey))
	now := service.clock().UTC()
	status := StatusPendingPayment
	if paymentCaptured || snapshot.PaymentMethod == "COD" {
		status = StatusPlaced
	}
	value := Order{
		ID: "order-" + hex.EncodeToString(digest[:8]), Revision: 1, Status: status, Snapshot: cloneSnapshot(snapshot),
		AllowedActions: allowedActions(status, nil, nil), Timeline: []TimelineEvent{{Status: status, Actor: "PLATFORM", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now, scope: scope,
	}
	service.orders[value.ID] = cloneOrder(value)
	service.requests[requestKey] = replayRecord{fingerprint: fingerprint, value: cloneOrder(value)}
	return cloneOrder(value), false, nil
}

func (service *Service) Get(scope Scope, orderID string) (Order, error) {
	if !validScope(scope) || !safeID(orderID) {
		return Order{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.orders[orderID]
	if !exists || value.scope != scope {
		return Order{}, ErrOrderNotFound
	}
	return cloneOrder(value), nil
}

func (service *Service) List(scope Scope) ([]Order, error) {
	if !validScope(scope) {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := make([]Order, 0, len(service.orders))
	for _, value := range service.orders {
		if value.scope == scope {
			values = append(values, cloneOrder(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) Transition(scope Scope, idempotencyKey, orderID string, expectedRevision int64, target Status, actor, reason string) (Order, bool, error) {
	if !validScope(scope) || !validCommand(idempotencyKey, orderID, actor) || expectedRevision < 1 || (reason != "" && (len(reason) > 500 || strings.TrimSpace(reason) != reason)) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("transition\x00%s\x00%d\x00%s\x00%s\x00%s", orderID, expectedRevision, target, actor, reason)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if !canTransition(value.Status, target, actor) {
			return ErrInvalidTransition
		}
		value.Status = target
		value.Timeline = append(value.Timeline, TimelineEvent{Status: target, Actor: actor, Reason: reason, CreatedAt: service.clock().UTC()})
		return nil
	})
}

func (service *Service) RequestReturn(scope Scope, idempotencyKey, orderID string, expectedRevision int64, lines []ReturnLine, reason string) (Order, bool, error) {
	normalized, err := normalizeReturn(lines)
	if err != nil || !safeReason(reason) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("return\x00%s\x00%d\x00%v\x00%s", orderID, expectedRevision, normalized, reason)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusCompleted && value.Status != StatusDeliveredPendingConfirmation {
			return ErrInvalidTransition
		}
		refund, err := refundable(value.Snapshot, normalized)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(value.ID + "\x00" + idempotencyKey))
		now := service.clock().UTC()
		value.Return = &ReturnCase{ID: "return-" + hex.EncodeToString(digest[:8]), Status: StatusReturnRequested, Lines: normalized, Reason: reason, RefundAmount: refund, CreatedAt: now, UpdatedAt: now}
		value.Status = StatusReturnRequested
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: "CUSTOMER", Reason: reason, CreatedAt: now})
		return nil
	})
}

func (service *Service) DecideReturn(scope Scope, idempotencyKey, orderID string, expectedRevision int64, approved bool, reason string) (Order, bool, error) {
	target := StatusReturnRejected
	if approved {
		target = StatusReturnApproved
	}
	fingerprint := fmt.Sprintf("return-decision\x00%s\x00%d\x00%t\x00%s", orderID, expectedRevision, approved, reason)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusReturnRequested || value.Return == nil {
			return ErrInvalidTransition
		}
		now := service.clock().UTC()
		value.Status = target
		value.Return.Status = target
		value.Return.UpdatedAt = now
		value.Timeline = append(value.Timeline, TimelineEvent{Status: target, Actor: "ADMIN", Reason: reason, CreatedAt: now})
		return nil
	})
}

func (service *Service) RecordPOD(scope Scope, idempotencyKey, orderID string, expectedRevision int64, proof Proof) (Order, bool, error) {
	if !validProof(proof) {
		return Order{}, false, ErrProofRequired
	}
	fingerprint := fmt.Sprintf("pod\x00%s\x00%d\x00%+v", orderID, expectedRevision, proof)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusOutForDelivery {
			return ErrInvalidTransition
		}
		now := service.clock().UTC()
		copy := proof
		value.Proof = &copy
		value.Status = StatusDeliveredPendingConfirmation
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: "RIDER", CreatedAt: now})
		return nil
	})
}

func (service *Service) RecordRefund(scope Scope, idempotencyKey, orderID string, expectedRevision int64, refundReference string, amount Money) (Order, bool, error) {
	if !safeID(refundReference) || !validMoney(amount) {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("refund\x00%s\x00%d\x00%s\x00%d", orderID, expectedRevision, refundReference, amount.AmountMinor)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Return == nil || value.Status != StatusReturned || amount != value.Return.RefundAmount {
			return ErrInvalidTransition
		}
		now := service.clock().UTC()
		value.Return.RefundReference = refundReference
		value.Return.Status = StatusRefunded
		value.Return.UpdatedAt = now
		value.Status = StatusRefunded
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: "PLATFORM", CreatedAt: now})
		return nil
	})
}

func (service *Service) Rate(scope Scope, idempotencyKey, orderID string, expectedRevision int64, score int, comment string) (Order, bool, error) {
	if score < 1 || score > 5 || len(comment) > 1000 || strings.TrimSpace(comment) != comment {
		return Order{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("rating\x00%s\x00%d\x00%d\x00%s", orderID, expectedRevision, score, comment)
	return service.mutate(scope, idempotencyKey, fingerprint, orderID, expectedRevision, func(value *Order) error {
		if value.Status != StatusCompleted || value.Rating != nil {
			return ErrInvalidTransition
		}
		value.Rating = &Rating{Score: score, Comment: comment, CreatedAt: service.clock().UTC()}
		return nil
	})
}

func (service *Service) mutate(scope Scope, idempotencyKey, fingerprint, orderID string, expectedRevision int64, apply func(*Order) error) (Order, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderID) || expectedRevision < 1 {
		return Order{}, false, ErrInvalidRequest
	}
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	if value, replayed, err := service.replayLocked(requestKey, fingerprint); replayed || err != nil {
		return value, replayed, err
	}
	value, exists := service.orders[orderID]
	if !exists || value.scope != scope {
		return Order{}, false, ErrOrderNotFound
	}
	if value.Revision != expectedRevision {
		return Order{}, false, ErrRevisionConflict
	}
	if err := apply(&value); err != nil {
		return Order{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	value.AllowedActions = allowedActions(value.Status, value.Return, value.Rating)
	service.orders[orderID] = cloneOrder(value)
	service.requests[requestKey] = replayRecord{fingerprint: fingerprint, value: cloneOrder(value)}
	return cloneOrder(value), false, nil
}

func (service *Service) replayLocked(key, fingerprint string) (Order, bool, error) {
	replay, exists := service.requests[key]
	if !exists {
		return Order{}, false, nil
	}
	if replay.fingerprint != fingerprint {
		return Order{}, false, ErrIdempotencyConflict
	}
	return cloneOrder(replay.value), true, nil
}

func canTransition(current, target Status, actor string) bool {
	allowed := map[Status]map[Status]string{
		StatusPendingPayment:   {StatusPlaced: "PLATFORM", StatusCancelled: "PLATFORM"},
		StatusPlaced:           {StatusAccepted: "VENDOR", StatusRejected: "VENDOR", StatusCancelRequested: "CUSTOMER"},
		StatusAccepted:         {StatusPacking: "VENDOR", StatusCancelRequested: "CUSTOMER"},
		StatusPacking:          {StatusReadyForHandover: "VENDOR", StatusCancelRequested: "CUSTOMER"},
		StatusReadyForHandover: {StatusAssigned: "PLATFORM"}, StatusAssigned: {StatusPickedUp: "RIDER"},
		StatusPickedUp: {StatusOutForDelivery: "RIDER"}, StatusDeliveredPendingConfirmation: {StatusCompleted: "CUSTOMER"},
		StatusCancelRequested: {StatusCancelled: "PLATFORM"}, StatusReturnApproved: {StatusReturned: "PLATFORM"},
	}
	required, exists := allowed[current][target]
	return exists && required == actor
}

func allowedActions(status Status, returnCase *ReturnCase, rating *Rating) []string {
	switch status {
	case StatusPendingPayment:
		return []string{"CHECK_PAYMENT", "RETRY_PAYMENT", "CANCEL"}
	case StatusPlaced, StatusAccepted, StatusPacking:
		return []string{"VIEW_TRACKING", "REQUEST_CANCELLATION", "CONTACT_SUPPORT"}
	case StatusReadyForHandover, StatusAssigned, StatusPickedUp, StatusOutForDelivery:
		return []string{"VIEW_TRACKING", "CONTACT_RIDER", "CONTACT_SUPPORT"}
	case StatusDeliveredPendingConfirmation:
		return []string{"CONFIRM_DELIVERY", "REQUEST_RETURN", "REPORT_ISSUE"}
	case StatusCompleted:
		actions := []string{"VIEW_RECEIPT", "REQUEST_RETURN"}
		if rating == nil {
			actions = append(actions, "RATE_ORDER")
		}
		return actions
	case StatusReturnRequested, StatusReturnApproved, StatusReturned:
		return []string{"VIEW_RETURN", "CONTACT_SUPPORT"}
	case StatusReturnRejected:
		return []string{"VIEW_RETURN", "APPEAL_RETURN", "CONTACT_SUPPORT"}
	case StatusRefunded:
		return []string{"VIEW_REFUND", "VIEW_RECEIPT"}
	default:
		_ = returnCase
		return []string{"VIEW_DETAILS", "CONTACT_SUPPORT"}
	}
}

func validSnapshot(value CheckoutSnapshot) bool {
	if value.CartRevision < 0 || len(value.Lines) == 0 || !safeID(value.Address.AddressID) || !safeID(value.Delivery.SlotID) || !value.Delivery.WindowEnd.After(value.Delivery.WindowStart) || !safeID(value.PricingPolicyVersion) || !safeID(value.ReservationID) || !safeID(value.PaymentID) || !safeID(value.PaymentMethod) {
		return false
	}
	for _, money := range []Money{value.Subtotal, value.Discount, value.Tax, value.Fees, value.WalletApplied, value.Total, value.Delivery.Fee} {
		if !validMoney(money) || money.Currency != value.Total.Currency {
			return false
		}
	}
	var subtotal int64
	for _, line := range value.Lines {
		if !safeID(line.VariantID) || !safeID(line.ItemID) || !safeID(line.VendorID) || line.Quantity < 1 || !validMoney(line.UnitPrice) || !validMoney(line.LineTotal) || line.LineTotal.AmountMinor != line.UnitPrice.AmountMinor*int64(line.Quantity) || line.TaxMinor < 0 || line.DiscountMinor < 0 {
			return false
		}
		subtotal += line.LineTotal.AmountMinor
	}
	return subtotal == value.Subtotal.AmountMinor && value.Total.AmountMinor == value.Subtotal.AmountMinor-value.Discount.AmountMinor+value.Tax.AmountMinor+value.Fees.AmountMinor-value.WalletApplied.AmountMinor
}

func refundable(snapshot CheckoutSnapshot, lines []ReturnLine) (Money, error) {
	requested := map[string]int{}
	for _, line := range lines {
		requested[line.VariantID] = line.Quantity
	}
	var amount int64
	for _, line := range snapshot.Lines {
		quantity := requested[line.VariantID]
		if quantity == 0 {
			continue
		}
		if quantity > line.Quantity {
			return Money{}, ErrInvalidRequest
		}
		amount += line.UnitPrice.AmountMinor*int64(quantity) - (line.DiscountMinor*int64(quantity))/int64(line.Quantity) + (line.TaxMinor*int64(quantity))/int64(line.Quantity)
		delete(requested, line.VariantID)
	}
	if len(requested) != 0 || amount < 0 {
		return Money{}, ErrInvalidRequest
	}
	return Money{AmountMinor: amount, Currency: snapshot.Total.Currency}, nil
}

func normalizeReturn(lines []ReturnLine) ([]ReturnLine, error) {
	if len(lines) == 0 || len(lines) > 100 {
		return nil, ErrInvalidRequest
	}
	values := map[string]int{}
	for _, line := range lines {
		if !safeID(line.VariantID) || line.Quantity < 1 || line.Quantity > 999 {
			return nil, ErrInvalidRequest
		}
		values[line.VariantID] += line.Quantity
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ReturnLine, 0, len(ids))
	for _, id := range ids {
		result = append(result, ReturnLine{VariantID: id, Quantity: values[id]})
	}
	return result, nil
}

func validProof(value Proof) bool {
	return safeID(value.PolicyVersion) && (safeID(value.PhotoAssetID) || value.OTPVerified || (!value.SignedAt.IsZero() && safeReason(value.RecipientName)))
}
func validCommand(key, orderID, actor string) bool {
	return safeID(key) && len(key) >= 16 && safeID(orderID) && (actor == "CUSTOMER" || actor == "VENDOR" || actor == "RIDER" || actor == "PLATFORM" || actor == "ADMIN")
}
func validMoney(value Money) bool {
	return value.AmountMinor >= 0 && len(value.Currency) == 3 && strings.ToUpper(value.Currency) == value.Currency
}
func validScope(scope Scope) bool {
	return safeID(scope.TenantID) && safeID(scope.CustomerID) && len(scope.Country) == 2 && strings.ToUpper(scope.Country) == scope.Country
}
func safeReason(value string) bool {
	return value != "" && len(value) <= 500 && strings.TrimSpace(value) == value
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
func scopeKey(scope Scope) string {
	return scope.TenantID + "\x00" + scope.Country + "\x00" + scope.CustomerID
}
func snapshotFingerprint(value CheckoutSnapshot, paid bool) string {
	return fmt.Sprintf("%+v\x00%t", value, paid)
}

func cloneSnapshot(value CheckoutSnapshot) CheckoutSnapshot {
	value.Lines = append([]LineSnapshot(nil), value.Lines...)
	return value
}
func cloneOrder(value Order) Order {
	value.Snapshot = cloneSnapshot(value.Snapshot)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	value.Timeline = append([]TimelineEvent(nil), value.Timeline...)
	if value.Proof != nil {
		copy := *value.Proof
		value.Proof = &copy
	}
	if value.Return != nil {
		copy := *value.Return
		copy.Lines = append([]ReturnLine(nil), value.Return.Lines...)
		value.Return = &copy
	}
	if value.Rating != nil {
		copy := *value.Rating
		value.Rating = &copy
	}
	return value
}
