package payment

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid payment request")
	ErrPaymentNotFound     = errors.New("payment not found")
	ErrSignatureInvalid    = errors.New("payment signature invalid")
	ErrProviderEventReuse  = errors.New("provider event id reused")
	ErrIdempotencyConflict = errors.New("payment idempotency conflict")
	ErrInvalidTransition   = errors.New("invalid payment transition")
	ErrReconciliation      = errors.New("payment reconciliation exception")
	ErrProviderUnavailable = errors.New("payment provider unavailable")
	ErrProviderResponse    = errors.New("payment provider response invalid")
)

type Scope struct {
	TenantID   string
	Country    string
	CustomerID string
}

type Method string

const (
	MethodRazorpay Method = "RAZORPAY"
	MethodPaystack Method = "PAYSTACK"
	MethodCOD      Method = "COD"
	MethodWallet   Method = "WALLET"
)

type Status string

const (
	StatusProviderOrderCreated Status = "PROVIDER_ORDER_CREATED"
	StatusAuthorisationPending Status = "AUTHORISATION_PENDING"
	StatusAuthorised           Status = "AUTHORISED"
	StatusCaptured             Status = "CAPTURED"
	StatusReconciled           Status = "RECONCILED"
	StatusFailedRetryable      Status = "FAILED_RETRYABLE"
	StatusFailedFinal          Status = "FAILED_FINAL"
	StatusSignatureInvalid     Status = "SIGNATURE_INVALID"
	StatusRefundRequested      Status = "REFUND_REQUESTED"
	StatusRefundSubmitted      Status = "REFUND_SUBMITTED"
	StatusRefunded             Status = "REFUNDED"
	StatusRefundFailed         Status = "REFUND_FAILED"
	StatusCancelled            Status = "CANCELLED"
)

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// ClientHandoff contains only the public, short-lived values required by a
// provider's native checkout. Provider secrets are never part of this model.
type ClientHandoff struct {
	Type             string `json:"type"`
	PublicKey        string `json:"public_key,omitempty"`
	ProviderOrderID  string `json:"provider_order_id,omitempty"`
	AccessCode       string `json:"access_code,omitempty"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

type Payer struct {
	Email string
	Phone string
	Name  string
}

type ProviderInitialization struct {
	PaymentID      string
	OrderReference string
	Amount         Money
	Payer          Payer
}

type ProviderSession struct {
	ProviderReference string
	ClientHandoff     ClientHandoff
}

// ProviderInitializer is implemented by the Razorpay and Paystack HTTP
// adapters. It is deliberately server-side so merchant credentials never
// cross the mobile API boundary.
type ProviderInitializer interface {
	Initialize(context.Context, ProviderInitialization) (ProviderSession, error)
}

type ProviderVerification struct {
	ProviderReference            string
	ProviderTransactionReference string
	Status                       Status
	Amount                       Money
}

// ProviderVerifier reads the authoritative transaction state directly from
// the provider. It is used by reconciliation jobs when a webhook is delayed
// or when an accepted callback still needs independent verification.
type ProviderVerifier interface {
	Verify(context.Context, Payment) (ProviderVerification, error)
}

type ProviderRefundRequest struct {
	PaymentID                    string
	ProviderReference            string
	ProviderTransactionReference string
	Amount                       Money
	Reason                       string
}

type ProviderRefundSubmission struct {
	ProviderRefundReference string
	Status                  Status
}

// ProviderRefunder submits a refund with the merchant credential held by the
// payment service. The resulting reference is safe to expose, but credentials
// and raw provider responses never cross the service boundary.
type ProviderRefunder interface {
	Refund(context.Context, ProviderRefundRequest) (ProviderRefundSubmission, error)
}

type Payment struct {
	ID                           string         `json:"id"`
	OrderReference               string         `json:"order_reference"`
	Method                       Method         `json:"method"`
	Status                       Status         `json:"status"`
	Amount                       Money          `json:"amount"`
	RefundAmount                 *Money         `json:"refund_amount,omitempty"`
	ProviderReference            string         `json:"provider_reference,omitempty"`
	ProviderTransactionReference string         `json:"provider_transaction_reference,omitempty"`
	ProviderRefundReference      string         `json:"provider_refund_reference,omitempty"`
	ClientHandoff                *ClientHandoff `json:"client_handoff,omitempty"`
	AllowedActions               []string       `json:"allowed_actions"`
	CreatedAt                    time.Time      `json:"created_at"`
	UpdatedAt                    time.Time      `json:"updated_at"`
	scope                        Scope
	payer                        Payer
}

type ProviderEvent struct {
	EventID                      string `json:"event_id"`
	PaymentID                    string `json:"payment_id"`
	ProviderReference            string `json:"provider_reference"`
	ProviderTransactionReference string `json:"provider_transaction_reference,omitempty"`
	Status                       Status `json:"status"`
	AmountMinor                  int64  `json:"amount_minor"`
	Currency                     string `json:"currency"`
}
