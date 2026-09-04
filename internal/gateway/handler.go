package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	correlationIDHeader = "X-Correlation-ID"
	maxCorrelationIDLen = 128
	maxErrorBodyBytes   = 64 * 1024
)

type contextKey string

const (
	correlationContextKey contextKey = "correlation_id"
	principalContextKey   contextKey = "principal"
)

type Config struct {
	Upstream         *url.URL
	UpstreamRoutes   map[string]*url.URL
	UpstreamHandler  http.Handler
	RequestTimeout   time.Duration
	MaxRequestBytes  int64
	PublicPaths      map[string]struct{}
	AnonymousRoutes  map[string]map[string]struct{}
	AnonymousLimiter Limiter
	GuestLimiter     Limiter
	PrincipalLimiter Limiter
	Readiness        func(context.Context) error
}

func DefaultConfig(upstream *url.URL) Config {
	return Config{
		Upstream:        upstream,
		RequestTimeout:  10 * time.Second,
		MaxRequestBytes: 1 << 20,
		PublicPaths: map[string]struct{}{
			"/healthz":      {},
			"/readyz":       {},
			"/health/ready": {},
		},
		AnonymousRoutes: map[string]map[string]struct{}{
			"/v1/auth/exchange": {http.MethodPost: {}},
			"/v1/auth/refresh":  {http.MethodPost: {}},
			"/v1/auth/revoke":   {http.MethodPost: {}},
		},
		AnonymousLimiter: UnlimitedLimiter{},
		GuestLimiter:     UnlimitedLimiter{},
		PrincipalLimiter: UnlimitedLimiter{},
	}
}

type Handler struct {
	config   Config
	verifier Verifier
	proxy    *httputil.ReverseProxy
	upstream http.Handler
	logger   *slog.Logger
}

func NewHandler(config Config, verifier Verifier, logger *slog.Logger) (*Handler, error) {
	if (config.Upstream == nil) == (config.UpstreamHandler == nil) ||
		(config.Upstream != nil && ((config.Upstream.Scheme != "http" && config.Upstream.Scheme != "https") || config.Upstream.Host == "")) ||
		(config.UpstreamHandler != nil && len(config.UpstreamRoutes) > 0) ||
		config.RequestTimeout <= 0 || config.RequestTimeout > time.Minute ||
		config.MaxRequestBytes < 1 || config.MaxRequestBytes > 16<<20 ||
		config.AnonymousLimiter == nil || config.GuestLimiter == nil || config.PrincipalLimiter == nil ||
		verifier == nil || logger == nil {
		return nil, ErrInvalidConfiguration
	}
	if config.Upstream != nil {
		upstream := *config.Upstream
		config.Upstream = &upstream
	}
	upstreamRoutes := make(map[string]*url.URL, len(config.UpstreamRoutes))
	for prefix, candidate := range config.UpstreamRoutes {
		if !validRoutePrefix(prefix) || candidate == nil ||
			(candidate.Scheme != "http" && candidate.Scheme != "https") || candidate.Host == "" {
			return nil, ErrInvalidConfiguration
		}
		cloned := *candidate
		upstreamRoutes[prefix] = &cloned
	}
	config.UpstreamRoutes = upstreamRoutes
	publicPaths := make(map[string]struct{}, len(config.PublicPaths))
	for path := range config.PublicPaths {
		publicPaths[path] = struct{}{}
	}
	config.PublicPaths = publicPaths
	anonymousRoutes := make(map[string]map[string]struct{}, len(config.AnonymousRoutes))
	for path, methods := range config.AnonymousRoutes {
		if !strings.HasPrefix(path, "/") || len(methods) == 0 {
			return nil, ErrInvalidConfiguration
		}
		clonedMethods := make(map[string]struct{}, len(methods))
		for method := range methods {
			if method != http.MethodPost {
				return nil, ErrInvalidConfiguration
			}
			clonedMethods[method] = struct{}{}
		}
		anonymousRoutes[path] = clonedMethods
	}
	config.AnonymousRoutes = anonymousRoutes

	handler := &Handler{config: config, verifier: verifier, logger: logger, upstream: config.UpstreamHandler}
	if config.Upstream != nil {
		handler.proxy = &httputil.ReverseProxy{
			Rewrite:        handler.rewrite,
			ModifyResponse: handler.modifyResponse,
			ErrorHandler:   handler.handleProxyError,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: config.RequestTimeout,
			},
		}
	}
	return handler, nil
}

