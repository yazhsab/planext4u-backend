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
	RequestTimeout   time.Duration
	MaxRequestBytes  int64
	PublicPaths      map[string]struct{}
	AnonymousLimiter Limiter
	PrincipalLimiter Limiter
	Readiness        func(context.Context) error
}

func DefaultConfig(upstream *url.URL) Config {
	return Config{
		Upstream:        upstream,
		RequestTimeout:  10 * time.Second,
		MaxRequestBytes: 1 << 20,
		PublicPaths: map[string]struct{}{
			"/healthz": {},
			"/readyz":  {},
		},
		AnonymousLimiter: UnlimitedLimiter{},
		PrincipalLimiter: UnlimitedLimiter{},
	}
}

type Handler struct {
	config   Config
	verifier Verifier
	proxy    *httputil.ReverseProxy
	logger   *slog.Logger
}

func NewHandler(config Config, verifier Verifier, logger *slog.Logger) (*Handler, error) {
	if config.Upstream == nil ||
		(config.Upstream.Scheme != "http" && config.Upstream.Scheme != "https") ||
		config.Upstream.Host == "" ||
		config.RequestTimeout <= 0 || config.RequestTimeout > time.Minute ||
		config.MaxRequestBytes < 1 || config.MaxRequestBytes > 16<<20 ||
		config.AnonymousLimiter == nil || config.PrincipalLimiter == nil ||
		verifier == nil || logger == nil {
		return nil, ErrInvalidConfiguration
	}
	upstream := *config.Upstream
	config.Upstream = &upstream
	publicPaths := make(map[string]struct{}, len(config.PublicPaths))
	for path := range config.PublicPaths {
		publicPaths[path] = struct{}{}
	}
	config.PublicPaths = publicPaths

	handler := &Handler{config: config, verifier: verifier, logger: logger}
	proxy := &httputil.ReverseProxy{
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
	handler.proxy = proxy
	return handler, nil
}

func (handler *Handler) CloseIdleConnections() {
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
	if !handler.applyLimit(writer, request, handler.config.PrincipalLimiter, principalRateKey(request, principal)) {
		return
	}

	request = request.WithContext(context.WithValue(request.Context(), principalContextKey, principal))
	requestContext, cancel := context.WithTimeout(request.Context(), handler.config.RequestTimeout)
	defer cancel()
	request = request.WithContext(requestContext)

	started := time.Now()
	handler.proxy.ServeHTTP(writer, request)
	handler.logger.Info(
		"gateway request",
		"correlation_id", correlationID,
		"method", request.Method,
		"path_group", pathGroup(request.URL.Path),
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func (handler *Handler) servePublic(writer http.ResponseWriter, request *http.Request) {
	status := "ok"
	statusCode := http.StatusOK
	if request.URL.Path == "/readyz" && handler.config.Readiness != nil {
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

	proxyRequest.SetURL(handler.config.Upstream)
	proxyRequest.SetXForwarded()
	removeTrustedHeaders(proxyRequest.Out.Header)
	proxyRequest.Out.Header.Del("Authorization")
	proxyRequest.Out.Header.Del("Cookie")
	proxyRequest.Out.Header.Set(correlationIDHeader, correlationID)
	proxyRequest.Out.Header.Set("X-Planext4u-Subject", principal.Subject)
	proxyRequest.Out.Header.Set("X-Planext4u-Session", principal.SessionID)
	proxyRequest.Out.Header.Set("X-Planext4u-Tenant", principal.TenantID)
	proxyRequest.Out.Header.Set("X-Planext4u-Country", principal.Country)
	proxyRequest.Out.Header.Set("X-Planext4u-Roles", strings.Join(principal.Roles, ","))
	if principal.DeviceID != "" {
		proxyRequest.Out.Header.Set("X-Planext4u-Device", principal.DeviceID)
	}
	if !validTraceparent(proxyRequest.In.Header.Get("traceparent")) {
		proxyRequest.Out.Header.Del("traceparent")
		proxyRequest.Out.Header.Del("tracestate")
	}
}

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
		if strings.HasPrefix(strings.ToLower(name), "x-planext4u-") {
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
