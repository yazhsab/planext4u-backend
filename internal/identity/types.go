package identity

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidConfiguration = errors.New("invalid identity configuration")
	ErrInvalidInput         = errors.New("invalid identity input")
	ErrRequestTooLarge      = errors.New("identity request too large")
	ErrMethodNotAllowed     = errors.New("identity method not allowed")
	ErrProviderRejected     = errors.New("provider token rejected")
	ErrProviderUnavailable  = errors.New("provider unavailable")
	ErrRefreshInvalid       = errors.New("refresh token invalid")
	ErrRefreshExpired       = errors.New("refresh token expired")
	ErrRefreshReuse         = errors.New("refresh token reuse detected")
	ErrSessionInvalid       = errors.New("session invalid")
	ErrForbidden            = errors.New("access forbidden")
	ErrNotFound             = errors.New("resource not found")
	ErrVersionConflict      = errors.New("resource version conflict")
)

type Role string

const (
	RoleCustomer Role = "CUSTOMER"
	RoleGuest    Role = "GUEST"
	RoleVendor   Role = "VENDOR"
	RoleRider    Role = "RIDER"
	RoleAdmin    Role = "ADMIN"
)

type ConsentPurpose string

const (
	ConsentAnalytics              ConsentPurpose = "ANALYTICS"
	ConsentMarketing              ConsentPurpose = "MARKETING"
	ConsentLocationServiceability ConsentPurpose = "LOCATION_SERVICEABILITY"
	ConsentLocationDelivery       ConsentPurpose = "LOCATION_DELIVERY"
)

type ProviderIdentity struct {
	Provider string
	Subject  string
}

type ProviderVerifier interface {
	Verify(context.Context, string, string) (ProviderIdentity, error)
}

type Profile struct {
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`
	Phone       string    `json:"phone,omitempty"`
	Locale      string    `json:"locale"`
	TimeZone    string    `json:"time_zone"`
	Version     int64     `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Consent struct {
	EvidenceID    string         `json:"evidence_id"`
	Purpose       ConsentPurpose `json:"purpose"`
	Granted       bool           `json:"granted"`
	PolicyVersion string         `json:"policy_version"`
	RecordedAt    time.Time      `json:"recorded_at"`
	Version       int64          `json:"version"`
}

type Identity struct {
	ID              string
	TenantID        string
	Provider        string
	ProviderSubject string
	Roles           []Role
	Profile         Profile
	CreatedAt       time.Time
	DisabledAt      *time.Time
}

type Session struct {
	ID              string
	IdentityID      string
	TenantID        string
	Country         string
	DeviceID        string
	DeviceReference string
	RefreshDigest   string
	RefreshExpires  time.Time
	AuthenticatedAt time.Time
	LastSeenAt      time.Time
	RevokedAt       *time.Time
	Rotation        int64
}

type SessionView struct {
	ID              string     `json:"id"`
	DeviceReference string     `json:"device_reference"`
	Country         string     `json:"country"`
	AuthenticatedAt time.Time  `json:"authenticated_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	Current         bool       `json:"current"`
}

type Principal struct {
	Subject  string
	Session  string
	TenantID string
	Country  string
	DeviceID string
	Roles    []Role
	AuthTime time.Time
}

type TrustedIdentity struct {
	Subject  string
	Session  string
	TenantID string
	Country  string
}

type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	TokenType        string    `json:"token_type"`
}

type Authentication struct {
	IdentityID string      `json:"identity_id"`
	TenantID   string      `json:"tenant_id"`
	Country    string      `json:"country"`
	Tokens     TokenPair   `json:"tokens"`
	Profile    Profile     `json:"profile"`
	Roles      []Role      `json:"roles"`
	Session    SessionView `json:"session"`
}

// GuestSession is deliberately access-token only. A guest cannot be refreshed
// or resolved through the customer identity/session repository.
type GuestSession struct {
	SessionID       string    `json:"session_id"`
	TenantID        string    `json:"tenant_id"`
	Country         string    `json:"country"`
	AccessToken     string    `json:"access_token"`
	AccessExpiresAt time.Time `json:"access_expires_at"`
	TokenType       string    `json:"token_type"`
}

type ExchangeInput struct {
	Provider      string
	ProviderToken string
	DeviceID      string
	Country       string
}

type ProfileUpdate struct {
	DisplayName string
	Email       *string
	Phone       *string
	Locale      string
	TimeZone    string
	Version     int64
}

type ConsentUpdate struct {
	Purpose       ConsentPurpose
	Granted       bool
	PolicyVersion string
}

type DataExport struct {
	GeneratedAt time.Time     `json:"generated_at"`
	IdentityID  string        `json:"identity_id"`
	TenantID    string        `json:"tenant_id"`
	Country     string        `json:"country"`
	Roles       []Role        `json:"roles"`
	Profile     Profile       `json:"profile"`
	Sessions    []SessionView `json:"sessions"`
	Consents    []Consent     `json:"consents"`
}

type DeletionRequest struct {
	ID          string    `json:"id"`
	IdentityID  string    `json:"identity_id"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	EffectiveAt time.Time `json:"effective_at"`
}

type StartSessionParams struct {
	Provider        ProviderIdentity
	IdentityID      string
	SessionID       string
	TenantID        string
	Country         string
	DeviceID        string
	DeviceReference string
	RefreshDigest   string
	RefreshExpires  time.Time
	Now             time.Time
}

type RotateSessionParams struct {
	CurrentDigest string
	NextDigest    string
	Now           time.Time
}

type Repository interface {
	StartSession(context.Context, StartSessionParams) (Identity, Session, error)
	RotateSession(context.Context, RotateSessionParams) (Identity, Session, error)
	RevokeByRefresh(context.Context, string, time.Time) error
	Authenticate(context.Context, TrustedIdentity, time.Time) (Identity, Session, error)
	ListSessions(context.Context, string) ([]Session, error)
	RevokeSession(context.Context, string, string, time.Time) error
	UpdateProfile(context.Context, string, ProfileUpdate, time.Time) (Profile, error)
	ListConsents(context.Context, string) ([]Consent, error)
	RecordConsent(context.Context, string, Consent, time.Time) (Consent, error)
	CreateDeletionRequest(context.Context, DeletionRequest) (DeletionRequest, error)
}

type AccessTokenIssuer interface {
	Issue(Principal) (string, time.Time, error)
}

type RefreshTokenFactory interface {
	Generate() (string, error)
}

type RefreshTokenHasher interface {
	Digest(string) (string, error)
	DeviceReference(string) (string, error)
}

type AuditEvent struct {
	ID            string
	Type          string
	IdentityID    string
	SessionID     string
	DeviceRef     string
	Purpose       ConsentPurpose
	Outcome       string
	OccurredAt    time.Time
	SecurityEvent bool
}
