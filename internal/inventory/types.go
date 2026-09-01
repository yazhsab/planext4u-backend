package inventory

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid inventory request")
	ErrInsufficientStock   = errors.New("insufficient inventory")
	ErrReservationNotFound = errors.New("inventory reservation not found")
	ErrInvalidTransition   = errors.New("invalid inventory transition")
	ErrIdempotencyConflict = errors.New("inventory idempotency conflict")
)

type Scope struct {
	TenantID string
	Country  string
}

type SeedStock struct {
	Scope     Scope
	VariantID string
	Quantity  int
}

type Line struct {
	VariantID string `json:"variant_id"`
	Quantity  int    `json:"quantity"`
}

type ReservationState string

const (
	StateReserved             ReservationState = "RESERVED"
	StateCommitted            ReservationState = "COMMITTED"
	StateReleased             ReservationState = "RELEASED"
	StateRejectedInsufficient ReservationState = "REJECTED_INSUFFICIENT"
)

type Reservation struct {
	ID             string           `json:"id"`
	TenantID       string           `json:"tenant_id"`
	Country        string           `json:"country"`
	OrderReference string           `json:"order_reference"`
	State          ReservationState `json:"state"`
	Lines          []Line           `json:"lines"`
	RestockedLines []Line           `json:"restocked_lines,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// ReservationService is the checkout-facing inventory boundary. Production
// uses PostgresService while unit tests can retain the deterministic in-memory
// implementation.
type ReservationService interface {
	Get(Scope, string) (Reservation, error)
	Reserve(Scope, string, string, []Line, time.Time) (Reservation, bool, error)
	Commit(Scope, string) (Reservation, error)
	Release(Scope, string) (Reservation, error)
	Restock(Scope, string, string, []Line) (Reservation, bool, error)
}
