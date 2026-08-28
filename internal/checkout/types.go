package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/commerce"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

var (
	ErrInvalidRequest      = errors.New("invalid checkout request")
	ErrAddressNotFound     = errors.New("checkout address not found")
	ErrAddressConflict     = errors.New("checkout address revision conflict")
	ErrSlotNotAvailable    = errors.New("delivery slot unavailable")
	ErrPromotionInvalid    = errors.New("promotion is invalid")
	ErrQuoteNotFound       = errors.New("checkout quote not found")
	ErrQuoteExpired        = errors.New("checkout quote expired")
	ErrQuoteStale          = errors.New("checkout quote is stale")
	ErrPaymentMethod       = errors.New("payment method is not allowed")
	ErrIdempotencyConflict = errors.New("checkout idempotency conflict")
)

type Scope struct {
	TenantID   string
	Country    string
	CustomerID string
}

type Address struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Line1       string  `json:"line1"`
	Line2       string  `json:"line2,omitempty"`
	PostalCode  string  `json:"postal_code"`
	Locality    string  `json:"locality"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	Serviceable bool    `json:"serviceable"`
	Default     bool    `json:"default"`
	Revision    int64   `json:"revision"`
	TenantID    string  `json:"-"`
	Country     string  `json:"-"`
	CustomerID  string  `json:"-"`
}

type AddressInput struct {
	Label      string  `json:"label"`
	Line1      string  `json:"line1"`
	Line2      string  `json:"line2,omitempty"`
	PostalCode string  `json:"postal_code"`
	Locality   string  `json:"locality"`
	Latitude   float64 `json:"latitude,omitempty"`
	Longitude  float64 `json:"longitude,omitempty"`
	Default    bool    `json:"default"`
}

type PostalZone struct {
	Country    string
	PostalCode string
	Locality   string
}

type DeliverySlot struct {
	ID          string    `json:"id"`
	Country     string    `json:"country"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Fee         Money     `json:"fee"`
	Capacity    int       `json:"capacity"`
}

type Promotion struct {
	Code             string
	Country          string
	MinimumSubtotal  int64
	DiscountBasisPts int64
	MaximumDiscount  int64
	StartsAt         time.Time
	EndsAt           time.Time
}

type WalletMode string

const (
	WalletHybrid     WalletMode = "HYBRID_PAYMENT"
	WalletPointsOnly WalletMode = "POINTS_ONLY"
)

type PricingPolicy struct {
	Version               string
	Country               string
	TaxBasisPoints        int64
	PlatformFeeMinor      int64
	WalletPointValueMinor int64
	WalletMode            WalletMode
	QuoteTTL              time.Duration
	ReservationTTL        time.Duration
}

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type QuoteRequest struct {
	CartRevision  int64  `json:"cart_revision"`
	AddressID     string `json:"address_id"`
	DeliverySlot  string `json:"delivery_slot_id"`
	PromotionCode string `json:"promotion_code,omitempty"`
	WalletPoints  int64  `json:"wallet_points"`
}

type Quote struct {
	ID                   string              `json:"id"`
	CartRevision         int64               `json:"cart_revision"`
	Items                []commerce.CartLine `json:"items"`
	Address              Address             `json:"address"`
	Delivery             DeliverySlot        `json:"delivery"`
	Subtotal             Money               `json:"subtotal"`
	Discount             Money               `json:"discount"`
	Tax                  Money               `json:"tax"`
	Fees                 Money               `json:"fees"`
	WalletApplied        Money               `json:"wallet_applied"`
	WalletPointsRedeemed int64               `json:"wallet_points_redeemed"`
	Total                Money               `json:"total"`
	PromotionCode        string              `json:"promotion_code,omitempty"`
	PricingPolicyVersion string              `json:"pricing_policy_version"`
	PaymentMethods       []payment.Method    `json:"payment_methods"`
	Warnings             []string            `json:"warnings"`
	AllowedActions       []string            `json:"allowed_actions"`
	ExpiresAt            time.Time           `json:"expires_at"`
	CreatedAt            time.Time           `json:"created_at"`
	scope                Scope
}

type PlaceResult struct {
	Quote       Quote                 `json:"quote"`
	Reservation inventory.Reservation `json:"reservation"`
	Payment     payment.Payment       `json:"payment"`
	Order       order.Order           `json:"order"`
	WalletDebit *wallet.LedgerEntry   `json:"wallet_debit,omitempty"`
}

type ReturnResult struct {
	Order        order.Order           `json:"order"`
	Payment      *payment.Payment      `json:"payment,omitempty"`
	WalletRefund *wallet.LedgerEntry   `json:"wallet_refund,omitempty"`
	Inventory    inventory.Reservation `json:"inventory"`
	Pending      bool                  `json:"pending"`
}

type WalletRefillResult struct {
	Offer   wallet.RefillOffer  `json:"offer"`
	Payment payment.Payment     `json:"payment"`
	Credit  *wallet.LedgerEntry `json:"credit,omitempty"`
}

type CancellationResult struct {
	Order         order.Order           `json:"order"`
	Payment       payment.Payment       `json:"payment"`
	WalletRefund  *wallet.LedgerEntry   `json:"wallet_refund,omitempty"`
	Inventory     inventory.Reservation `json:"inventory"`
	RefundPending bool                  `json:"refund_pending"`
}

type Dependencies struct {
	Cart      *commerce.Service
	Inventory *inventory.Service
	Wallet    *wallet.Service
	Payment   *payment.Service
	Orders    *order.Service
	Payers    PayerResolver
}

type PayerResolver interface {
	ResolvePayer(context.Context, Scope) (payment.Payer, error)
}

type Configuration struct {
	Addresses   []Address
	Slots       []DeliverySlot
	Promotions  []Promotion
	Policies    []PricingPolicy
	PostalZones []PostalZone
}
