package customerweb

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

const (
	defaultMaxRequestBytes = 1 << 20
	platformPrefix         = "/platform-api"
)

type Config struct {
	Sessions        SessionStore
	Identity        IdentityClient
	Platform        http.Handler
	Clock           func() time.Time
	AllowedOrigins  []string
	SessionTTL      time.Duration
	MaxRequestBytes int64
}

type Handler struct {
	sessions        SessionStore
	identity        IdentityClient
	platform        http.Handler
	clock           func() time.Time
	allowedOrigins  map[string]struct{}
	sessionTTL      time.Duration
	maxRequestBytes int64
	random          func([]byte) error
	mux             *http.ServeMux
}

func NewHandler(config Config) (*Handler, error) {
	if config.Sessions == nil || config.Identity == nil || config.Platform == nil || config.Clock == nil ||
		len(config.AllowedOrigins) == 0 || config.SessionTTL < time.Minute || config.SessionTTL > 30*24*time.Hour {
		return nil, ErrInvalidConfiguration
	}
	origins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, raw := range config.AllowedOrigins {
		parsed, err := url.Parse(raw)
		loopbackHTTP := parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !loopbackHTTP) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, ErrInvalidConfiguration
		}
		origins[raw] = struct{}{}
	}
	maxBytes := config.MaxRequestBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxRequestBytes
	}
	if maxBytes < 1024 || maxBytes > 2<<20 {
		return nil, ErrInvalidConfiguration
	}
	handler := &Handler{
		sessions: config.Sessions, identity: config.Identity, platform: config.Platform, clock: config.Clock,
		allowedOrigins: origins, sessionTTL: config.SessionTTL, maxRequestBytes: maxBytes,
		random: func(value []byte) error { _, err := rand.Read(value); return err }, mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("GET /web/v1/customer/session", handler.getSession)
	handler.mux.HandleFunc("POST /web/v1/customer/session", handler.createSession)
	handler.mux.HandleFunc("DELETE /web/v1/customer/session", handler.deleteSession)
	handler.mux.HandleFunc("POST /web/v1/customer/session/refresh", handler.refreshSession)
	handler.mux.HandleFunc("/platform-api/", handler.proxyPlatform)
	return handler, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	handler.mux.ServeHTTP(writer, request)
}

func (handler *Handler) createSession(writer http.ResponseWriter, request *http.Request) {
	if !handler.originAllowed(request) {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_ORIGIN_INVALID", "The request origin is not allowed.")
		return
	}
	var input CreateSessionInput
	if !handler.decodeJSON(writer, request, &input) || !validCreateInput(input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CUSTOMER_SESSION_INPUT_INVALID", "Check the customer session request and try again.")
		return
	}
	existingToken, existing, _ := handler.resolveOptional(request)
	if input.Mode == SessionModeGuest && existing.ID != "" && existing.Guest && existing.Country == input.Country && existing.AccessExpiresAt.After(handler.clock().UTC().Add(2*time.Minute)) {
		writeJSON(writer, http.StatusOK, existing.View())
		return
	}

	var upstream PlatformSession
	var err error
	if input.Mode == SessionModeGuest {
		upstream, err = handler.identity.CreateGuest(request.Context(), input.Country)
	} else {
		upstream, err = handler.identity.Exchange(request.Context(), identity.ExchangeInput{
			Provider: input.Provider, ProviderToken: input.ProviderToken, DeviceID: input.DeviceID, Country: input.Country,
		})
	}
	if err != nil || upstream.Country != input.Country || !validPlatformSession(upstream, input.Mode, handler.clock()) {
		writeProblem(writer, request, http.StatusBadGateway, "CUSTOMER_IDENTITY_UNAVAILABLE", "The customer session could not be established.")
		return
	}
	session, err := handler.newSession(upstream)
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "CUSTOMER_SESSION_UNAVAILABLE", "The customer session could not be established.")
		return
	}
	var token string
	if existing.ID != "" {
		session.CSRFToken = existing.CSRFToken
		token, err = handler.sessions.Replace(request.Context(), existingToken, session)
	} else {
		token, err = handler.sessions.Create(request.Context(), session)
	}
	if err != nil {
		if upstream.RefreshToken != "" {
			_ = handler.identity.Revoke(context.WithoutCancel(request.Context()), upstream.RefreshToken)
		}
		writeProblem(writer, request, http.StatusServiceUnavailable, "CUSTOMER_SESSION_UNAVAILABLE", "The customer session could not be established.")
		return
	}
	if existing.RefreshToken != "" {
		_ = handler.identity.Revoke(context.WithoutCancel(request.Context()), existing.RefreshToken)
	}
	setSessionCookie(writer, token, session.ExpiresAt, handler.clock())
	writeJSON(writer, http.StatusCreated, session.View())
}

