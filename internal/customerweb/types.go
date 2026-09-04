package customerweb

import (
	"context"
	"errors"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/identity"
)

const SessionCookieName = "__Host-p4u_customer"

var (
	ErrInvalidConfiguration   = errors.New("invalid customer web configuration")
	ErrInvalidRequest         = errors.New("invalid customer web request")
	ErrAuthenticationRequired = errors.New("customer web authentication required")
	ErrSessionNotFound        = errors.New("customer web session not found")
	ErrSessionExists          = errors.New("customer web session already exists")
	ErrGuestMutation          = errors.New("guest sessions cannot mutate platform resources")
	ErrRefreshInProgress      = errors.New("customer web session refresh is already in progress")
)

type SessionMode string

const (
	SessionModeGuest         SessionMode = "GUEST"
	SessionModeAuthenticated SessionMode = "AUTHENTICATED"
)

type Session struct {
	ID               string
	PlatformSession  string
	IdentityID       string
	TenantID         string
	Country          string
	DisplayName      string
	Roles            []identity.Role
	Guest            bool
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	CSRFToken        string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type SessionView struct {
	SessionID        string          `json:"session_id"`
	IdentityID       string          `json:"identity_id,omitempty"`
	TenantID         string          `json:"tenant_id"`
	Country          string          `json:"country"`
	DisplayName      string          `json:"display_name"`
	Roles            []identity.Role `json:"roles"`
	Guest            bool            `json:"guest"`
	CSRFToken        string          `json:"csrf_token"`
	AccessExpiresAt  time.Time       `json:"access_expires_at"`
	SessionExpiresAt time.Time       `json:"session_expires_at"`
}

type CreateSessionInput struct {
	Mode          SessionMode `json:"mode"`
	Country       string      `json:"country"`
	Provider      string      `json:"provider,omitempty"`
	ProviderToken string      `json:"provider_token,omitempty"`
	DeviceID      string      `json:"device_id,omitempty"`
}

type PlatformSession struct {
	PlatformSession  string
	IdentityID       string
	TenantID         string
	Country          string
	DisplayName      string
	Roles            []identity.Role
	Guest            bool
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

type IdentityClient interface {
	CreateGuest(context.Context, string) (PlatformSession, error)
	Exchange(context.Context, identity.ExchangeInput) (PlatformSession, error)
	Refresh(context.Context, string) (identity.TokenPair, error)
	Revoke(context.Context, string) error
}

type SessionStore interface {
	Create(context.Context, Session) (string, error)
	Resolve(context.Context, string) (Session, error)
	Replace(context.Context, string, Session) (string, error)
	Delete(context.Context, string) error
	AcquireRefresh(context.Context, string, time.Duration) error
	ReleaseRefresh(context.Context, string) error
}

func (session Session) View() SessionView {
	return SessionView{
		SessionID: session.ID, IdentityID: session.IdentityID, TenantID: session.TenantID,
		Country: session.Country, DisplayName: session.DisplayName, Roles: append([]identity.Role(nil), session.Roles...),
		Guest: session.Guest, CSRFToken: session.CSRFToken, AccessExpiresAt: session.AccessExpiresAt,
		SessionExpiresAt: session.ExpiresAt,
	}
}