func (handler *Handler) CloseIdleConnections() {
	if handler.proxy == nil {
		return
	}
	if transport, ok := handler.proxy.Transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	correlationID := validCorrelationID(request.Header.Get(correlationIDHeader))
	if correlationID == "" {
		correlationID = newCorrelationID()
	}
	ctx := context.WithValue(request.Context(), correlationContextKey, correlationID)
	request = request.WithContext(ctx)
	writer.Header().Set(correlationIDHeader, correlationID)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

	if _, public := handler.config.PublicPaths[request.URL.Path]; public {
		handler.servePublic(writer, request)
		return
	}

	if !handler.applyLimit(writer, request, handler.config.AnonymousLimiter, anonymousRateKey(request)) {
		return
	}
	if !handler.readBoundedBody(writer, request) {
		return
	}
	if handler.isAnonymousRoute(request) {
		handler.forward(writer, request)
		return
	}

	token, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		writeProblem(
			writer,
			request,
			http.StatusUnauthorized,
			"AUTHENTICATION_REQUIRED",
			"A valid bearer token is required.",
			false,
		)
		return
	}
	principal, err := handler.verifier.Verify(request.Context(), token)
	if err != nil {
		code := "AUTHENTICATION_INVALID"
		message := "The session could not be verified. Sign in again."
		if errors.Is(err, ErrTokenExpired) {
			code = "AUTHENTICATION_EXPIRED"
			message = "The session expired. Sign in again."
		}
		writeProblem(writer, request, http.StatusUnauthorized, code, message, false)
		return
	}
	if containsString(principal.Roles, "GUEST") {
		if !handler.applyLimit(writer, request, handler.config.GuestLimiter, principalRateKey(request, principal)) {
			return
		}
		if !guestRequestAllowed(request, principal) {
			writeProblem(writer, request, http.StatusForbidden, "GUEST_ACCESS_FORBIDDEN", "Guest sessions may only read the public storefront for their selected country.", false)
			return
		}
	} else {
		if !handler.applyLimit(writer, request, handler.config.PrincipalLimiter, principalRateKey(request, principal)) {
			return
		}
	}

	request = request.WithContext(context.WithValue(request.Context(), principalContextKey, principal))
	handler.forward(writer, request)
}

func guestRequestAllowed(request *http.Request, principal Principal) bool {
	if len(principal.Roles) != 1 || principal.Roles[0] != "GUEST" || request.Method != http.MethodGet {
		return false
	}
	for _, key := range []string{"country", "country_code"} {
		values, exists := request.URL.Query()[key]
		if exists && (len(values) != 1 || strings.ToUpper(strings.TrimSpace(values[0])) != principal.Country) {
			return false
		}
	}
	path := request.URL.Path
	if path == "/v1/bootstrap" || path == "/v1/home" || path == "/v1/catalog/categories" ||
		path == "/v1/catalog/items" || path == "/v1/catalog/search" || path == "/v1/catalog/suggestions" ||
		path == "/v1/serviceability/check" || path == "/v1/geocoding/search" {
		return true
	}
	return routePrefixMatches("/v1/pages", path) ||
		routePrefixMatches("/v1/catalog/items", path) ||
		routePrefixMatches("/v1/homes/listings", path) ||
		routePrefixMatches("/v1/classifieds/listings", path)
}

func (handler *Handler) isAnonymousRoute(request *http.Request) bool {
	methods, exists := handler.config.AnonymousRoutes[request.URL.Path]
	if !exists {
		return false
	}
	_, allowed := methods[request.Method]
	return allowed
}