func (handler *Handler) getSession(writer http.ResponseWriter, request *http.Request) {
	_, session, err := handler.resolve(request)
	if err != nil {
		writeProblem(writer, request, http.StatusUnauthorized, "CUSTOMER_AUTHENTICATION_REQUIRED", "Create or restore a customer session.")
		return
	}
	writeJSON(writer, http.StatusOK, session.View())
}

func (handler *Handler) refreshSession(writer http.ResponseWriter, request *http.Request) {
	token, session, err := handler.resolve(request)
	if err != nil {
		writeProblem(writer, request, http.StatusUnauthorized, "CUSTOMER_AUTHENTICATION_REQUIRED", "Create or restore a customer session.")
		return
	}
	if !handler.mutationAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_REQUEST_SAFETY_INVALID", "The request safety token or origin is invalid.")
		return
	}
	if session.Guest || session.RefreshToken == "" {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_GUEST_REFRESH_FORBIDDEN", "Guest sessions cannot be refreshed.")
		return
	}
	if err := handler.sessions.AcquireRefresh(request.Context(), token, 30*time.Second); err != nil {
		if errors.Is(err, ErrRefreshInProgress) {
			writer.Header().Set("Retry-After", "1")
			writeProblem(writer, request, http.StatusServiceUnavailable, "CUSTOMER_REFRESH_IN_PROGRESS", "Another refresh is already completing. Try again.")
			return
		}
		writeProblem(writer, request, http.StatusUnauthorized, "CUSTOMER_AUTHENTICATION_REQUIRED", "Create or restore a customer session.")
		return
	}
	defer func() { _ = handler.sessions.ReleaseRefresh(context.WithoutCancel(request.Context()), token) }()
	tokens, err := handler.identity.Refresh(request.Context(), session.RefreshToken)
	if err != nil {
		if IdentityErrorIsRetryable(err) {
			writeProblem(writer, request, http.StatusServiceUnavailable, "CUSTOMER_IDENTITY_UNAVAILABLE", "The customer session could not be refreshed. Try again.")
			return
		}
		_ = handler.sessions.Delete(context.WithoutCancel(request.Context()), token)
		clearSessionCookie(writer)
		writeProblem(writer, request, http.StatusUnauthorized, "CUSTOMER_REFRESH_INVALID", "Sign in again to continue.")
		return
	}
	if !validTokenPair(tokens, handler.clock) {
		_ = handler.sessions.Delete(context.WithoutCancel(request.Context()), token)
		clearSessionCookie(writer)
		writeProblem(writer, request, http.StatusUnauthorized, "CUSTOMER_REFRESH_INVALID", "Sign in again to continue.")
		return
	}
	now := handler.clock().UTC()
	session.AccessToken, session.AccessExpiresAt = tokens.AccessToken, tokens.AccessExpiresAt
	session.RefreshToken, session.RefreshExpiresAt = tokens.RefreshToken, tokens.RefreshExpiresAt
	session.UpdatedAt = now
	session.ExpiresAt = minTime(now.Add(handler.sessionTTL), tokens.RefreshExpiresAt)
	newToken, err := handler.sessions.Replace(request.Context(), token, session)
	if err != nil {
		_ = handler.identity.Revoke(context.WithoutCancel(request.Context()), tokens.RefreshToken)
		writeProblem(writer, request, http.StatusServiceUnavailable, "CUSTOMER_SESSION_UNAVAILABLE", "The customer session could not be refreshed.")
		return
	}
	setSessionCookie(writer, newToken, session.ExpiresAt, now)
	writeJSON(writer, http.StatusOK, session.View())
}

