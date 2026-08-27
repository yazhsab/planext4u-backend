package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type replayRecord struct {
	fingerprint string
	value       Payment
}

type eventRecord struct {
	digest string
	value  Payment
}

type Service struct {
	clock    func() time.Time
	secrets  map[Method][]byte
	mu       sync.Mutex
	payments map[string]Payment
	requests map[string]replayRecord
	events   map[string]eventRecord
}

func NewService(clock func() time.Time, providerSecrets map[Method][]byte) (*Service, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	secrets := map[Method][]byte{}
	for method, secret := range providerSecrets {
		if (method != MethodRazorpay && method != MethodPaystack) || len(secret) < 32 {
			return nil, ErrInvalidRequest
		}
		secrets[method] = append([]byte(nil), secret...)
	}
	return &Service{clock: clock, secrets: secrets, payments: map[string]Payment{}, requests: map[string]replayRecord{}, events: map[string]eventRecord{}}, nil
}

func (service *Service) Create(scope Scope, idempotencyKey, orderReference string, method Method, amount Money) (Payment, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderReference) || !validMoney(amount) || !service.supported(scope.Country, method) {
		return Payment{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("%s\x00%s\x00%d\x00%s", orderReference, method, amount.AmountMinor, amount.Currency)
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.requests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Payment{}, false, ErrIdempotencyConflict
		}
		return clonePayment(replay.value), true, nil
	}
	digest := sha256.Sum256([]byte(requestKey))
	id := "payment-" + hex.EncodeToString(digest[:8])
	now := service.clock().UTC()
	status := StatusProviderOrderCreated
	providerReference := "provider-" + hex.EncodeToString(digest[8:16])
	if method == MethodCOD {
		status = StatusAuthorisationPending
		providerReference = ""
	} else if method == MethodWallet {
		status = StatusCaptured
		providerReference = ""
	}
	value := Payment{ID: id, OrderReference: orderReference, Method: method, Status: status, Amount: amount, ProviderReference: providerReference, AllowedActions: allowedActions(status), CreatedAt: now, UpdatedAt: now, scope: scope}
	service.payments[id] = clonePayment(value)
	service.requests[requestKey] = replayRecord{fingerprint: fingerprint, value: clonePayment(value)}
	return clonePayment(value), false, nil
}

func (service *Service) Get(scope Scope, paymentID string) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.payments[paymentID]
	if !exists || value.scope != scope {
		return Payment{}, ErrPaymentNotFound
	}
	return clonePayment(value), nil
}

func (service *Service) HandleWebhook(method Method, signature string, body []byte) (Payment, bool, error) {
	secret, exists := service.secrets[method]
	if !exists || len(body) == 0 || len(body) > 64*1024 {
		return Payment{}, false, ErrInvalidRequest
	}
	expected := hmac.New(sha256.New, secret)
	_, _ = expected.Write(body)
	provided, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || !hmac.Equal(provided, expected.Sum(nil)) {
		return Payment{}, false, ErrSignatureInvalid
	}
	var event ProviderEvent
	decoderErr := json.Unmarshal(body, &event)
	if decoderErr != nil || !safeID(event.EventID) || !safeID(event.PaymentID) || !safeID(event.ProviderReference) || event.AmountMinor < 0 || len(event.Currency) != 3 {
		return Payment{}, false, ErrInvalidRequest
	}
	bodyDigest := sha256.Sum256(body)
	digest := hex.EncodeToString(bodyDigest[:])
	service.mu.Lock()
	defer service.mu.Unlock()
	if recorded, duplicate := service.events[string(method)+"\x00"+event.EventID]; duplicate {
		if recorded.digest != digest {
			return Payment{}, false, ErrProviderEventReuse
		}
		return clonePayment(recorded.value), true, nil
	}
	value, found := service.payments[event.PaymentID]
	expectedAmount := value.Amount
	if value.Status == StatusRefundSubmitted && value.RefundAmount != nil {
		expectedAmount = *value.RefundAmount
	}
	if !found || value.Method != method || value.ProviderReference != event.ProviderReference || expectedAmount.AmountMinor != event.AmountMinor || expectedAmount.Currency != event.Currency {
		return Payment{}, false, ErrReconciliation
	}
	if !validProviderTransition(value.Status, event.Status) {
		return Payment{}, false, ErrInvalidTransition
	}
	value.Status = event.Status
	value.AllowedActions = allowedActions(event.Status)
	value.UpdatedAt = service.clock().UTC()
	service.payments[value.ID] = value
	service.events[string(method)+"\x00"+event.EventID] = eventRecord{digest: digest, value: clonePayment(value)}
	return clonePayment(value), false, nil
}

