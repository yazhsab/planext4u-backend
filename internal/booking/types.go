package booking

import (
	"errors"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

var (
	ErrInvalidRequest      = errors.New("invalid booking request")
	ErrOfferingNotFound    = errors.New("service offering not found")
	ErrSlotNotFound        = errors.New("service slot not found")
	ErrSlotUnavailable     = errors.New("service slot unavailable")
	ErrHoldNotFound        = errors.New("service slot hold not found")
	ErrHoldExpired         = errors.New("service slot hold expired")
	ErrBookingNotFound     = errors.New("service booking not found")
	ErrForbidden           = errors.New("booking action forbidden")
	ErrInvalidTransition   = errors.New("invalid booking transition")
	ErrPolicyDenied        = errors.New("booking policy denied action")
	ErrOTPInvalid          = errors.New("service start otp invalid")
	ErrEvidenceRequired    = errors.New("completion evidence required")
	ErrRevisionConflict    = errors.New("booking revision conflict")
	ErrIdempotencyConflict = errors.New("booking idempotency conflict")
)

type Scope struct {
	TenantID   string
	Country    string
	CustomerID string
}

type Actor struct {
	TenantID string
	Country  string
	Subject  string
	Roles    []string
}

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type PaymentMode string

const (
	PaymentFull    PaymentMode = "FULL"
	PaymentAdvance PaymentMode = "ADVANCE"
)

type Offering struct {
	ID                    string      `json:"id"`
	ProviderID            string      `json:"provider_id"`
	ProviderName          string      `json:"provider_name"`
	CategoryID            string      `json:"category_id"`
	Name                  string      `json:"name"`
	Summary               string      `json:"summary"`
	DurationMinutes       int         `json:"duration_minutes"`
	Price                 Money       `json:"price"`
	Advance               Money       `json:"advance"`
	PaymentMode           PaymentMode `json:"payment_mode"`
	VerifiedProvider      bool        `json:"verified_provider"`
	RatingAverage         float64     `json:"rating_average"`
	CompletedBookings     int         `json:"completed_bookings"`
	LiveEngagements       int         `json:"live_engagements"`
	ServicePostalCodes    []string    `json:"service_postal_codes"`
	CancellationPolicyRef string      `json:"cancellation_policy_ref"`
	ReschedulePolicyRef   string      `json:"reschedule_policy_ref"`
	Active                bool        `json:"active"`
}

type Slot struct {
	ID              string    `json:"id"`
	OfferingID      string    `json:"offering_id"`
	ProviderID      string    `json:"provider_id"`
	StartsAt        time.Time `json:"starts_at"`
	EndsAt          time.Time `json:"ends_at"`
	TimeZone        string    `json:"time_zone"`
	Capacity        int       `json:"capacity"`
	Remaining       int       `json:"remaining"`
	BufferMinutes   int       `json:"buffer_minutes"`
	Price           Money     `json:"price"`
	Advance         Money     `json:"advance"`
	AllowedActions  []string  `json:"allowed_actions"`
	PolicyVersion   string    `json:"policy_version"`
	ServiceDate     string    `json:"service_date"`
	ProviderVersion int64     `json:"provider_version"`
}

type HoldStatus string

const (
	HoldActive   HoldStatus = "HELD"
	HoldConsumed HoldStatus = "CONSUMED"
	HoldExpired  HoldStatus = "EXPIRED"
	HoldReleased HoldStatus = "RELEASED"
)

type SlotHold struct {
	ID             string     `json:"id"`
	SlotID         string     `json:"slot_id"`
	OfferingID     string     `json:"offering_id"`
	Status         HoldStatus `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	AllowedActions []string   `json:"allowed_actions"`
	scope          Scope
}

type Status string

const (
	StatusPendingPayment               Status = "PENDING_PAYMENT"
	StatusRequested                    Status = "REQUESTED"
	StatusAccepted                     Status = "ACCEPTED"
	StatusProviderEnRoute              Status = "PROVIDER_EN_ROUTE"
	StatusArrived                      Status = "ARRIVED"
	StatusStartOTPRequired             Status = "START_OTP_REQUIRED"
	StatusInProgress                   Status = "IN_PROGRESS"
	StatusCompletionEvidenceRequired   Status = "COMPLETION_EVIDENCE_REQUIRED"
	StatusCompletedPendingConfirmation Status = "COMPLETED_PENDING_CONFIRMATION"
	StatusCompleted                    Status = "COMPLETED"
	StatusRescheduleRequested          Status = "RESCHEDULE_REQUESTED"
	StatusCancelRequested              Status = "CANCEL_REQUESTED"
	StatusCancelled                    Status = "CANCELLED"
	StatusDeclined                     Status = "DECLINED"
	StatusCustomerNoShow               Status = "CUSTOMER_NO_SHOW"
	StatusProviderNoShow               Status = "PROVIDER_NO_SHOW"
	StatusDisputed                     Status = "DISPUTED"
)

type TimelineEvent struct {
	Status    Status    `json:"status"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type CompletionEvidence struct {
	PhotoAssetID string    `json:"photo_asset_id"`
	CapturedAt   time.Time `json:"captured_at"`
	SubmittedBy  string    `json:"submitted_by"`
}

type Booking struct {
	ID                  string              `json:"id"`
	Revision            int64               `json:"revision"`
	Status              Status              `json:"status"`
	Offering            Offering            `json:"offering"`
	Slot                Slot                `json:"slot"`
	Price               Money               `json:"price"`
	AmountDue           Money               `json:"amount_due"`
	Payment             payment.Payment     `json:"payment"`
	StartOTP            string              `json:"start_otp,omitempty"`
	CompletionEvidence  *CompletionEvidence `json:"completion_evidence,omitempty"`
	RescheduleCount     int                 `json:"reschedule_count"`
	FreeReschedulesLeft int                 `json:"free_reschedules_left"`
	AllowedActions      []string            `json:"allowed_actions"`
	Timeline            []TimelineEvent     `json:"timeline"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	scope               Scope
	walletDebit         *wallet.LedgerEntry
	startOTPDigest      [32]byte
	otpExpiresAt        time.Time
	paymentExpiresAt    time.Time
}

type Policy struct {
	Version                 string
	Country                 string
	HoldTTL                 time.Duration
	CancellationCutoff      time.Duration
	MaximumFreeReschedules  int
	StartOTPValidity        time.Duration
	CompletionConfirmWindow time.Duration
	WalletPointValueMinor   int64
}

type Configuration struct {
	Offerings []Offering
	Slots     []Slot
	Policies  []Policy
}

type CreateBookingRequest struct {
	HoldID        string         `json:"hold_id"`
	PaymentMethod payment.Method `json:"payment_method"`
}

type RescheduleRequest struct {
	HoldID string `json:"hold_id"`
	Reason string `json:"reason"`
}

type ReasonRequest struct {
	Reason string `json:"reason"`
}

type StartRequest struct {
	OTP string `json:"otp"`
}

type CompletionRequest struct {
	PhotoAssetID string `json:"photo_asset_id"`
}
