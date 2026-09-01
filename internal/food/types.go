package food

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid food request")
	ErrNotFound            = errors.New("food resource not found")
	ErrForbidden           = errors.New("food action forbidden")
	ErrConflict            = errors.New("food resource conflict")
	ErrInvalidTransition   = errors.New("invalid food transition")
	ErrIdempotencyConflict = errors.New("food idempotency conflict")
	ErrRestaurantClosed    = errors.New("restaurant closed")
	ErrUnavailable         = errors.New("menu item unavailable")
)

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

type Restaurant struct {
	ID                 string    `json:"id"`
	OwnerID            string    `json:"owner_id,omitempty"`
	Name               string    `json:"name"`
	Cuisine            []string  `json:"cuisine"`
	PostalCodes        []string  `json:"postal_codes"`
	Rating             float64   `json:"rating"`
	Verified           bool      `json:"verified"`
	Open               bool      `json:"open"`
	AcceptUntilMinute  int       `json:"accept_until_minute"`
	PreparationMinutes int       `json:"preparation_minutes"`
	DeliveryFee        Money     `json:"delivery_fee"`
	MinimumOrder       Money     `json:"minimum_order"`
	UpdatedAt          time.Time `json:"updated_at"`
	tenantID           string
	country            string
}

type Option struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceDelta Money  `json:"price_delta"`
	Available  bool   `json:"available"`
}

type OptionGroup struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Minimum      int      `json:"minimum"`
	Maximum      int      `json:"maximum"`
	Options      []Option `json:"options"`
	Instructions string   `json:"instructions,omitempty"`
}

type MenuItem struct {
	ID           string        `json:"id"`
	RestaurantID string        `json:"restaurant_id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Category     string        `json:"category"`
	Vegetarian   bool          `json:"vegetarian"`
	BasePrice    Money         `json:"base_price"`
	Available    bool          `json:"available"`
	OptionGroups []OptionGroup `json:"option_groups"`
	ImageAssetID string        `json:"image_asset_id,omitempty"`
	tenantID     string
	country      string
}

type CartLineRequest struct {
	MenuItemID string   `json:"menu_item_id"`
	Quantity   int      `json:"quantity"`
	OptionIDs  []string `json:"option_ids"`
	Note       string   `json:"note,omitempty"`
}

type CartRequest struct {
	RestaurantID string            `json:"restaurant_id"`
	PostalCode   string            `json:"postal_code"`
	Lines        []CartLineRequest `json:"lines"`
}

type PricedOption struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceDelta Money  `json:"price_delta"`
}

type CartLine struct {
	MenuItemID string         `json:"menu_item_id"`
	Name       string         `json:"name"`
	Quantity   int            `json:"quantity"`
	UnitPrice  Money          `json:"unit_price"`
	Options    []PricedOption `json:"options"`
	Note       string         `json:"note,omitempty"`
	LineTotal  Money          `json:"line_total"`
}

type Cart struct {
	ID             string     `json:"id"`
	CustomerID     string     `json:"customer_id"`
	Restaurant     Restaurant `json:"restaurant"`
	PostalCode     string     `json:"postal_code"`
	Revision       int64      `json:"revision"`
	Lines          []CartLine `json:"lines"`
	Subtotal       Money      `json:"subtotal"`
	DeliveryFee    Money      `json:"delivery_fee"`
	Tax            Money      `json:"tax"`
	Total          Money      `json:"total"`
	PricingVersion string     `json:"pricing_version"`
	ExpiresAt      time.Time  `json:"expires_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	tenantID       string
	country        string
}

type OrderStatus string

const (
	StatusPendingRestaurant OrderStatus = "PENDING_RESTAURANT"
	StatusAccepted          OrderStatus = "ACCEPTED"
	StatusPreparing         OrderStatus = "PREPARING"
	StatusReady             OrderStatus = "READY"
	StatusRiderAssigned     OrderStatus = "RIDER_ASSIGNED"
	StatusPickedUp          OrderStatus = "PICKED_UP"
	StatusDelivered         OrderStatus = "DELIVERED"
	StatusRejected          OrderStatus = "REJECTED"
	StatusTimedOut          OrderStatus = "TIMED_OUT"
	StatusCancelled         OrderStatus = "CANCELLED"
)

type Payment struct {
	Method      string `json:"method"`
	Status      string `json:"status"`
	Reference   string `json:"reference"`
	RefundState string `json:"refund_state,omitempty"`
}

type OrderEvent struct {
	Status    OrderStatus `json:"status"`
	Actor     string      `json:"actor"`
	Reason    string      `json:"reason,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
}

type Order struct {
	ID               string       `json:"id"`
	CustomerID       string       `json:"customer_id"`
	RestaurantID     string       `json:"restaurant_id"`
	Revision         int64        `json:"revision"`
	Status           OrderStatus  `json:"status"`
	Lines            []CartLine   `json:"lines"`
	Subtotal         Money        `json:"subtotal"`
	DeliveryFee      Money        `json:"delivery_fee"`
	Tax              Money        `json:"tax"`
	Total            Money        `json:"total"`
	Payment          Payment      `json:"payment"`
	PostalCode       string       `json:"postal_code"`
	PricingVersion   string       `json:"pricing_version"`
	AcceptBy         time.Time    `json:"accept_by"`
	EstimatedReadyAt *time.Time   `json:"estimated_ready_at,omitempty"`
	RejectionReason  string       `json:"rejection_reason,omitempty"`
	AllowedActions   []string     `json:"allowed_actions"`
	Timeline         []OrderEvent `json:"timeline"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
	tenantID         string
	country          string
	walletDebitID    string
}

type CreateOrderRequest struct {
	CartID        string `json:"cart_id"`
	PaymentMethod string `json:"payment_method"`
}

type RestaurantTransitionRequest struct {
	Status OrderStatus `json:"status"`
	Reason string      `json:"reason,omitempty"`
}

type Configuration struct {
	TenantID       string
	Country        string
	Restaurants    []Restaurant
	Menu           []MenuItem
	CartTTL        time.Duration
	AcceptanceTTL  time.Duration
	TaxBasisPoints int64
}
