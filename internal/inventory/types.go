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
	CreatedAt      time.Time        `json:"created_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}
