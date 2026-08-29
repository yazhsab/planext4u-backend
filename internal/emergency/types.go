package emergency

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid emergency request")
	ErrNotFound            = errors.New("emergency resource not found")
	ErrForbidden           = errors.New("emergency action forbidden")
	ErrConflict            = errors.New("emergency assignment conflict")
	ErrIdempotencyConflict = errors.New("emergency idempotency conflict")
	ErrMFARequired         = errors.New("emergency action requires mfa")
)

type Actor struct {
	TenantID    string
	Country     string
	Subject     string
	Roles       []string
	MFAVerified bool
}

type Location struct {
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	AccuracyM  float64   `json:"accuracy_m"`
	CapturedAt time.Time `json:"captured_at"`
}

type Request struct {
	ID                string     `json:"id"`
	Revision          int64      `json:"revision"`
	RequesterID       string     `json:"requester_id"`
	Category          string     `json:"category"`
	Description       string     `json:"description"`
	Priority          string     `json:"priority"`
	Status            string     `json:"status"`
	AssignedResponder string     `json:"assigned_responder,omitempty"`
	LocationConsent   bool       `json:"location_consent"`
	CurrentLocation   *Location  `json:"current_location,omitempty"`
	EscalationLevel   int        `json:"escalation_level"`
	SLADeadline       time.Time  `json:"sla_deadline"`
	AcceptedAt        *time.Time `json:"accepted_at,omitempty"`
	ResolvedAt        *time.Time `json:"resolved_at,omitempty"`
	AllowedActions    []string   `json:"allowed_actions"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	tenantID          string
	country           string
	lastLocation      *Location
}

type CreateRequest struct {
	Category        string   `json:"category"`
	Description     string   `json:"description"`
	Priority        string   `json:"priority"`
	LocationConsent bool     `json:"location_consent"`
	Location        Location `json:"location"`
}

type LocationRequest struct {
	Consent  bool     `json:"consent"`
	Location Location `json:"location"`
}

type TransitionRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

type Message struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id"`
	SenderID  string    `json:"sender_id"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type MessageRequest struct {
	Body string `json:"body"`
}

type SLAReport struct {
	GeneratedAt       time.Time      `json:"generated_at"`
	Open              int            `json:"open"`
	Assigned          int            `json:"assigned"`
	Resolved          int            `json:"resolved"`
	Breached          int            `json:"breached"`
	AverageAcceptSecs int64          `json:"average_accept_seconds"`
	ByCategory        map[string]int `json:"by_category"`
	LocationPrecision string         `json:"location_precision"`
}

type Configuration struct {
	TenantID       string
	Country        string
	AssignmentSLA  time.Duration
	LocationMaxAge time.Duration
}
