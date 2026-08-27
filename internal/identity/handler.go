package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	identityCorrelationHeader = "X-Correlation-ID"
	maxIdentityRequestBytes   = 64 * 1024
)

type Handler struct {
	service   *Service
	readiness func() bool
	now       func() time.Time
	mux       *http.ServeMux
}

func NewHandler(service *Service, readiness func() bool) (*Handler, error) {
	if service == nil {
		return nil, ErrInvalidConfiguration
	}
	if readiness == nil {
		readiness = func() bool { return true }
	}
	handler := &Handler{service: service, readiness: readiness, now: service.now, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /healthz", handler.handleHealth)
	handler.mux.HandleFunc("GET /readyz", handler.handleReadiness)
	handler.mux.HandleFunc("POST /v1/auth/exchange", handler.handleExchange)
	handler.mux.HandleFunc("POST /v1/auth/refresh", handler.handleRefresh)
	handler.mux.HandleFunc("POST /v1/auth/revoke", handler.handleRevoke)
	handler.mux.HandleFunc("GET /v1/me", handler.handleMe)
	handler.mux.HandleFunc("PATCH /v1/me", handler.handleProfileUpdate)
	handler.mux.HandleFunc("GET /v1/me/sessions", handler.handleSessions)
	handler.mux.HandleFunc("DELETE /v1/me/sessions/{session_id}", handler.handleSessionRevoke)
	handler.mux.HandleFunc("GET /v1/me/consents", handler.handleConsents)
	handler.mux.HandleFunc("PUT /v1/me/consents/{purpose}", handler.handleConsentUpdate)
	return handler, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	correlationID := validCorrelationID(request.Header.Get(identityCorrelationHeader))
	if correlationID == "" {
		correlationID = newCorrelationID()
	}
	request.Header.Set(identityCorrelationHeader, correlationID)
	writer.Header().Set(identityCorrelationHeader, correlationID)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Cache-Control", "no-store")
	if request.Body != nil {
		request.Body = http.MaxBytesReader(writer, request.Body, maxIdentityRequestBytes)
	}
	_, pattern := handler.mux.Handler(request)
	if pattern == "" {
		failure := ErrNotFound
		if knownIdentityPath(request.URL.Path) {
			failure = ErrMethodNotAllowed
		}
		handler.writeFailure(writer, request, failure)
		return
	}
	handler.mux.ServeHTTP(writer, request)
}

func (handler *Handler) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "service": "identity"})
}

func (handler *Handler) handleReadiness(writer http.ResponseWriter, _ *http.Request) {
	if !handler.readiness() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "service": "identity"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready", "service": "identity"})
}

func (handler *Handler) handleExchange(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Provider      string `json:"provider"`
		ProviderToken string `json:"provider_token"`
		DeviceID      string `json:"device_id"`
		Country       string `json:"country"`
	}
	if err := decodeJSON(request, &input); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	result, err := handler.service.Exchange(request.Context(), ExchangeInput{
		Provider:      input.Provider,
		ProviderToken: input.ProviderToken,
		DeviceID:      input.DeviceID,
		Country:       input.Country,
	})
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (handler *Handler) handleRefresh(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(request, &input); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	result, err := handler.service.Refresh(request.Context(), input.RefreshToken)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) handleRevoke(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(request, &input); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	if err := handler.service.Revoke(request.Context(), input.RefreshToken); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) handleMe(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	principal, profile, err := handler.service.Principal(request.Context(), trusted)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.Header().Set("ETag", profileETag(profile.Version))
	writeJSON(writer, http.StatusOK, map[string]any{
		"id":        principal.Subject,
		"tenant_id": principal.TenantID,
		"country":   principal.Country,
		"roles":     principal.Roles,
		"profile":   profile,
	})
}

func (handler *Handler) handleProfileUpdate(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	version, err := parseProfileETag(request.Header.Get("If-Match"))
	if err != nil {
		handler.writeFailure(writer, request, ErrInvalidInput)
		return
	}
	var input struct {
		DisplayName string `json:"display_name"`
		Locale      string `json:"locale"`
		TimeZone    string `json:"time_zone"`
	}
	if err := decodeJSON(request, &input); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	profile, err := handler.service.UpdateProfile(request.Context(), trusted, ProfileUpdate{
		DisplayName: input.DisplayName,
		Locale:      input.Locale,
		TimeZone:    input.TimeZone,
		Version:     version,
	})
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.Header().Set("ETag", profileETag(profile.Version))
	writeJSON(writer, http.StatusOK, profile)
}

func (handler *Handler) handleSessions(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	sessions, err := handler.service.Sessions(request.Context(), trusted)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sessions": sessions})
}