func (handler *Handler) deleteSession(writer http.ResponseWriter, request *http.Request) {
	token, session, err := handler.resolve(request)
	if err != nil {
		clearSessionCookie(writer)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if !handler.mutationAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_REQUEST_SAFETY_INVALID", "The request safety token or origin is invalid.")
		return
	}
	if session.RefreshToken != "" {
		_ = handler.identity.Revoke(request.Context(), session.RefreshToken)
	}
	if err := handler.sessions.Delete(request.Context(), token); err != nil && !errors.Is(err, ErrSessionNotFound) {
		writeProblem(writer, request, http.StatusServiceUnavailable, "CUSTOMER_SESSION_UNAVAILABLE", "The customer session could not be closed.")
		return
	}
	clearSessionCookie(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) proxyPlatform(writer http.ResponseWriter, request *http.Request) {
	_, session, err := handler.resolve(request)
	if err != nil {
		writeProblem(writer, request, http.StatusUnauthorized, "CUSTOMER_AUTHENTICATION_REQUIRED", "Create or restore a customer session.")
		return
	}
	path := strings.TrimPrefix(request.URL.Path, platformPrefix)
	if path == "" || strings.HasPrefix(path, "/v1/auth/") || strings.HasPrefix(path, "/internal/") || strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, "/web/") {
		writeProblem(writer, request, http.StatusNotFound, "CUSTOMER_ROUTE_NOT_FOUND", "The requested customer route does not exist.")
		return
	}
	if session.Guest && !guestRequestAllowed(request.Method, path) {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_GUEST_MUTATION_FORBIDDEN", "Sign in before changing customer data.")
		return
	}
	if isMutation(request.Method) && !handler.mutationAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_REQUEST_SAFETY_INVALID", "The request safety token or origin is invalid.")
		return
	}
	clone := request.Clone(request.Context())
	clone.URL.Path = path
	clone.URL.RawPath = ""
	clone.RequestURI = ""
	stripBrowserTrustHeaders(clone.Header)
	clone.Header.Set("Authorization", "Bearer "+session.AccessToken)
	handler.platform.ServeHTTP(writer, clone)
}

