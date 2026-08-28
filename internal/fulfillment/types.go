package fulfillment

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid fulfillment request")
	ErrNotFound            = errors.New("fulfillment resource not found")
	ErrForbidden           = errors.New("fulfillment action forbidden")
	ErrConflict            = errors.New("fulfillment resource conflict")
	ErrInvalidTransition   = errors.New("invalid fulfillment transition")
	ErrIdempotencyConflict = errors.New("fulfillment idempotency conflict")
	ErrOfferExpired        = errors.New("fulfillment offer expired")
	ErrLocationStale       = errors.New("fulfillment location stale")
	ErrChatExpired         = errors.New("fulfillment chat expired")
	ErrMFARequired         = errors.New("fulfillment mfa required")
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

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type RiderStatus string

const (
	RiderRegistered RiderStatus = "REGISTERED"
	RiderKYCReview  RiderStatus = "KYC_REVIEW"
	RiderApproved   RiderStatus = "APPROVED"
	RiderSuspended  RiderStatus = "SUSPENDED"
)

type RiderDocument struct {
	Kind      string `json:"kind"`
	AssetID   string `json:"asset_id"`
	OCRStatus string `json:"ocr_status"`
}

type RiderProfile struct {
	ID             string          `json:"id"`
	RiderID        string          `json:"rider_id"`
	Revision       int64           `json:"revision"`
	Status         RiderStatus     `json:"status"`
	FullName       string          `json:"full_name"`
	PhoneMasked    string          `json:"phone_masked"`
	VehicleType    string          `json:"vehicle_type"`
	VehicleNumber  string          `json:"vehicle_number"`
	Documents      []RiderDocument `json:"documents"`
	BankReference  string          `json:"bank_reference,omitempty"`
	BankStatus     string          `json:"bank_status,omitempty"`
	Zones          []string        `json:"zones"`
	MaxConcurrent  int             `json:"max_concurrent"`
	AllowedActions []string        `json:"allowed_actions"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	tenantID       string
	country        string
}

type RiderRegistrationRequest struct {
	FullName      string          `json:"full_name"`
	PhoneMasked   string          `json:"phone_masked"`
	VehicleType   string          `json:"vehicle_type"`
	VehicleNumber string          `json:"vehicle_number"`
	Documents     []RiderDocument `json:"documents"`
	BankReference string          `json:"bank_reference"`
	Zones         []string        `json:"zones"`
}

type DutySession struct {
	ID          string     `json:"id"`
	RiderID     string     `json:"rider_id"`
	Revision    int64      `json:"revision"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	ZoneID      string     `json:"zone_id"`
	ActiveTasks int        `json:"active_tasks"`
	tenantID    string
	country     string
}

type Stop struct {
	Label        string `json:"label"`
	AddressToken string `json:"address_token"`
	Point        Point  `json:"point"`
}

type DeliveryTask struct {
	ID                string     `json:"id"`
	Revision          int64      `json:"revision"`
	OrderID           string     `json:"order_id"`
	OrderType         string     `json:"order_type"`
	RegionID          string     `json:"region_id"`
	TerritoryID       string     `json:"territory_id"`
	ZoneID            string     `json:"zone_id"`
	Status            string     `json:"status"`
	AssignedRiderID   string     `json:"assigned_rider_id,omitempty"`
	Pickup            Stop       `json:"pickup"`
	Dropoff           Stop       `json:"dropoff"`
	DistanceMeters    int        `json:"distance_meters"`
	Earning           Money      `json:"earning"`
	OfferExpiresAt    *time.Time `json:"offer_expires_at,omitempty"`
	AcceptedAt        *time.Time `json:"accepted_at,omitempty"`
	PickedUpAt        *time.Time `json:"picked_up_at,omitempty"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
	PODBlurredAssetID string     `json:"pod_blurred_asset_id,omitempty"`
	PODSignatureID    string     `json:"pod_signature_asset_id,omitempty"`
	ReassignmentCount int        `json:"reassignment_count"`
	AllowedActions    []string   `json:"allowed_actions"`
	UpdatedAt         time.Time  `json:"updated_at"`
	tenantID          string
	country           string
	deliveryOTPHash   string
}

type TaskSeed struct {
	ID             string `json:"id"`
	OrderID        string `json:"order_id"`
	OrderType      string `json:"order_type"`
	RegionID       string `json:"region_id"`
	TerritoryID    string `json:"territory_id"`
	ZoneID         string `json:"zone_id"`
	Pickup         Stop   `json:"pickup"`
	Dropoff        Stop   `json:"dropoff"`
	DistanceMeters int    `json:"distance_meters"`
	Earning        Money  `json:"earning"`
	DeliveryOTP    string `json:"delivery_otp"`
	CustomerID     string `json:"customer_id"`
	CounterpartyID string `json:"counterparty_id"`
}

type LocationUpdate struct {
	Sequence   int64     `json:"sequence"`
	Point      Point     `json:"point"`
	AccuracyM  float64   `json:"accuracy_m"`
	CapturedAt time.Time `json:"captured_at"`
}

type RiderLocation struct {
	RiderID   string    `json:"rider_id"`
	Sequence  int64     `json:"sequence"`
	Point     Point     `json:"point"`
	AccuracyM float64   `json:"accuracy_m"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
	tenantID  string
	country   string
}

