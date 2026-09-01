package order

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid order request")
	ErrOrderNotFound       = errors.New("order not found")
	ErrRevisionConflict    = errors.New("order revision conflict")
	ErrInvalidTransition   = errors.New("invalid order transition")
	ErrIdempotencyConflict = errors.New("order idempotency conflict")
	ErrProofRequired       = errors.New("proof of delivery required")
	ErrReturnNotFound      = errors.New("return case not found")
)

type Scope struct {
	TenantID   string
	Country    string
	CustomerID string
}

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type LineSnapshot struct {
	VariantID                   string `json:"variant_id"`
	ItemID                      string `json:"item_id"`
	ItemName                    string `json:"item_name"`
	VariantName                 string `json:"variant_name"`
	Quantity                    int    `json:"quantity"`
	UnitPrice                   Money  `json:"unit_price"`
	LineTotal                   Money  `json:"line_total"`
	VendorID                    string `json:"vendor_id"`
	TaxMinor                    int64  `json:"tax_minor"`
	DiscountMinor               int64  `json:"discount_minor"`
	VendorTier                  string `json:"vendor_tier"`
	CommissionBasisPoints       int64  `json:"commission_basis_points"`
	WalletRedemptionBasisPoints int64  `json:"wallet_redemption_basis_points"`
	CommissionMinor             int64  `json:"commission_minor"`
	CommercialRuleSource        string `json:"commercial_rule_source"`
}

type AddressSnapshot struct {
	AddressID  string `json:"address_id"`
	Label      string `json:"label"`
	PostalCode string `json:"postal_code"`
	Locality   string `json:"locality"`
}

type DeliverySnapshot struct {
	SlotID      string    `json:"slot_id"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Fee         Money     `json:"fee"`
}

type CheckoutSnapshot struct {
	CartRevision            int64            `json:"cart_revision"`
	Lines                   []LineSnapshot   `json:"lines"`
	Address                 AddressSnapshot  `json:"address"`
	Delivery                DeliverySnapshot `json:"delivery"`
	Subtotal                Money            `json:"subtotal"`
	Discount                Money            `json:"discount"`
	Tax                     Money            `json:"tax"`
	Fees                    Money            `json:"fees"`
	ProductTax              Money            `json:"product_tax"`
	ProductTaxTreatment     string           `json:"product_tax_treatment"`
	PlatformFee             Money            `json:"platform_fee"`
	PlatformFeeTax          Money            `json:"platform_fee_tax"`
	DeliveryFee             Money            `json:"delivery_fee"`
	MarketplaceCommission   Money            `json:"marketplace_commission"`
	WalletRedemptionLimit   Money            `json:"wallet_redemption_limit"`
	WalletApplied           Money            `json:"wallet_applied"`
	Total                   Money            `json:"total"`
	PromotionCode           string           `json:"promotion_code,omitempty"`
	PricingPolicyVersion    string           `json:"pricing_policy_version"`
	CommercialPolicyVersion string           `json:"commercial_policy_version"`
	ReservationID           string           `json:"reservation_id"`
	PaymentID               string           `json:"payment_id"`
	PaymentMethod           string           `json:"payment_method"`
}

type Status string

const (
	StatusPendingPayment               Status = "PENDING_PAYMENT"
	StatusPlaced                       Status = "PLACED"
	StatusAccepted                     Status = "ACCEPTED"
	StatusRejected                     Status = "REJECTED"
	StatusPacking                      Status = "PACKING"
	StatusReadyForHandover             Status = "READY_FOR_HANDOVER"
	StatusAssigned                     Status = "ASSIGNED"
	StatusPickedUp                     Status = "PICKED_UP"
	StatusOutForDelivery               Status = "OUT_FOR_DELIVERY"
	StatusDeliveredPendingConfirmation Status = "DELIVERED_PENDING_CONFIRMATION"
	StatusCompleted                    Status = "COMPLETED"
	StatusCancelRequested              Status = "CANCEL_REQUESTED"
	StatusCancelled                    Status = "CANCELLED"
	StatusReturnRequested              Status = "RETURN_REQUESTED"
	StatusReturnApproved               Status = "RETURN_APPROVED"
	StatusReturnRejected               Status = "RETURN_REJECTED"
	StatusReturned                     Status = "RETURNED"
	StatusRefunded                     Status = "REFUNDED"
)

type TimelineEvent struct {
	Status    Status    `json:"status"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Proof struct {
	PolicyVersion string    `json:"policy_version"`
	PhotoAssetID  string    `json:"photo_asset_id,omitempty"`
	RecipientName string    `json:"recipient_name,omitempty"`
	OTPVerified   bool      `json:"otp_verified"`
	SignedAt      time.Time `json:"signed_at,omitempty"`
}

type ReturnLine struct {
	VariantID string `json:"variant_id"`
	Quantity  int    `json:"quantity"`
}

type ReturnCase struct {
	ID              string       `json:"id"`
	Status          Status       `json:"status"`
	Lines           []ReturnLine `json:"lines"`
	Reason          string       `json:"reason"`
	RefundAmount    Money        `json:"refund_amount"`
	RefundReference string       `json:"refund_reference,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type Rating struct {
	Score     int       `json:"score"`
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Notification struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	Country     string     `json:"country"`
	CustomerID  string     `json:"customer_id"`
	OrderID     string     `json:"order_id"`
	Status      Status     `json:"status"`
	Revision    int64      `json:"revision"`
	Attempts    int        `json:"attempts"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Notifier interface {
	Send(context.Context, Notification) error
}

type Order struct {
	ID             string           `json:"id"`
	Revision       int64            `json:"revision"`
	Status         Status           `json:"status"`
	Snapshot       CheckoutSnapshot `json:"snapshot"`
	AllowedActions []string         `json:"allowed_actions"`
	Timeline       []TimelineEvent  `json:"timeline"`
	Proof          *Proof           `json:"proof,omitempty"`
	Return         *ReturnCase      `json:"return,omitempty"`
	Rating         *Rating          `json:"rating,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	scope          Scope
}

// OrderService is the checkout-facing order lifecycle boundary.
type OrderService interface {
	Create(Scope, string, CheckoutSnapshot, bool) (Order, bool, error)
	Get(Scope, string) (Order, error)
	List(Scope) ([]Order, error)
	Transition(Scope, string, string, int64, Status, string, string) (Order, bool, error)
	RequestReturn(Scope, string, string, int64, []ReturnLine, string) (Order, bool, error)
	DecideReturn(Scope, string, string, int64, bool, string) (Order, bool, error)
	RecordPOD(Scope, string, string, int64, Proof) (Order, bool, error)
	RecordRefund(Scope, string, string, int64, string, Money) (Order, bool, error)
	Rate(Scope, string, string, int64, int, string) (Order, bool, error)
	ProcessNotifications(context.Context, int) (int, error)
}
