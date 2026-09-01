package adminshell

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxSessionExchangeBytes = 64 * 1024

type SessionIssuer interface {
	Issue(Principal, time.Duration) (string, error)
}

type SessionExchangeConfig struct {
	Sessions   SessionIssuer
	Secret     []byte
	Clock      func() time.Time
	SessionTTL time.Duration
	MaxSkew    time.Duration
	RequireMFA bool
}

type sessionExchangeHandler struct {
	sessions   SessionIssuer
	secret     []byte
	clock      func() time.Time
	sessionTTL time.Duration
	maxSkew    time.Duration
	requireMFA bool
}

func NewSessionExchangeHandler(config SessionExchangeConfig) (http.Handler, error) {
	if config.Sessions == nil || config.Clock == nil || len(config.Secret) < 32 || len(config.Secret) > 1024 ||
		config.SessionTTL < 5*time.Minute || config.SessionTTL > 24*time.Hour || config.MaxSkew < time.Second || config.MaxSkew > 5*time.Minute {
		return nil, ErrInvalidRequest
	}
	handler := &sessionExchangeHandler{
		sessions: config.Sessions, secret: append([]byte(nil), config.Secret...), clock: config.Clock,
		sessionTTL: config.SessionTTL, maxSkew: config.MaxSkew, requireMFA: config.RequireMFA,
	}
	mux := http.NewServeMux()
	mux.Handle("POST /internal/v1/admin-sessions", handler)
	return securityHeaders(mux), nil
}

func (handler *sessionExchangeHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, request, http.StatusUnsupportedMediaType, "ADMIN_SESSION_CONTENT_TYPE_INVALID", "Use application/json for administrator session exchange.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxSessionExchangeBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxSessionExchangeBytes {
		writeProblem(writer, request, http.StatusRequestEntityTooLarge, "ADMIN_SESSION_REQUEST_TOO_LARGE", "The administrator session exchange request is too large.")
		return
	}
	issuedAt, signatureOK := handler.validSignature(request, body)
	if !signatureOK {
		writeProblem(writer, request, http.StatusUnauthorized, "ADMIN_SESSION_EXCHANGE_INVALID", "The administrator identity assertion could not be verified.")
		return
	}
	var input struct {
		SubjectID        string    `json:"subject_id"`
		SessionID        string    `json:"session_id"`
		TenantID         string    `json:"tenant_id"`
		DisplayName      string    `json:"display_name"`
		Roles            []Role    `json:"roles"`
		AllowedCountries []string  `json:"allowed_countries"`
		SelectedCountry  string    `json:"selected_country"`
		AuthenticatedAt  time.Time `json:"authenticated_at"`
		AuthMethods      []string  `json:"auth_methods"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_SESSION_ASSERTION_INVALID", "The administrator identity assertion is invalid.")
		return
	}
	principal := Principal{
		SubjectID: input.SubjectID, SessionID: input.SessionID, TenantID: input.TenantID, DisplayName: input.DisplayName,
		Roles: input.Roles, AllowedCountries: input.AllowedCountries, SelectedCountry: input.SelectedCountry,
		AuthenticatedAt: input.AuthenticatedAt.UTC(), AuthMethods: input.AuthMethods,
	}
	assurance := assuranceFor(principal, handler.clock().UTC())
	if !validPrincipal(principal) || principal.AuthenticatedAt.After(issuedAt.Add(handler.maxSkew)) ||
		issuedAt.Sub(principal.AuthenticatedAt) > 24*time.Hour || handler.requireMFA && !assurance.MFASatisfied {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_SESSION_ASSERTION_FORBIDDEN", "The verified identity does not satisfy administrator access policy.")
		return
	}
	token, err := handler.sessions.Issue(principal, handler.sessionTTL)
	if err != nil && errors.Is(err, ErrSessionExists) {
		writeProblem(writer, request, http.StatusConflict, "ADMIN_SESSION_EXCHANGE_REPLAYED", "This administrator sign-in assertion was already exchanged or is no longer valid.")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "ADMIN_SESSION_ISSUE_FAILED", "The administrator session could not be created.")
		return
	}
	expiresAt := handler.clock().UTC().Add(handler.sessionTTL)
	if err := SetSessionCookie(writer, token, expiresAt); err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "ADMIN_SESSION_ISSUE_FAILED", "The administrator session could not be created.")
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"expires_at": expiresAt})
}

func (handler *sessionExchangeHandler) validSignature(request *http.Request, body []byte) (time.Time, bool) {
	rawTimestamp := strings.TrimSpace(request.Header.Get("X-P4U-Admin-Timestamp"))
	seconds, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	issuedAt := time.Unix(seconds, 0).UTC()
	delta := handler.clock().UTC().Sub(issuedAt)
	if delta < -handler.maxSkew || delta > handler.maxSkew {
		return time.Time{}, false
	}
	rawSignature := strings.TrimPrefix(strings.TrimSpace(request.Header.Get("X-P4U-Admin-Signature")), "v1=")
	provided, err := hex.DecodeString(rawSignature)
	if err != nil || len(provided) != sha256.Size {
		return time.Time{}, false
	}
	mac := hmac.New(sha256.New, handler.secret)
	_, _ = mac.Write([]byte("v1\n"))
	_, _ = mac.Write([]byte(rawTimestamp))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	return issuedAt, subtle.ConstantTimeCompare(provided, expected) == 1
}