func (service *Service) Reconcile(scope Scope, paymentID string, providerCaptured bool) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.payments[paymentID]
	if !exists || value.scope != scope {
		return Payment{}, ErrPaymentNotFound
	}
	if value.Status == StatusReconciled {
		return clonePayment(value), nil
	}
	if value.Status != StatusCaptured || !providerCaptured {
		return Payment{}, ErrReconciliation
	}
	value.Status = StatusReconciled
	value.AllowedActions = allowedActions(value.Status)
	value.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = value
	return clonePayment(value), nil
}

func (service *Service) RequestRefund(scope Scope, paymentID string, amount Money) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) || !validMoney(amount) {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.payments[paymentID]
	if !exists || value.scope != scope {
		return Payment{}, ErrPaymentNotFound
	}
	if value.Status != StatusCaptured && value.Status != StatusReconciled {
		return Payment{}, ErrInvalidTransition
	}
	if amount.AmountMinor < 1 || amount.Currency != value.Amount.Currency || amount.AmountMinor > value.Amount.AmountMinor {
		return Payment{}, ErrInvalidRequest
	}
	value.Status = StatusRefundSubmitted
	refundAmount := amount
	value.RefundAmount = &refundAmount
	value.AllowedActions = allowedActions(value.Status)
	value.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = value
	return clonePayment(value), nil
}

func (service *Service) MarkCODCollected(scope Scope, paymentID string) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.payments[paymentID]
	if !exists || value.scope != scope {
		return Payment{}, ErrPaymentNotFound
	}
	if value.Method != MethodCOD || value.Status != StatusAuthorisationPending {
		return Payment{}, ErrInvalidTransition
	}
	value.Status = StatusCaptured
	value.AllowedActions = allowedActions(value.Status)
	value.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = value
	return clonePayment(value), nil
}

func (service *Service) supported(country string, method Method) bool {
	switch method {
	case MethodCOD, MethodWallet:
		return true
	case MethodRazorpay:
		_, configured := service.secrets[method]
		return country == "IN" && configured
	case MethodPaystack:
		_, configured := service.secrets[method]
		return country == "NG" && configured
	default:
		return false
	}
}

func validProviderTransition(current, target Status) bool {
	switch target {
	case StatusAuthorised:
		return current == StatusProviderOrderCreated || current == StatusAuthorisationPending
	case StatusCaptured:
		return current == StatusProviderOrderCreated || current == StatusAuthorised || current == StatusFailedRetryable
	case StatusFailedRetryable, StatusFailedFinal:
		return current == StatusProviderOrderCreated || current == StatusAuthorisationPending || current == StatusAuthorised
	case StatusRefunded, StatusRefundFailed:
		return current == StatusRefundSubmitted
	default:
		return false
	}
}

func allowedActions(status Status) []string {
	switch status {
	case StatusProviderOrderCreated, StatusAuthorisationPending, StatusFailedRetryable:
		return []string{"CHECK_STATUS", "RETRY"}
	case StatusCaptured:
		return []string{"CHECK_STATUS", "RECONCILE"}
	case StatusReconciled:
		return []string{"VIEW_ORDER", "REQUEST_REFUND"}
	case StatusRefundSubmitted:
		return []string{"CHECK_REFUND"}
	case StatusRefunded:
		return []string{"VIEW_REFUND"}
	default:
		return []string{"CONTACT_SUPPORT"}
	}
}

func validMoney(value Money) bool {
	return value.AmountMinor >= 0 && len(value.Currency) == 3 && strings.ToUpper(value.Currency) == value.Currency
}
func validScope(scope Scope) bool {
	return safeID(scope.TenantID) && safeID(scope.CustomerID) && len(scope.Country) == 2 && strings.ToUpper(scope.Country) == scope.Country
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
func clonePayment(value Payment) Payment {
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	if value.RefundAmount != nil {
		copy := *value.RefundAmount
		value.RefundAmount = &copy
	}
	return value
}

func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
