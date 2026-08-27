package commerce

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid commerce request")
	ErrVariantNotFound     = errors.New("variant not found")
	ErrVariantUnavailable  = errors.New("variant unavailable")
	ErrQuantityUnavailable = errors.New("quantity unavailable")
	ErrRevisionConflict    = errors.New("cart revision conflict")
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
)

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type Scope struct {
	TenantID   string
	Country    string
	CustomerID string
}

type VariantSnapshot struct {
	VariantID   string
	ItemID      string
	VendorID    string
	ItemName    string
	VariantName string
	MediaRef    string
	UnitPrice   Money
	Available   bool
	Stock       int
	MaxPerOrder int
}

type SnapshotProvider interface {
	Resolve(context.Context, Scope, string) (VariantSnapshot, error)
}

type CartLine struct {
	VariantID    string `json:"variant_id"`
	ItemID       string `json:"item_id"`
	VendorID     string `json:"vendor_id"`
	ItemName     string `json:"item_name"`
	VariantName  string `json:"variant_name"`
	MediaRef     string `json:"media_ref,omitempty"`
	Quantity     int    `json:"quantity"`
	UnitPrice    Money  `json:"unit_price"`
	LineTotal    Money  `json:"line_total"`
	Available    bool   `json:"available"`
	PriceChanged bool   `json:"price_changed"`
}

type Cart struct {
	ID             string     `json:"id"`
	Revision       int64      `json:"revision"`
	Items          []CartLine `json:"items"`
	Subtotal       Money      `json:"subtotal"`
	Discount       Money      `json:"discount"`
	Tax            Money      `json:"tax"`
	Fees           Money      `json:"fees"`
	Total          Money      `json:"total"`
	PricingStatus  string     `json:"pricing_status"`
	AllowedActions []string   `json:"allowed_actions"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