func (handler *Handler) newSession(upstream PlatformSession) (Session, error) {
	now := handler.clock().UTC()
	csrf, err := opaqueToken(handler.random)
	if err != nil {
		return Session{}, err
	}
	expiresAt := minTime(now.Add(handler.sessionTTL), upstream.AccessExpiresAt)
	if !upstream.Guest {
		expiresAt = minTime(now.Add(handler.sessionTTL), upstream.RefreshExpiresAt)
	}
	displayName := strings.TrimSpace(upstream.DisplayName)
	if upstream.Guest {
		displayName = "Guest"
	}
	return Session{
		ID: uuid.NewString(), PlatformSession: upstream.PlatformSession, IdentityID: upstream.IdentityID,
		TenantID: upstream.TenantID, Country: upstream.Country, DisplayName: displayName,
		Roles: append([]identity.Role(nil), upstream.Roles...), Guest: upstream.Guest,
		AccessToken: upstream.AccessToken, AccessExpiresAt: upstream.AccessExpiresAt,
		RefreshToken: upstream.RefreshToken, RefreshExpiresAt: upstream.RefreshExpiresAt,
		CSRFToken: csrf, ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (handler *Handler) resolve(request *http.Request) (string, Session, error) {
	cookie, err := request.Cookie(SessionCookieName)
	if err != nil || !validOpaqueToken(cookie.Value) {
		return "", Session{}, ErrAuthenticationRequired
	}
	session, err := handler.sessions.Resolve(request.Context(), cookie.Value)
	if err != nil || !validSession(session) || !handler.clock().UTC().Before(session.ExpiresAt) {
		return "", Session{}, ErrAuthenticationRequired
	}
	return cookie.Value, session, nil
}

func (handler *Handler) resolveOptional(request *http.Request) (string, Session, error) {
	token, session, err := handler.resolve(request)
	if errors.Is(err, ErrAuthenticationRequired) {
		return "", Session{}, nil
	}
	return token, session, err
}

func (handler *Handler) originAllowed(request *http.Request) bool {
	if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	_, ok := handler.allowedOrigins[strings.TrimSpace(request.Header.Get("Origin"))]
	return ok
}

func (handler *Handler) mutationAllowed(request *http.Request, csrf string) bool {
	provided := request.Header.Get("X-CSRF-Token")
	return handler.originAllowed(request) && len(provided) == len(csrf) && len(csrf) >= 32 &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(csrf)) == 1
}

func (handler *Handler) decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, handler.maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func validCreateInput(input CreateSessionInput) bool {
	if !validCountry(input.Country) {
		return false
	}
	if input.Mode == SessionModeGuest {
		return input.Provider == "" && input.ProviderToken == "" && input.DeviceID == ""
	}
	if input.Mode != SessionModeAuthenticated || len(input.ProviderToken) < 8 || len(input.ProviderToken) > 8192 || len(input.DeviceID) < 1 || len(input.DeviceID) > 128 {
		return false
	}
	switch input.Provider {
	case "supabase", "firebase", "oidc", "local":
		return true
	default:
		return false
	}
}

func validPlatformSession(session PlatformSession, mode SessionMode, current time.Time) bool {
	now := current.UTC()
	if strings.TrimSpace(session.PlatformSession) == "" || strings.TrimSpace(session.TenantID) == "" || !validCountry(session.Country) ||
		len(session.AccessToken) < 8 || !session.AccessExpiresAt.After(now) || len(session.Roles) == 0 {
		return false
	}
	if mode == SessionModeGuest {
		return session.Guest && session.IdentityID == "" && session.RefreshToken == "" && len(session.Roles) == 1 && session.Roles[0] == identity.RoleGuest
	}
	if session.Guest || session.IdentityID == "" || session.DisplayName == "" || len(session.RefreshToken) < 8 || !session.RefreshExpiresAt.After(now) {
		return false
	}
	for _, role := range session.Roles {
		if role != identity.RoleCustomer && role != identity.RoleVendor && role != identity.RoleRider {
			return false
		}
	}
	return true
}

func validTokenPair(tokens identity.TokenPair, clock func() time.Time) bool {
	now := clock().UTC()
	return tokens.TokenType == "Bearer" && len(tokens.AccessToken) >= 8 && tokens.AccessExpiresAt.After(now) &&
		len(tokens.RefreshToken) >= 8 && tokens.RefreshExpiresAt.After(tokens.AccessExpiresAt)
}

func guestRequestAllowed(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		return false
	}
	for _, prefix := range []string{"/v1/bootstrap", "/v1/pages", "/v1/home", "/v1/catalog/", "/v1/services/", "/health/"} {
		if path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func stripBrowserTrustHeaders(header http.Header) {
	for name := range header {
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "Authorization" || canonical == "Cookie" || canonical == "X-Csrf-Token" || strings.HasPrefix(strings.ToLower(name), "x-planext4u-") || strings.HasPrefix(strings.ToLower(name), "x-p4u-") {
			header.Del(name)
		}
	}
}

func setSessionCookie(writer http.ResponseWriter, token string, expiresAt, now time.Time) {
	maxAge := int(expiresAt.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(writer, &http.Cookie{Name: SessionCookieName, Value: token, Path: "/", Expires: expiresAt.UTC(), MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{Name: SessionCookieName, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := strings.TrimSpace(request.Header.Get("X-Correlation-ID"))
	if correlationID == "" || len(correlationID) > 128 {
		correlationID = uuid.NewString()
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlationID, "retryable": status >= 500}})
}
