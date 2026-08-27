package payment

import (
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
)

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type Payment struct {
	ID                string    `json:"id"`
	OrderReference    string    `json:"order_reference"`
	Method            Method    `json:"method"`
	Status            Status    `json:"status"`
	Amount            Money     `json:"amount"`
	RefundAmount      *Money    `json:"refund_amount,omitempty"`
	ProviderReference string    `json:"provider_reference,omitempty"`
	AllowedActions    []string  `json:"allowed_actions"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	scope             Scope
}

type ProviderEvent struct {
	EventID           string `json:"event_id"`
	PaymentID         string `json:"payment_id"`
	ProviderReference string `json:"provider_reference"`
	Status            Status `json:"status"`
	AmountMinor       int64  `json:"amount_minor"`
	Currency          string `json:"currency"`
}
