package payment

import (
	"context"
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

type createCall struct {
	done chan struct{}
}

type Service struct {
	clock     func() time.Time
	secrets   map[Method][]byte
	providers map[Method]ProviderInitializer
	mu        sync.Mutex
	payments  map[string]Payment
	requests  map[string]replayRecord
	events    map[string]eventRecord
	inflight  map[string]*createCall
}

func NewService(clock func() time.Time, providerSecrets map[Method][]byte) (*Service, error) {
	return NewServiceWithProviders(clock, providerSecrets, nil)
}

func NewServiceWithProviders(clock func() time.Time, providerSecrets map[Method][]byte, providers map[Method]ProviderInitializer) (*Service, error) {
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
	configuredProviders := map[Method]ProviderInitializer{}
	for method, provider := range providers {
		if (method != MethodRazorpay && method != MethodPaystack) || provider == nil {
			return nil, ErrInvalidRequest
		}
		if _, configured := secrets[method]; !configured {
			return nil, ErrInvalidRequest
		}
		configuredProviders[method] = provider
	}
	return &Service{clock: clock, secrets: secrets, providers: configuredProviders, payments: map[string]Payment{}, requests: map[string]replayRecord{}, events: map[string]eventRecord{}, inflight: map[string]*createCall{}}, nil
}

func (service *Service) Create(scope Scope, idempotencyKey, orderReference string, method Method, amount Money) (Payment, bool, error) {
	return service.CreateWithPayer(context.Background(), scope, idempotencyKey, orderReference, method, amount, Payer{})
}

func (service *Service) CreateWithPayer(ctx context.Context, scope Scope, idempotencyKey, orderReference string, method Method, amount Money, payer Payer) (Payment, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderReference) || !validMoney(amount) || !service.supported(scope.Country, method) {
		return Payment{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("%s\x00%s\x00%d\x00%s", orderReference, method, amount.AmountMinor, amount.Currency)
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	for {
		service.mu.Lock()
		if replay, exists := service.requests[requestKey]; exists {
			service.mu.Unlock()
			if replay.fingerprint != fingerprint {
				return Payment{}, false, ErrIdempotencyConflict
			}
			return clonePayment(replay.value), true, nil
		}
		if call, exists := service.inflight[requestKey]; exists {
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return Payment{}, false, ctx.Err()
			case <-call.done:
				continue
			}
		}
		service.inflight[requestKey] = &createCall{done: make(chan struct{})}
		service.mu.Unlock()
		break
	}
	digest := sha256.Sum256([]byte(requestKey))
	id := "payment-" + hex.EncodeToString(digest[:8])
	now := service.clock().UTC()
	status := StatusProviderOrderCreated
	providerReference := "provider-" + hex.EncodeToString(digest[8:16])
	var handoff *ClientHandoff
	var initializeErr error
	if method == MethodCOD {
		status = StatusAuthorisationPending
		providerReference = ""
	} else if method == MethodWallet {
		status = StatusCaptured
		providerReference = ""
	} else if provider := service.providers[method]; provider != nil {
		var session ProviderSession
		session, initializeErr = provider.Initialize(ctx, ProviderInitialization{
			PaymentID: id, OrderReference: orderReference, Amount: amount, Payer: payer,
		})
		if initializeErr == nil && validProviderSession(method, session) {
			providerReference = session.ProviderReference
			value := session.ClientHandoff
			handoff = &value
		} else if initializeErr == nil {
			initializeErr = ErrProviderResponse
		}
	}
	service.mu.Lock()
	call := service.inflight[requestKey]
	delete(service.inflight, requestKey)
	if initializeErr != nil {
		close(call.done)
		service.mu.Unlock()
		return Payment{}, false, initializeErr
	}
	value := Payment{ID: id, OrderReference: orderReference, Method: method, Status: status, Amount: amount, ProviderReference: providerReference, ClientHandoff: handoff, AllowedActions: allowedActions(status), CreatedAt: now, UpdatedAt: now, scope: scope, payer: payer}
	service.payments[id] = clonePayment(value)
	service.requests[requestKey] = replayRecord{fingerprint: fingerprint, value: clonePayment(value)}
	close(call.done)
	service.mu.Unlock()
	return clonePayment(value), false, nil
}

func (service *Service) RetryProvider(ctx context.Context, scope Scope, idempotencyKey, paymentID string) (Payment, bool, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(paymentID) {
		return Payment{}, false, ErrInvalidRequest
	}
	requestKey := "retry\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	for {
		service.mu.Lock()
		if replay, exists := service.requests[requestKey]; exists {
			service.mu.Unlock()
			if replay.fingerprint != paymentID {
				return Payment{}, false, ErrIdempotencyConflict
			}
			return clonePayment(replay.value), true, nil
		}
		value, exists := service.payments[paymentID]
		if !exists || value.scope != scope {
			service.mu.Unlock()
			return Payment{}, false, ErrPaymentNotFound
		}
		if value.Status != StatusFailedRetryable || service.providers[value.Method] == nil {
			service.mu.Unlock()
			return Payment{}, false, ErrInvalidTransition
		}
		if call, exists := service.inflight[requestKey]; exists {
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return Payment{}, false, ctx.Err()
			case <-call.done:
				continue
			}
		}
		service.inflight[requestKey] = &createCall{done: make(chan struct{})}
		service.mu.Unlock()

		digest := sha256.Sum256([]byte(requestKey))
		retryReference := value.OrderReference + "-" + hex.EncodeToString(digest[:4])
		session, err := service.providers[value.Method].Initialize(ctx, ProviderInitialization{
			PaymentID: value.ID, OrderReference: retryReference, Amount: value.Amount, Payer: value.payer,
		})
		if err == nil && !validProviderSession(value.Method, session) {
			err = ErrProviderResponse
		}
		service.mu.Lock()
		call := service.inflight[requestKey]
		delete(service.inflight, requestKey)
		if err != nil {
			close(call.done)
			service.mu.Unlock()
			return Payment{}, false, err
		}
		latest := service.payments[paymentID]
		if latest.Status != StatusFailedRetryable {
			close(call.done)
			service.mu.Unlock()
			return Payment{}, false, ErrInvalidTransition
		}
		latest.Status = StatusProviderOrderCreated
		latest.ProviderReference = session.ProviderReference
		latest.ProviderTransactionReference = ""
		handoff := session.ClientHandoff
		latest.ClientHandoff = &handoff
		latest.AllowedActions = allowedActions(latest.Status)
		latest.UpdatedAt = service.clock().UTC()
		service.payments[paymentID] = clonePayment(latest)
		service.requests[requestKey] = replayRecord{fingerprint: paymentID, value: clonePayment(latest)}
		close(call.done)
		service.mu.Unlock()
		return clonePayment(latest), false, nil
	}
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
	return service.applyProviderEvent(method, event, digest)
}

