package supply

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid supply request")
	ErrNotFound            = errors.New("supply resource not found")
	ErrForbidden           = errors.New("supply action forbidden")
	ErrConflict            = errors.New("supply resource conflict")
	ErrInvalidTransition   = errors.New("invalid supply transition")
	ErrIdempotencyConflict = errors.New("supply idempotency conflict")
)

type Actor struct {
	TenantID string
	Country  string
	Subject  string
	Roles    []string
}

type ApplicationStatus string

const (
	StatusRegistered          ApplicationStatus = "REGISTERED"
	StatusDocumentsSubmitted  ApplicationStatus = "DOCUMENTS_SUBMITTED"
	StatusOCRReview           ApplicationStatus = "OCR_REVIEW"
	StatusKYCReview           ApplicationStatus = "KYC_REVIEW"
	StatusFieldVisitRequired  ApplicationStatus = "FIELD_VISIT_REQUIRED"
	StatusFieldVisitScheduled ApplicationStatus = "FIELD_VISIT_SCHEDULED"
	StatusFieldVisitPassed    ApplicationStatus = "FIELD_VISIT_PASSED"
	StatusBankReview          ApplicationStatus = "BANK_REVIEW"
	StatusApproved            ApplicationStatus = "APPROVED"
	StatusRejected            ApplicationStatus = "REJECTED"
)

type TimelineEvent struct {
	Status    ApplicationStatus `json:"status"`
	Actor     string            `json:"actor"`
	Reason    string            `json:"reason,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type Document struct {
	Kind            string            `json:"kind"`
	AssetID         string            `json:"asset_id"`
	OCRStatus       string            `json:"ocr_status"`
	ExtractedFields map[string]string `json:"extracted_fields,omitempty"`
	ReviewReason    string            `json:"review_reason,omitempty"`
}

type FieldVisit struct {
	ID              string     `json:"id"`
	OfficerID       string     `json:"officer_id,omitempty"`
	ScheduledAt     time.Time  `json:"scheduled_at"`
	Latitude        float64    `json:"latitude"`
	Longitude       float64    `json:"longitude"`
	AllowedRadiusM  float64    `json:"allowed_radius_m"`
	CheckedInAt     *time.Time `json:"checked_in_at,omitempty"`
	CheckInDistance float64    `json:"check_in_distance_m,omitempty"`
}

type ServiceZone struct {
	ID            string   `json:"id"`
	PostalCodes   []string `json:"postal_codes"`
	Latitude      float64  `json:"latitude"`
	Longitude     float64  `json:"longitude"`
	RadiusKM      float64  `json:"radius_km"`
	PolicyVersion string   `json:"policy_version"`
}

type BankAccount struct {
	Reference  string `json:"reference"`
	HolderName string `json:"holder_name"`
	Last4      string `json:"last4"`
	IFSC       string `json:"ifsc"`
	Status     string `json:"status"`
}

type Application struct {
	ID             string            `json:"id"`
	VendorID       string            `json:"vendor_id"`
	Revision       int64             `json:"revision"`
	Status         ApplicationStatus `json:"status"`
	BusinessName   string            `json:"business_name"`
	BusinessType   string            `json:"business_type"`
	ContactName    string            `json:"contact_name"`
	Documents      []Document        `json:"documents"`
	Visit          *FieldVisit       `json:"field_visit,omitempty"`
	Zones          []ServiceZone     `json:"service_zones"`
	Bank           *BankAccount      `json:"bank_account,omitempty"`
	Verified       bool              `json:"verified"`
	AllowedActions []string          `json:"allowed_actions"`
	Timeline       []TimelineEvent   `json:"timeline"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	tenantID       string
	country        string
}

type RegisterRequest struct {
	BusinessName string `json:"business_name"`
	BusinessType string `json:"business_type"`
	ContactName  string `json:"contact_name"`
}

type DocumentsRequest struct {
	Documents []Document `json:"documents"`
}

type VisitRequest struct {
	ScheduledAt    time.Time `json:"scheduled_at"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	AllowedRadiusM float64   `json:"allowed_radius_m"`
}

type CheckInRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type TransitionRequest struct {
	Status ApplicationStatus `json:"status"`
	Reason string            `json:"reason"`
}

type CatalogKind string

const (
	CatalogProduct CatalogKind = "PRODUCT"
	CatalogService CatalogKind = "SERVICE"
	CatalogFood    CatalogKind = "FOOD"
)

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type ScheduleWindow struct {
	Weekday      int    `json:"weekday"`
	StartsMinute int    `json:"starts_minute"`
	EndsMinute   int    `json:"ends_minute"`
	TimeZone     string `json:"time_zone"`
	Capacity     int    `json:"capacity"`
	BufferMinute int    `json:"buffer_minutes"`
}

type CatalogItem struct {
	ID             string           `json:"id"`
	VendorID       string           `json:"vendor_id"`
	Revision       int64            `json:"revision"`
	Kind           CatalogKind      `json:"kind"`
	Name           string           `json:"name"`
	Description    string           `json:"description"`
	SKU            string           `json:"sku"`
	Price          Money            `json:"price"`
	Stock          int              `json:"stock"`
	ApprovalStatus string           `json:"approval_status"`
	Active         bool             `json:"active"`
	Schedules      []ScheduleWindow `json:"schedules"`
	AllowedActions []string         `json:"allowed_actions"`
	UpdatedAt      time.Time        `json:"updated_at"`
	tenantID       string
	country        string
}

type CatalogRequest struct {
	Kind        CatalogKind `json:"kind"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	SKU         string      `json:"sku"`
	Price       Money       `json:"price"`
}

type WorkItem struct {
	ID             string    `json:"id"`
	VendorID       string    `json:"vendor_id"`
	ReferenceType  string    `json:"reference_type"`
	ReferenceID    string    `json:"reference_id"`
	Status         string    `json:"status"`
	Total          Money     `json:"total"`
	CustomerLabel  string    `json:"customer_label"`
	AllowedActions []string  `json:"allowed_actions"`
	UpdatedAt      time.Time `json:"updated_at"`
	tenantID       string
	country        string
}

type Promotion struct {
	ID          string    `json:"id"`
	VendorID    string    `json:"vendor_id"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind"`
	Budget      Money     `json:"budget"`
	Status      string    `json:"status"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	Impressions int64     `json:"impressions"`
	Conversions int64     `json:"conversions"`
	tenantID    string
	country     string
}

type Dashboard struct {
	Application       Application `json:"application"`
	CatalogItems      int         `json:"catalog_items"`
	LowStockItems     int         `json:"low_stock_items"`
	OpenWorkItems     int         `json:"open_work_items"`
	Sales             Money       `json:"sales"`
	Points            int64       `json:"points"`
	Recommendations   []string    `json:"recommendations"`
	OfflineSnapshotAt time.Time   `json:"offline_snapshot_at"`
}
