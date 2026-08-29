package localverticals

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid local vertical request")
	ErrNotFound            = errors.New("local vertical resource not found")
	ErrForbidden           = errors.New("local vertical action forbidden")
	ErrConflict            = errors.New("local vertical revision conflict")
	ErrIdempotencyConflict = errors.New("local vertical idempotency conflict")
	ErrMFARequired         = errors.New("local vertical administration requires mfa")
)

type Actor struct {
	TenantID    string
	Country     string
	Subject     string
	Roles       []string
	MFAVerified bool
}

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type HomeEstimate struct {
	Version      string            `json:"version"`
	Amount       Money             `json:"amount"`
	PricePerArea Money             `json:"price_per_area"`
	Factors      map[string]string `json:"factors"`
	GeneratedAt  time.Time         `json:"generated_at"`
}

type HomeListing struct {
	ID             string       `json:"id"`
	Revision       int64        `json:"revision"`
	OwnerID        string       `json:"owner_id"`
	Title          string       `json:"title"`
	PropertyType   string       `json:"property_type"`
	Purpose        string       `json:"purpose"`
	Locality       string       `json:"locality"`
	Latitude       float64      `json:"latitude"`
	Longitude      float64      `json:"longitude"`
	AreaSqFt       int          `json:"area_sq_ft"`
	Bedrooms       int          `json:"bedrooms"`
	Price          Money        `json:"price"`
	Amenities      []string     `json:"amenities"`
	MediaAssetIDs  []string     `json:"media_asset_ids"`
	Status         string       `json:"status"`
	KYCVerified    bool         `json:"kyc_verified"`
	Plan           string       `json:"plan"`
	FeaturedUntil  *time.Time   `json:"featured_until,omitempty"`
	Estimate       HomeEstimate `json:"estimate"`
	AllowedActions []string     `json:"allowed_actions"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	tenantID       string
	country        string
}

type HomeSearch struct {
	Query        string
	Locality     string
	PropertyType string
	Purpose      string
	MinPrice     int64
	MaxPrice     int64
}

type HomeListingRequest struct {
	Title         string   `json:"title"`
	PropertyType  string   `json:"property_type"`
	Purpose       string   `json:"purpose"`
	Locality      string   `json:"locality"`
	Latitude      float64  `json:"latitude"`
	Longitude     float64  `json:"longitude"`
	AreaSqFt      int      `json:"area_sq_ft"`
	Bedrooms      int      `json:"bedrooms"`
	Price         Money    `json:"price"`
	Amenities     []string `json:"amenities"`
	MediaAssetIDs []string `json:"media_asset_ids"`
}

type Inquiry struct {
	ID        string    `json:"id"`
	ListingID string    `json:"listing_id"`
	BuyerID   string    `json:"buyer_id"`
	Message   string    `json:"message"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Visit struct {
	ID          string    `json:"id"`
	ListingID   string    `json:"listing_id"`
	VisitorID   string    `json:"visitor_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type ClassifiedListing struct {
	ID              string     `json:"id"`
	Revision        int64      `json:"revision"`
	OwnerID         string     `json:"owner_id"`
	Category        string     `json:"category"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Price           Money      `json:"price"`
	Locality        string     `json:"locality"`
	MediaAssetIDs   []string   `json:"media_asset_ids"`
	Status          string     `json:"status"`
	Plan            string     `json:"plan"`
	ContactMasked   string     `json:"contact_masked"`
	ContactRevealed string     `json:"contact_revealed,omitempty"`
	WhatsAppEnabled bool       `json:"whatsapp_enabled"`
	ExpiresAt       time.Time  `json:"expires_at"`
	FeaturedUntil   *time.Time `json:"featured_until,omitempty"`
	ReportCount     int        `json:"report_count"`
	AllowedActions  []string   `json:"allowed_actions"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	tenantID        string
	country         string
	contact         string
}

type ClassifiedRequest struct {
	Category        string   `json:"category"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Price           Money    `json:"price"`
	Locality        string   `json:"locality"`
	MediaAssetIDs   []string `json:"media_asset_ids"`
	Contact         string   `json:"contact"`
	WhatsAppEnabled bool     `json:"whatsapp_enabled"`
}

type ContactRequest struct {
	Channel string `json:"channel"`
	Consent bool   `json:"consent"`
}

type ReportRequest struct {
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

type Configuration struct {
	TenantID          string
	Country           string
	Currency          string
	KYCVerifiedOwners []string
	ReviewTerms       []string
	Homes             []HomeListing
	Classifieds       []ClassifiedListing
	SeedContacts      map[string]string
}