func (handler *Handler) handleSessionRevoke(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	if err := handler.service.RevokeSession(request.Context(), trusted, request.PathValue("session_id")); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) handleConsents(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	consents, err := handler.service.Consents(request.Context(), trusted)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"consents": consents})
}

func (handler *Handler) handleConsentUpdate(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	var input struct {
		Granted       *bool  `json:"granted"`
		PolicyVersion string `json:"policy_version"`
	}
	if err := decodeJSON(request, &input); err != nil || input.Granted == nil {
		if err == nil {
			err = ErrInvalidInput
		}
		handler.writeFailure(writer, request, err)
		return
	}
	consent, err := handler.service.RecordConsent(request.Context(), trusted, ConsentUpdate{
		Purpose:       ConsentPurpose(request.PathValue("purpose")),
		Granted:       *input.Granted,
		PolicyVersion: input.PolicyVersion,
	})
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, consent)
}

func (handler *Handler) writeFailure(writer http.ResponseWriter, request *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "INTERNAL_ERROR"
	message := "The request could not be completed."
	retryable := false
	switch {
	case errors.Is(err, ErrRequestTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "The request exceeds the allowed size."
	case errors.Is(err, ErrInvalidInput):
		status, code, message = http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Check the submitted values."
	case errors.Is(err, ErrProviderRejected):
		status, code, message = http.StatusUnauthorized, "PROVIDER_TOKEN_INVALID", "Sign in again to continue."
	case errors.Is(err, ErrProviderUnavailable):
		status, code, message, retryable = http.StatusServiceUnavailable, "IDENTITY_PROVIDER_UNAVAILABLE", "Sign-in is temporarily unavailable.", true
	case errors.Is(err, ErrRefreshReuse), errors.Is(err, ErrRefreshInvalid), errors.Is(err, ErrRefreshExpired), errors.Is(err, ErrSessionInvalid):
		status, code, message = http.StatusUnauthorized, "SESSION_INVALID", "The session is no longer valid. Sign in again."
	case errors.Is(err, ErrForbidden):
		status, code, message = http.StatusForbidden, "ACCESS_DENIED", "This action is not allowed."
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "RESOURCE_NOT_FOUND", "The requested resource was not found."
	case errors.Is(err, ErrMethodNotAllowed):
		status, code, message = http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "This method is not allowed for the resource."
	case errors.Is(err, ErrVersionConflict):
		status, code, message = http.StatusConflict, "PROFILE_VERSION_CONFLICT", "The profile changed. Refresh and try again."
	}
	writeJSON(writer, status, map[string]any{
		"error": map[string]any{
			"code":           code,
			"message":        message,
			"correlation_id": request.Header.Get(identityCorrelationHeader),
			"retryable":      retryable,
			"field_errors":   []any{},
			"details":        map[string]any{},
		},
	})
}

func trustedIdentity(request *http.Request) (TrustedIdentity, error) {
	trusted := TrustedIdentity{
		Subject:  request.Header.Get("X-Planext4u-Subject"),
		Session:  request.Header.Get("X-Planext4u-Session"),
		TenantID: request.Header.Get("X-Planext4u-Tenant"),
		Country:  request.Header.Get("X-Planext4u-Country"),
	}
	if !validSafeIdentifier(trusted.Subject, 128) ||
		!validSafeIdentifier(trusted.Session, 128) ||
		!validSafeIdentifier(trusted.TenantID, 128) ||
		!validCountry(trusted.Country) {
		return TrustedIdentity{}, ErrSessionInvalid
	}
	return trusted, nil
}

func decodeJSON(request *http.Request, target any) error {
	if mediaType := request.Header.Get("Content-Type"); mediaType != "" &&
		!strings.HasPrefix(strings.ToLower(mediaType), "application/json") {
		return ErrInvalidInput
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrRequestTooLarge
		}
		return ErrInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidInput
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

func profileETag(version int64) string {
	return fmt.Sprintf("\"%d\"", version)
}

func parseProfileETag(value string) (int64, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, ErrInvalidInput
	}
	version, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || version < 1 {
		return 0, ErrInvalidInput
	}
	return version, nil
}

func validCorrelationID(value string) string {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return ""
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}

func newCorrelationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "correlation-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(value[:])
}

func knownIdentityPath(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/v1/auth/exchange", "/v1/auth/refresh", "/v1/auth/revoke",
		"/v1/me", "/v1/me/sessions", "/v1/me/consents":
		return true
	}
	for _, prefix := range []string{"/v1/me/sessions/", "/v1/me/consents/"} {
		if value := strings.TrimPrefix(path, prefix); value != path && value != "" && !strings.Contains(value, "/") {
			return true
		}
	}
	return false
}