func (service *Service) applyProviderEvent(method Method, event ProviderEvent, digest string) (Payment, bool, error) {
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
	if (value.Status == StatusRefundRequested || value.Status == StatusRefundSubmitted || value.Status == StatusRefundFailed) && value.RefundAmount != nil {
		expectedAmount = *value.RefundAmount
	}
	providerReferenceMatches := value.ProviderReference == event.ProviderReference
	if event.ProviderReference == "" && value.Status == StatusRefundSubmitted {
		providerReferenceMatches = true
	}
	transactionReferenceMatches := event.ProviderTransactionReference == "" || value.ProviderTransactionReference == "" || value.ProviderTransactionReference == event.ProviderTransactionReference
	if !found || value.Method != method || !providerReferenceMatches || !transactionReferenceMatches || expectedAmount.AmountMinor != event.AmountMinor || expectedAmount.Currency != event.Currency {
		return Payment{}, false, ErrReconciliation
	}
	if !validProviderTransition(value.Status, event.Status) {
		return Payment{}, false, ErrInvalidTransition
	}
	value.Status = event.Status
	if event.ProviderTransactionReference != "" {
		value.ProviderTransactionReference = event.ProviderTransactionReference
	}
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

func (service *Service) ReconcileWithProvider(ctx context.Context, scope Scope, paymentID string) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	value, exists := service.payments[paymentID]
	provider, configured := service.providers[value.Method]
	service.mu.Unlock()
	if !exists || value.scope != scope {
		return Payment{}, ErrPaymentNotFound
	}
	verifier, supported := provider.(ProviderVerifier)
	if !configured || !supported {
		return Payment{}, ErrProviderUnavailable
	}
	verification, err := verifier.Verify(ctx, clonePayment(value))
	if err != nil {
		return Payment{}, err
	}
	if verification.Status != StatusCaptured ||
		verification.Amount != value.Amount ||
		verification.ProviderReference != value.ProviderReference ||
		(value.ProviderTransactionReference != "" && verification.ProviderTransactionReference != value.ProviderTransactionReference) {
		return Payment{}, ErrReconciliation
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	latest, exists := service.payments[paymentID]
	if !exists || latest.scope != scope || latest.ProviderReference != verification.ProviderReference || latest.Amount != verification.Amount {
		return Payment{}, ErrReconciliation
	}
	if latest.Status == StatusReconciled {
		return clonePayment(latest), nil
	}
	if latest.Status != StatusProviderOrderCreated && latest.Status != StatusAuthorisationPending && latest.Status != StatusAuthorised && latest.Status != StatusCaptured {
		return Payment{}, ErrInvalidTransition
	}
	latest.Status = StatusReconciled
	latest.ProviderTransactionReference = verification.ProviderTransactionReference
	latest.AllowedActions = allowedActions(latest.Status)
	latest.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = clonePayment(latest)
	return clonePayment(latest), nil
}

func (service *Service) RequestRefund(scope Scope, paymentID string, amount Money) (Payment, error) {
	return service.RequestRefundWithProvider(context.Background(), scope, paymentID, amount, "Customer return approved")
}

func (service *Service) RequestRefundWithProvider(ctx context.Context, scope Scope, paymentID string, amount Money, reason string) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) || !validMoney(amount) {
		return Payment{}, ErrInvalidRequest
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 500 {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	value, exists := service.payments[paymentID]
	if !exists || value.scope != scope {
		service.mu.Unlock()
		return Payment{}, ErrPaymentNotFound
	}
	if value.RefundAmount != nil && *value.RefundAmount == amount {
		switch value.Status {
		case StatusRefundRequested, StatusRefundSubmitted, StatusRefunded:
			service.mu.Unlock()
			return clonePayment(value), nil
		}
	}
	if value.Status != StatusCaptured && value.Status != StatusReconciled && value.Status != StatusRefundFailed {
		service.mu.Unlock()
		return Payment{}, ErrInvalidTransition
	}
	if amount.AmountMinor < 1 || amount.Currency != value.Amount.Currency || amount.AmountMinor > value.Amount.AmountMinor {
		service.mu.Unlock()
		return Payment{}, ErrInvalidRequest
	}
	provider := service.providers[value.Method]
	refunder, providerRefund := provider.(ProviderRefunder)
	if (value.Method == MethodRazorpay || value.Method == MethodPaystack) && !providerRefund {
		service.mu.Unlock()
		return Payment{}, ErrProviderUnavailable
	}
	value.Status = StatusRefundRequested
	refundAmount := amount
	value.RefundAmount = &refundAmount
	value.AllowedActions = allowedActions(value.Status)
	value.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = clonePayment(value)
	service.mu.Unlock()

	if !providerRefund {
		service.mu.Lock()
		value = service.payments[paymentID]
		value.Status = StatusRefundSubmitted
		value.AllowedActions = allowedActions(value.Status)
		value.UpdatedAt = service.clock().UTC()
		service.payments[paymentID] = clonePayment(value)
		service.mu.Unlock()
		return clonePayment(value), nil
	}
	submission, err := refunder.Refund(ctx, ProviderRefundRequest{
		PaymentID: paymentID, ProviderReference: value.ProviderReference,
		ProviderTransactionReference: value.ProviderTransactionReference,
		Amount:                       amount, Reason: reason,
	})
	service.mu.Lock()
	defer service.mu.Unlock()
	latest, exists := service.payments[paymentID]
	if !exists || latest.scope != scope || latest.Status != StatusRefundRequested || latest.RefundAmount == nil || *latest.RefundAmount != amount {
		return Payment{}, ErrReconciliation
	}
	if err != nil {
		latest.Status = StatusRefundFailed
		latest.AllowedActions = allowedActions(latest.Status)
		latest.UpdatedAt = service.clock().UTC()
		service.payments[paymentID] = clonePayment(latest)
		return clonePayment(latest), err
	}
	if !safeID(submission.ProviderRefundReference) || (submission.Status != StatusRefundSubmitted && submission.Status != StatusRefunded) {
		latest.Status = StatusRefundFailed
		latest.AllowedActions = allowedActions(latest.Status)
		latest.UpdatedAt = service.clock().UTC()
		service.payments[paymentID] = clonePayment(latest)
		return clonePayment(latest), ErrProviderResponse
	}
	latest.Status = submission.Status
	latest.ProviderRefundReference = submission.ProviderRefundReference
	latest.AllowedActions = allowedActions(latest.Status)
	latest.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = clonePayment(latest)
	return clonePayment(latest), nil
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

func (service *Service) CancelUncaptured(scope Scope, paymentID string) (Payment, error) {
	if !validScope(scope) || !safeID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.payments[paymentID]
	if !exists || value.scope != scope {
		return Payment{}, ErrPaymentNotFound
	}
	if value.Status == StatusCancelled {
		return clonePayment(value), nil
	}
	if value.Status != StatusProviderOrderCreated && value.Status != StatusAuthorisationPending && value.Status != StatusAuthorised && value.Status != StatusFailedRetryable && value.Status != StatusFailedFinal {
		return Payment{}, ErrInvalidTransition
	}
	value.Status = StatusCancelled
	value.AllowedActions = allowedActions(value.Status)
	value.UpdatedAt = service.clock().UTC()
	service.payments[paymentID] = clonePayment(value)
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
		return current == StatusRefundRequested || current == StatusRefundSubmitted
	default:
		return false
	}
}

func allowedActions(status Status) []string {
	switch status {
	case StatusProviderOrderCreated, StatusAuthorisationPending, StatusFailedRetryable:
		if status == StatusFailedRetryable {
			return []string{"CHECK_STATUS", "RETRY"}
		}
		return []string{"CHECK_STATUS"}
	case StatusCaptured:
		return []string{"CHECK_STATUS", "RECONCILE"}
	case StatusReconciled:
		return []string{"VIEW_ORDER", "REQUEST_REFUND"}
	case StatusRefundRequested, StatusRefundSubmitted:
		return []string{"CHECK_REFUND"}
	case StatusRefundFailed:
		return []string{"CONTACT_SUPPORT", "RETRY_REFUND"}
	case StatusRefunded:
		return []string{"VIEW_REFUND"}
	case StatusCancelled:
		return []string{"VIEW_ORDER"}
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
	if value.ClientHandoff != nil {
		copy := *value.ClientHandoff
		value.ClientHandoff = &copy
	}
	return value
}

func validProviderSession(method Method, value ProviderSession) bool {
	if !safeID(value.ProviderReference) {
		return false
	}
	handoff := value.ClientHandoff
	switch method {
	case MethodRazorpay:
		return handoff.Type == "RAZORPAY_CHECKOUT" &&
			validPublicKey(handoff.PublicKey, "rzp_") &&
			handoff.ProviderOrderID == value.ProviderReference &&
			safeID(handoff.ProviderOrderID) && handoff.AccessCode == "" && handoff.AuthorizationURL == ""
	case MethodPaystack:
		if handoff.Type != "PAYSTACK_CHECKOUT" || !validPublicKey(handoff.PublicKey, "pk_") ||
			!safeOpaque(handoff.AccessCode, 8, 512) || handoff.ProviderOrderID != "" {
			return false
		}
		url := strings.TrimSpace(handoff.AuthorizationURL)
		return strings.HasPrefix(url, "https://checkout.paystack.com/") && len(url) <= 2048
	default:
		return false
	}
}

func validPublicKey(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && safeOpaque(value, 8, 256)
}

func safeOpaque(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
