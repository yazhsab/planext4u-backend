package adminshell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

type OperatorVerifier interface {
	VerifyOperator(context.Context, string) (identity.ProviderAssurance, error)
}
type OperatorAccounts struct{ pool *pgxpool.Pool }

func NewOperatorAccounts(pool *pgxpool.Pool) (*OperatorAccounts, error) {
	if pool == nil {
		return nil, ErrInvalidRequest
	}
	return &OperatorAccounts{pool: pool}, nil
}
func (accounts *OperatorAccounts) Ready(ctx context.Context) error {
	var ready bool
	err := accounts.pool.QueryRow(ctx, `SELECT to_regclass('admin.operator_events') IS NOT NULL AND EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='admin' AND table_name='sessions' AND column_name='operator_revision')`).Scan(&ready)
	if err == nil && !ready {
		return ErrOperatorNotFound
	}
	return err
}
func (accounts *OperatorAccounts) lookup(ctx context.Context, tenant, key string, bySubject bool) (Principal, error) {
	var result Principal
	column := "provider_subject"
	if bySubject {
		column = "subject_id::text"
	}
	err := accounts.pool.QueryRow(ctx, `SELECT subject_id,tenant_id,display_name,roles,allowed_countries,revision FROM admin.operator_accounts WHERE tenant_id=$1 AND `+column+`=$2 AND active`, tenant, key).Scan(&result.SubjectID, &result.TenantID, &result.DisplayName, &result.Roles, &result.AllowedCountries, &result.OperatorRevision)
	return result, err
}

type OperatorSessionResolver struct {
	SessionResolver
	Accounts *OperatorAccounts
}

func (resolver OperatorSessionResolver) Resolve(request *http.Request) (ResolvedSession, error) {
	session, err := resolver.SessionResolver.Resolve(request)
	if err != nil {
		return session, err
	}
	if !contains(session.Principal.AuthMethods, "firebase_verified") {
		return session, nil
	}
	current, err := resolver.Accounts.lookup(request.Context(), session.Principal.TenantID, session.Principal.SubjectID, true)
	if err != nil || current.OperatorRevision != session.Principal.OperatorRevision || !contains(current.AllowedCountries, session.Principal.SelectedCountry) {
		return ResolvedSession{}, ErrAuthenticationRequired
	}
	// Cached cookie role snapshots cannot outlive a server-side staff change.
	session.Principal.DisplayName = current.DisplayName
	session.Principal.Roles = current.Roles
	session.Principal.AllowedCountries = current.AllowedCountries
	return session, nil
}

type OperatorLoginConfig struct {
	Verifier   OperatorVerifier
	Accounts   *OperatorAccounts
	Sessions   SessionIssuer
	TenantID   string
	Origins    []string
	Clock      func() time.Time
	SessionTTL time.Duration
}

func NewOperatorLoginHandler(config OperatorLoginConfig) (http.Handler, error) {
	if config.Accounts == nil || config.Sessions == nil || uuid.Validate(config.TenantID) != nil || config.Clock == nil || config.SessionTTL < time.Minute || config.SessionTTL > 24*time.Hour {
		return nil, ErrInvalidRequest
	}
	origins := map[string]bool{}
	for _, origin := range config.Origins {
		origins[origin] = true
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Sec-Fetch-Site") == "cross-site" || !origins[request.Header.Get("Origin")] {
			writeProblem(writer, request, 403, "ADMIN_LOGIN_ORIGIN_DENIED", "Open sign-in from the administration site.")
			return
		}
		if config.Verifier == nil {
			writeProblem(writer, request, 503, "ADMIN_IDENTITY_UNAVAILABLE", "Administrator identity is not configured.")
			return
		}
		defer request.Body.Close()
		var input struct {
			Token   string `json:"id_token"`
			Country string `json:"country"`
		}
		decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") || decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeProblem(writer, request, 422, "ADMIN_LOGIN_INVALID", "The sign-in request is invalid.")
			return
		}
		verified, err := config.Verifier.VerifyOperator(request.Context(), input.Token)
		if errors.Is(err, identity.ErrProviderUnavailable) {
			writeProblem(writer, request, 503, "ADMIN_IDENTITY_UNAVAILABLE", "Identity verification is temporarily unavailable.")
			return
		}
		if err != nil {
			writeProblem(writer, request, 401, "ADMIN_LOGIN_REJECTED", "Sign in again to continue.")
			return
		}
		now := config.Clock().UTC()
		if !verified.MFA || verified.AuthenticatedAt.After(now.Add(30*time.Second)) || now.Sub(verified.AuthenticatedAt) > 5*time.Minute {
			writeProblem(writer, request, 403, "ADMIN_MFA_REQUIRED", "Complete a fresh multi-factor sign-in.")
			return
		}
		principal, err := config.Accounts.lookup(request.Context(), config.TenantID, verified.Subject, false)
		if err != nil || !contains(principal.AllowedCountries, input.Country) {
			writeProblem(writer, request, 403, "ADMIN_ROLE_FORBIDDEN", "This identity has no active administrator access for this country.")
			return
		}
		principal.SessionID = uuid.NewString()
		principal.SelectedCountry = input.Country
		principal.AuthenticatedAt = verified.AuthenticatedAt
		principal.AuthMethods = []string{"firebase_verified", "mfa"}
		if !validPrincipal(principal) {
			writeProblem(writer, request, 403, "ADMIN_ROLE_FORBIDDEN", "Administrator access is not configured correctly.")
			return
		}
		token, err := config.Sessions.Issue(principal, config.SessionTTL)
		if err != nil {
			writeProblem(writer, request, 503, "ADMIN_SESSION_ISSUE_FAILED", "The administrator session could not be created.")
			return
		}
		if err = SetSessionCookie(writer, token, now.Add(config.SessionTTL)); err != nil {
			writeProblem(writer, request, 503, "ADMIN_SESSION_ISSUE_FAILED", "The administrator session could not be created.")
			return
		}
		writeJSON(writer, 201, map[string]any{"expires_at": now.Add(config.SessionTTL)})
	}), nil
}