type OfflineCommand struct {
	DeviceSequence int64          `json:"device_sequence"`
	CommandID      string         `json:"command_id"`
	Kind           string         `json:"kind"`
	TaskID         string         `json:"task_id"`
	Revision       int64          `json:"revision"`
	Payload        map[string]any `json:"payload,omitempty"`
}

type OfflineResult struct {
	DeviceSequence int64  `json:"device_sequence"`
	CommandID      string `json:"command_id"`
	Status         string `json:"status"`
	ResourceStatus string `json:"resource_status,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
}

type CompletionRequest struct {
	OTP                 string `json:"otp,omitempty"`
	BlurredPhotoAssetID string `json:"blurred_photo_asset_id"`
	SignatureAssetID    string `json:"signature_asset_id,omitempty"`
}

type Conversation struct {
	ID             string        `json:"id"`
	OrderID        string        `json:"order_id"`
	ParticipantIDs []string      `json:"participant_ids"`
	ExpiresAt      time.Time     `json:"expires_at"`
	Blocked        bool          `json:"blocked"`
	Messages       []ChatMessage `json:"messages"`
	tenantID       string
	country        string
}

type ChatMessage struct {
	ID        string           `json:"id"`
	SenderID  string           `json:"sender_id"`
	Body      string           `json:"body"`
	Redacted  bool             `json:"redacted"`
	CreatedAt time.Time        `json:"created_at"`
	Receipts  []MessageReceipt `json:"receipts"`
}

type MessageReceipt struct {
	ActorID string    `json:"actor_id"`
	State   string    `json:"state"`
	At      time.Time `json:"at"`
}

type LedgerEntry struct {
	ID                 string    `json:"id"`
	AccountID          string    `json:"account_id"`
	ReferenceID        string    `json:"reference_id"`
	Kind               string    `json:"kind"`
	Gross              Money     `json:"gross"`
	Commission         Money     `json:"commission"`
	Tax                Money     `json:"tax"`
	Net                Money     `json:"net"`
	CalculationVersion string    `json:"calculation_version"`
	AvailableAt        time.Time `json:"available_at"`
	CreatedAt          time.Time `json:"created_at"`
	tenantID           string
	country            string
}

type Payout struct {
	ID                string    `json:"id"`
	AccountID         string    `json:"account_id"`
	Revision          int64     `json:"revision"`
	Amount            Money     `json:"amount"`
	Status            string    `json:"status"`
	EntryIDs          []string  `json:"entry_ids"`
	FirstApproverID   string    `json:"first_approver_id,omitempty"`
	SecondApproverID  string    `json:"second_approver_id,omitempty"`
	ProviderReference string    `json:"provider_reference,omitempty"`
	AttemptCount      int       `json:"attempt_count"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	tenantID          string
	country           string
}

type AttendanceEntry struct {
	ID         string    `json:"id"`
	SubjectID  string    `json:"subject_id"`
	SessionID  string    `json:"session_id"`
	Kind       string    `json:"kind"`
	Point      Point     `json:"point"`
	RecordedAt time.Time `json:"recorded_at"`
	Source     string    `json:"source"`
	tenantID   string
	country    string
}

type Territory struct {
	ID          string   `json:"id"`
	RegionID    string   `json:"region_id"`
	FranchiseID string   `json:"franchise_id"`
	Name        string   `json:"name"`
	Center      Point    `json:"center"`
	RadiusKM    float64  `json:"radius_km"`
	PostalCodes []string `json:"postal_codes"`
	tenantID    string
	country     string
}

type FieldCheckIn struct {
	ID          string    `json:"id"`
	OfficerID   string    `json:"officer_id"`
	TerritoryID string    `json:"territory_id"`
	Point       Point     `json:"point"`
	DistanceM   float64   `json:"distance_m"`
	RecordedAt  time.Time `json:"recorded_at"`
	tenantID    string
	country     string
}

type AuditEvent struct {
	ID           string    `json:"id"`
	ActorID      string    `json:"actor_id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	tenantID     string
	country      string
}

type RegionalDashboard struct {
	RegionID            string          `json:"region_id"`
	Territories         int             `json:"territories"`
	OnlineRiders        int             `json:"online_riders"`
	UnassignedTasks     int             `json:"unassigned_tasks"`
	AtRiskTasks         int             `json:"at_risk_tasks"`
	CompletedToday      int             `json:"completed_today"`
	PayoutsPending      Money           `json:"payouts_pending"`
	LatestLocations     []RiderLocation `json:"latest_locations"`
	RecentFieldCheckIns []FieldCheckIn  `json:"recent_field_check_ins"`
	GeneratedAt         time.Time       `json:"generated_at"`
}

type Reconciliation struct {
	AccountID          string `json:"account_id"`
	LedgerNetMinor     int64  `json:"ledger_net_minor"`
	PaidMinor          int64  `json:"paid_minor"`
	PendingMinor       int64  `json:"pending_minor"`
	VarianceMinor      int64  `json:"variance_minor"`
	Currency           string `json:"currency"`
	CalculationVersion string `json:"calculation_version"`
}

type Configuration struct {
	TenantID              string
	Country               string
	OfferTTL              time.Duration
	LocationTTL           time.Duration
	ChatAfterDeliveryTTL  time.Duration
	SettlementCooling     time.Duration
	CommissionBasisPoints int64
	TaxBasisPoints        int64
	Territories           []Territory
}
