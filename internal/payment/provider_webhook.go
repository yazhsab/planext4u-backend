package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// HandleProviderWebhook accepts the native provider payload and signature.
// It normalizes only the states that can change Planext4u's payment ledger.
func (service *Service) HandleProviderWebhook(method Method, signature, providerEventID string, body []byte) (Payment, bool, error) {
	secret, exists := service.secrets[method]
	if !exists || len(body) == 0 || len(body) > 64*1024 {
		return Payment{}, false, ErrInvalidRequest
	}
	if !verifyProviderSignature(method, secret, signature, body) {
		return Payment{}, false, ErrSignatureInvalid
	}
	event, err := normalizeProviderEvent(method, providerEventID, body)
	if err != nil {
		return Payment{}, false, err
	}
	digest := sha256.Sum256(body)
	return service.applyProviderEvent(method, event, hex.EncodeToString(digest[:]))
}

func verifyProviderSignature(method Method, secret []byte, signature string, body []byte) bool {
	provided, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	var expected []byte
	switch method {
	case MethodRazorpay:
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(body)
		expected = mac.Sum(nil)
	case MethodPaystack:
		mac := hmac.New(sha512.New, secret)
		_, _ = mac.Write(body)
		expected = mac.Sum(nil)
	default:
		return false
	}
	return hmac.Equal(provided, expected)
}

func normalizeProviderEvent(method Method, providerEventID string, body []byte) (ProviderEvent, error) {
	switch method {
	case MethodRazorpay:
		return normalizeRazorpayEvent(providerEventID, body)
	case MethodPaystack:
		return normalizePaystackEvent(body)
	default:
		return ProviderEvent{}, ErrInvalidRequest
	}
}

func normalizeRazorpayEvent(providerEventID string, body []byte) (ProviderEvent, error) {
	var envelope struct {
		Event   string `json:"event"`
		Payload struct {
			Payment struct {
				Entity struct {
					ID       string            `json:"id"`
					OrderID  string            `json:"order_id"`
					Status   string            `json:"status"`
					Amount   int64             `json:"amount"`
					Currency string            `json:"currency"`
					Notes    map[string]string `json:"notes"`
				} `json:"entity"`
			} `json:"payment"`
			Refund struct {
				Entity struct {
					ID        string            `json:"id"`
					PaymentID string            `json:"payment_id"`
					Status    string            `json:"status"`
					Amount    int64             `json:"amount"`
					Currency  string            `json:"currency"`
					Notes     map[string]string `json:"notes"`
				} `json:"entity"`
			} `json:"refund"`
		} `json:"payload"`
	}
	if decodeStrictJSON(body, &envelope) != nil || !safeOpaque(providerEventID, 8, 256) {
		return ProviderEvent{}, ErrInvalidRequest
	}
	status := Status("")
	entity := envelope.Payload.Payment.Entity
	event := ProviderEvent{EventID: providerEventID, PaymentID: entity.Notes["planext4u_payment_id"], ProviderReference: entity.OrderID, ProviderTransactionReference: entity.ID, AmountMinor: entity.Amount, Currency: entity.Currency}
	switch envelope.Event {
	case "payment.authorized":
		status = StatusAuthorised
	case "payment.captured":
		status = StatusCaptured
	case "payment.failed":
		status = StatusFailedRetryable
	case "refund.processed", "refund.failed":
		refund := envelope.Payload.Refund.Entity
		event.PaymentID = refund.Notes["planext4u_payment_id"]
		event.ProviderReference = ""
		event.ProviderTransactionReference = refund.PaymentID
		event.AmountMinor = refund.Amount
		event.Currency = refund.Currency
		if envelope.Event == "refund.processed" {
			status = StatusRefunded
		} else {
			status = StatusRefundFailed
		}
	default:
		return ProviderEvent{}, ErrInvalidRequest
	}
	event.Status = status
	if !validNormalizedEvent(event) {
		return ProviderEvent{}, ErrInvalidRequest
	}
	return event, nil
}

func normalizePaystackEvent(body []byte) (ProviderEvent, error) {
	var envelope struct {
		Event string `json:"event"`
		Data  struct {
			ID        json.Number `json:"id"`
			Reference string      `json:"reference"`
			Status    string      `json:"status"`
			Amount    int64       `json:"amount"`
			Currency  string      `json:"currency"`
			Metadata  struct {
				PaymentID string `json:"planext4u_payment_id"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if decodeStrictJSON(body, &envelope) != nil {
		return ProviderEvent{}, ErrInvalidRequest
	}
	status := Status("")
	switch envelope.Event {
	case "charge.success":
		status = StatusCaptured
	case "charge.failed":
		status = StatusFailedRetryable
	case "refund.processed":
		status = StatusRefunded
	case "refund.failed":
		status = StatusRefundFailed
	default:
		return ProviderEvent{}, ErrInvalidRequest
	}
	event := ProviderEvent{
		EventID:   "paystack-" + envelope.Data.ID.String() + "-" + strings.ReplaceAll(envelope.Event, ".", "-"),
		PaymentID: envelope.Data.Metadata.PaymentID, ProviderReference: envelope.Data.Reference,
		Status: status, AmountMinor: envelope.Data.Amount, Currency: strings.ToUpper(envelope.Data.Currency),
	}
	if !validNormalizedEvent(event) {
		return ProviderEvent{}, ErrInvalidRequest
	}
	return event, nil
}

func validNormalizedEvent(value ProviderEvent) bool {
	providerReferenceValid := safeID(value.ProviderReference) ||
		(value.ProviderReference == "" && (value.Status == StatusRefunded || value.Status == StatusRefundFailed))
	transactionReferenceValid := value.ProviderTransactionReference == "" || safeID(value.ProviderTransactionReference)
	return safeID(value.EventID) && safeID(value.PaymentID) && providerReferenceValid && transactionReferenceValid &&
		value.AmountMinor >= 0 && len(value.Currency) == 3 && value.Currency == strings.ToUpper(value.Currency)
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidRequest
	}
	return nil
}