func (handler *Handler) forward(writer http.ResponseWriter, request *http.Request) {
	requestContext, cancel := context.WithTimeout(request.Context(), handler.config.RequestTimeout)
	defer cancel()
	request = request.WithContext(requestContext)

	started := time.Now()
	if handler.upstream != nil {
		directRequest := request.Clone(request.Context())
		directRequest.Header = request.Header.Clone()
		handler.prepareTrustedRequest(directRequest)
		handler.upstream.ServeHTTP(&sanitizingResponseWriter{ResponseWriter: writer}, directRequest)
	} else {
		handler.proxy.ServeHTTP(writer, request)
	}
	handler.logger.Info(
		"gateway request",
		"correlation_id", correlationIDFromContext(request.Context()),
		"method", request.Method,
		"path_group", pathGroup(request.URL.Path),
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func (handler *Handler) servePublic(writer http.ResponseWriter, request *http.Request) {
	status := "ok"
	statusCode := http.StatusOK
	if (request.URL.Path == "/readyz" || request.URL.Path == "/health/ready") && handler.config.Readiness != nil {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if err := handler.config.Readiness(ctx); err != nil {
			status = "not_ready"
			statusCode = http.StatusServiceUnavailable
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(map[string]string{
		"status":  status,
		"service": "gateway",
	})
}

func (handler *Handler) readBoundedBody(writer http.ResponseWriter, request *http.Request) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	defer request.Body.Close()
	if request.ContentLength > handler.config.MaxRequestBytes {
		writeProblem(
			writer,
			request,
			http.StatusRequestEntityTooLarge,
			"REQUEST_TOO_LARGE",
			"The request exceeds the allowed size.",
			false,
		)
		return false
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.config.MaxRequestBytes+1))
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "REQUEST_BODY_INVALID", "The request body could not be read.", false)
		return false
	}
	if int64(len(body)) > handler.config.MaxRequestBytes {
		writeProblem(
			writer,
			request,
			http.StatusRequestEntityTooLarge,
			"REQUEST_TOO_LARGE",
			"The request exceeds the allowed size.",
			false,
		)
		return false
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	return true
}

func (handler *Handler) applyLimit(
	writer http.ResponseWriter,
	request *http.Request,
	limiter Limiter,
	key string,
) bool {
	allowed, retryAfter, err := limiter.Allow(request.Context(), key)
	if err != nil {
		writeProblem(
			writer,
			request,
			http.StatusServiceUnavailable,
			"RATE_LIMIT_UNAVAILABLE",
			"The request safety service is temporarily unavailable.",
			true,
		)
		return false
	}
	if allowed {
		return true
	}
	seconds := int64(retryAfter.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	writer.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeProblem(
		writer,
		request,
		http.StatusTooManyRequests,
		"RATE_LIMITED",
		"Too many requests. Try again shortly.",
		true,
	)
	return false
}

func (handler *Handler) rewrite(proxyRequest *httputil.ProxyRequest) {
	principal, _ := proxyRequest.In.Context().Value(principalContextKey).(Principal)
	correlationID := correlationIDFromContext(proxyRequest.In.Context())

	proxyRequest.SetURL(handler.upstreamForPath(proxyRequest.In.URL.Path))
	proxyRequest.SetXForwarded()
	removeTrustedHeaders(proxyRequest.Out.Header)
	proxyRequest.Out.Header.Del("Authorization")
	proxyRequest.Out.Header.Del("Cookie")
	proxyRequest.Out.Header.Set(correlationIDHeader, correlationID)
	if principal.Subject != "" {
		proxyRequest.Out.Header.Set("X-Planext4u-Subject", principal.Subject)
		proxyRequest.Out.Header.Set("X-Planext4u-Session", principal.SessionID)
		proxyRequest.Out.Header.Set("X-Planext4u-Tenant", principal.TenantID)
		proxyRequest.Out.Header.Set("X-Planext4u-Country", principal.Country)
		proxyRequest.Out.Header.Set("X-Planext4u-Roles", strings.Join(principal.Roles, ","))
		if principal.MFAVerified {
			proxyRequest.Out.Header.Set("X-Planext4u-MFA", "verified")
		}
		if principal.DeviceID != "" {
			proxyRequest.Out.Header.Set("X-Planext4u-Device", principal.DeviceID)
		}
	}
	if !validTraceparent(proxyRequest.In.Header.Get("traceparent")) {
		proxyRequest.Out.Header.Del("traceparent")
		proxyRequest.Out.Header.Del("tracestate")
	}
}

func (handler *Handler) upstreamForPath(path string) *url.URL {
	selected := handler.config.Upstream
	selectedLength := -1
	for prefix, candidate := range handler.config.UpstreamRoutes {
		if routePrefixMatches(prefix, path) && len(prefix) > selectedLength {
			selected, selectedLength = candidate, len(prefix)
		}
	}
	return selected
}

func validRoutePrefix(value string) bool {
	if value == "" || value == "/" || value[0] != '/' || strings.HasSuffix(value, "/") || strings.ContainsAny(value, "?#") {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character >= 0x7f {
			return false
		}
	}
	return true
}

func routePrefixMatches(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (handler *Handler) prepareTrustedRequest(request *http.Request) {
	principal, _ := request.Context().Value(principalContextKey).(Principal)
	removeTrustedHeaders(request.Header)
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	request.Header.Set(correlationIDHeader, correlationIDFromContext(request.Context()))
	if principal.Subject != "" {
		request.Header.Set("X-Planext4u-Subject", principal.Subject)
		request.Header.Set("X-Planext4u-Session", principal.SessionID)
		request.Header.Set("X-Planext4u-Tenant", principal.TenantID)
		request.Header.Set("X-Planext4u-Country", principal.Country)
		request.Header.Set("X-Planext4u-Roles", strings.Join(principal.Roles, ","))
		if principal.MFAVerified {
			request.Header.Set("X-Planext4u-MFA", "verified")
		}
		if principal.DeviceID != "" {
			request.Header.Set("X-Planext4u-Device", principal.DeviceID)
		}
	}
	if !validTraceparent(request.Header.Get("traceparent")) {
		request.Header.Del("traceparent")
		request.Header.Del("tracestate")
	}
}

type sanitizingResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (writer *sanitizingResponseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.Header().Del("Server")
	writer.Header().Del("X-Powered-By")
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *sanitizingResponseWriter) Write(value []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(value)
}

func (writer *sanitizingResponseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (handler *Handler) modifyResponse(response *http.Response) error {
	response.Header.Del("Server")
	response.Header.Del("X-Powered-By")
	response.Header.Set(correlationIDHeader, correlationIDFromContext(response.Request.Context()))
	if response.StatusCode < http.StatusBadRequest {
		return nil
	}

	contents, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	response.Body.Close()
	if err != nil || len(contents) > maxErrorBodyBytes {
		return &boundaryError{code: "UPSTREAM_RESPONSE_INVALID"}
	}
	var envelope problemEnvelope
	if err := json.Unmarshal(contents, &envelope); err != nil ||
		envelope.Error.Code == "" || envelope.Error.Message == "" {
		return &boundaryError{code: "UPSTREAM_RESPONSE_INVALID"}
	}
	envelope.Error.CorrelationID = correlationIDFromContext(response.Request.Context())
	if envelope.Error.FieldErrors == nil {
		envelope.Error.FieldErrors = []fieldError{}
	}
	if envelope.Error.Details == nil {
		envelope.Error.Details = map[string]any{}
	}
	normalized, err := json.Marshal(envelope)
	if err != nil {
		return &boundaryError{code: "UPSTREAM_RESPONSE_INVALID"}
	}
	response.Body = io.NopCloser(bytes.NewReader(append(normalized, '\n')))
	response.ContentLength = int64(len(normalized) + 1)
	response.Header.Set("Content-Type", "application/json")
	response.Header.Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	response.Header.Set("Cache-Control", "no-store")
	return nil
}

type boundaryError struct {
	code string
}

func (problem *boundaryError) Error() string {
	return problem.code
}

func (handler *Handler) handleProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	var invalidResponse *boundaryError
	if errors.As(err, &invalidResponse) {
		writeProblem(
			writer,
			request,
			http.StatusBadGateway,
			invalidResponse.code,
			"A dependency returned an invalid response.",
			true,
		)
		return
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		writeProblem(
			writer,
			request,
			http.StatusGatewayTimeout,
			"UPSTREAM_TIMEOUT",
			"A dependency did not respond in time.",
			true,
		)
		return
	}
	writeProblem(
		writer,
		request,
		http.StatusServiceUnavailable,
		"UPSTREAM_UNAVAILABLE",
		"A required service is temporarily unavailable.",
		true,
	)
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = parts[1]
	}
	return returnValue, returnValue != ""
}

func validCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxCorrelationIDLen {
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
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func correlationIDFromContext(ctx context.Context) string {
	correlationID, _ := ctx.Value(correlationContextKey).(string)
	return correlationID
}

func removeTrustedHeaders(header http.Header) {
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-planext4u-") && !strings.EqualFold(name, "X-Planext4u-Provider-Signature") {
			header.Del(name)
		}
	}
}

func anonymousRateKey(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	return digestRateKey("ip\x00" + host)
}

func principalRateKey(request *http.Request, principal Principal) string {
	return digestRateKey(strings.Join([]string{
		anonymousRateKey(request),
		principal.Subject,
		principal.SessionID,
		principal.DeviceID,
	}, "\x00"))
}

func digestRateKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func pathGroup(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return "/"
	}
	if len(segments) > 2 {
		segments = segments[:2]
	}
	return "/" + strings.Join(segments, "/")
}

func validTraceparent(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return false
	}
	for _, part := range parts {
		if _, err := hex.DecodeString(part); err != nil {
			return false
		}
	}
	return parts[1] != strings.Repeat("0", 32) && parts[2] != strings.Repeat("0", 16)
}
