package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type fixedVerifier struct {
	principal Principal
	err       error
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	return false, 0, errors.New("synthetic limiter failure")
}

type fakeRedisEvaluator struct {
	result []any
	err    error
	key    string
}

func (evaluator *fakeRedisEvaluator) Eval(
	_ context.Context,
	_ string,
	keys []string,
	_ ...any,
) *redis.Cmd {
	if len(keys) == 1 {
		evaluator.key = keys[0]
	}
	return redis.NewCmdResult(evaluator.result, evaluator.err)
}

func (verifier fixedVerifier) Verify(context.Context, string) (Principal, error) {
	return verifier.principal, verifier.err
}

func TestGatewayDerivesTrustedIdentityAndPropagatesCorrelation(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		path   string
		query  string
		body   string
		header http.Header
	}
	captured := make(chan capturedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		captured <- capturedRequest{
			path:   request.URL.Path,
			query:  request.URL.RawQuery,
			body:   string(body),
			header: request.Header.Clone(),
		}
		writer.Header().Set("Server", "unsafe-upstream")
		writer.Header().Set(correlationIDHeader, "upstream-correlation")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{principal: syntheticPrincipal()}, UnlimitedLimiter{}, UnlimitedLimiter{}, time.Second, 1024)
	request := httptest.NewRequest(http.MethodPost, "/v1/customer/home?country=IN", strings.NewReader(`{"fixture":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Authorization", "Bearer signed-token-value")
	request.Header.Set("Cookie", "session=must-not-propagate")
	request.Header.Set(correlationIDHeader, "corr-synthetic-gateway-001")
	request.Header.Set("X-Planext4u-Subject", "attacker-controlled")
	request.Header.Set("X-Planext4u-Provider-Signature", "verified-only-by-upstream")
	request.Header.Set("traceparent", "00-00000000000000000000000000000001-0000000000000001-01")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get(correlationIDHeader) != "corr-synthetic-gateway-001" {
		t.Fatalf("correlation response header = %q", response.Header().Get(correlationIDHeader))
	}
	if response.Header().Get("Server") != "" {
		t.Fatalf("unsafe Server response header propagated: %q", response.Header().Get("Server"))
	}
	got := <-captured
	if got.path != "/v1/customer/home" || got.query != "country=IN" || got.body != `{"fixture":true}` {
		t.Fatalf("upstream request = %#v", got)
	}
	if got.header.Get("Authorization") != "" || got.header.Get("Cookie") != "" {
		t.Fatalf("credentials propagated upstream: %#v", got.header)
	}
	if got.header.Get("X-Planext4u-Provider-Signature") != "verified-only-by-upstream" {
		t.Fatalf("provider signature was not preserved for upstream verification: %#v", got.header)
	}
	if got.header.Get("X-Planext4u-Subject") != "customer-synthetic-001" ||
		got.header.Get("X-Planext4u-Session") != "session-synthetic-001" ||
		got.header.Get("X-Planext4u-Tenant") != "tenant-synthetic-001" ||
		got.header.Get("X-Planext4u-Country") != "IN" ||
		got.header.Get("X-Planext4u-Roles") != "CUSTOMER" ||
		got.header.Get("X-Planext4u-Device") != "device-synthetic-001" {
		t.Fatalf("trusted headers = %#v", got.header)
	}
	if got.header.Get(correlationIDHeader) != "corr-synthetic-gateway-001" || got.header.Get("traceparent") == "" {
		t.Fatalf("trace headers = %#v", got.header)
	}
}

func TestGatewayDirectUpstreamEnforcesTheSameTrustBoundary(t *testing.T) {
	t.Parallel()

	upstream := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Error("browser credentials crossed the direct trust boundary")
		}
		if request.Header.Get("X-Planext4u-Tenant") != syntheticPrincipal().TenantID || request.Header.Get("X-Planext4u-Country") != "IN" {
			t.Errorf("trusted scope = %v", request.Header)
		}
		writer.Header().Set("Server", "unsafe-direct-upstream")
		writer.Header().Set("X-Powered-By", "unsafe-framework")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})
	config := DefaultConfig(nil)
	config.UpstreamHandler = upstream
	handler, err := NewHandler(config, fixedVerifier{principal: syntheticPrincipal()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/home", nil)
	request.Header.Set("Authorization", "Bearer valid")
	request.Header.Set("Cookie", "session=unsafe")
	request.Header.Set("X-Planext4u-Tenant", "attacker")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Server") != "" || response.Header().Get("X-Powered-By") != "" {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestGatewayRoutesToTheMostSpecificPrivateService(t *testing.T) {
	t.Parallel()

	base := mustURL(t, "http://platform.internal:8080")
	identity := mustURL(t, "http://identity.internal:8083")
	exchange := mustURL(t, "http://identity-exchange.internal:8083")
	config := DefaultConfig(base)
	config.UpstreamRoutes = map[string]*url.URL{
		"/v1/auth":          identity,
		"/v1/auth/exchange": exchange,
	}
	handler, err := NewHandler(config, fixedVerifier{principal: syntheticPrincipal()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := handler.upstreamForPath("/v1/auth/exchange"); got.String() != exchange.String() {
		t.Fatalf("exchange upstream = %s", got)
	}
	if got := handler.upstreamForPath("/v1/auth/refresh"); got.String() != identity.String() {
		t.Fatalf("auth upstream = %s", got)
	}
	if got := handler.upstreamForPath("/v1/authentic"); got.String() != base.String() {
		t.Fatalf("boundary-bypass upstream = %s", got)
	}
	if got := handler.upstreamForPath("/v1/catalog/items"); got.String() != base.String() {
		t.Fatalf("default upstream = %s", got)
	}
}

func TestGatewayRejectsUnsafeUpstreamRoutes(t *testing.T) {
	t.Parallel()

	for name, routes := range map[string]map[string]*url.URL{
		"relative prefix": {"v1/auth": mustURL(t, "http://identity.internal:8083")},
		"root prefix":     {"/": mustURL(t, "http://identity.internal:8083")},
		"missing target":  {"/v1/auth": nil},
		"unsafe scheme":   {"/v1/auth": mustURL(t, "ftp://identity.internal:21")},
	} {
		t.Run(name, func(t *testing.T) {
			config := DefaultConfig(mustURL(t, "http://platform.internal:8080"))
			config.UpstreamRoutes = routes
			if _, err := NewHandler(config, fixedVerifier{principal: syntheticPrincipal()}, slog.New(slog.NewTextHandler(io.Discard, nil))); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	config := DefaultConfig(nil)
	config.UpstreamHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	config.UpstreamRoutes = map[string]*url.URL{"/v1/auth": mustURL(t, "http://identity.internal:8083")}
	if _, err := NewHandler(config, fixedVerifier{principal: syntheticPrincipal()}, slog.New(slog.NewTextHandler(io.Discard, nil))); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("direct handler route error = %v", err)
	}
}

func TestGatewayAuthenticationDenialsUseSafeProblems(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("denied request reached upstream")
	}))
	defer upstream.Close()

	tests := []struct {
		name      string
		authority string
		verifier  fixedVerifier
		wantCode  string
	}{
		{name: "missing", verifier: fixedVerifier{principal: syntheticPrincipal()}, wantCode: "AUTHENTICATION_REQUIRED"},
		{name: "malformed", authority: "Basic unsafe", verifier: fixedVerifier{principal: syntheticPrincipal()}, wantCode: "AUTHENTICATION_REQUIRED"},
		{name: "invalid", authority: "Bearer invalid", verifier: fixedVerifier{err: ErrTokenInvalid}, wantCode: "AUTHENTICATION_INVALID"},
		{name: "expired", authority: "Bearer expired", verifier: fixedVerifier{err: ErrTokenExpired}, wantCode: "AUTHENTICATION_EXPIRED"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newGatewayTestHandler(t, upstream.URL, test.verifier, UnlimitedLimiter{}, UnlimitedLimiter{}, time.Second, 1024)
			request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
			request.Header.Set("Authorization", test.authority)
			request.Header.Set(correlationIDHeader, "corr-synthetic-denial")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized ||
				!strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) ||
				!strings.Contains(response.Body.String(), `"correlation_id":"corr-synthetic-denial"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if test.authority != "" && strings.Contains(response.Body.String(), test.authority) {
				t.Fatal("authentication input leaked in response")
			}
		})
	}
}

func TestGatewayForwardsOnlyExactAnonymousAuthRoutes(t *testing.T) {
	t.Parallel()

	captured := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured <- request.Header.Clone()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"accepted":true}`))
	}))
	defer upstream.Close()
	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{err: ErrTokenInvalid}, UnlimitedLimiter{}, UnlimitedLimiter{}, time.Second, 1024)

	request := httptest.NewRequest(http.MethodPost, "/v1/auth/exchange", strings.NewReader(`{"provider_token":"secret"}`))
	request.Header.Set("Authorization", "Bearer attacker-value")
	request.Header.Set("X-Planext4u-Roles", "ADMIN")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("anonymous exchange status = %d body=%s", response.Code, response.Body.String())
	}
	upstreamHeader := <-captured
	if upstreamHeader.Get("Authorization") != "" || upstreamHeader.Get("X-Planext4u-Roles") != "" {
		t.Fatalf("anonymous route received credential or trusted identity headers: %v", upstreamHeader)
	}

	wrongMethod := httptest.NewRequest(http.MethodGet, "/v1/auth/exchange", nil)
	wrongMethodResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethodResponse, wrongMethod)
	if wrongMethodResponse.Code != http.StatusUnauthorized || !strings.Contains(wrongMethodResponse.Body.String(), "AUTHENTICATION_REQUIRED") {
		t.Fatalf("wrong-method response = %d %s", wrongMethodResponse.Code, wrongMethodResponse.Body.String())
	}

	prefixBypass := httptest.NewRequest(http.MethodPost, "/v1/auth/exchange/extra", nil)
	prefixResponse := httptest.NewRecorder()
	handler.ServeHTTP(prefixResponse, prefixBypass)
	if prefixResponse.Code != http.StatusUnauthorized {
		t.Fatalf("prefix bypass status = %d", prefixResponse.Code)
	}
}

func TestGatewayEnforcesDeclaredAndStreamingBodyLimits(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("oversized request reached upstream")
	}))
	defer upstream.Close()
	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{principal: syntheticPrincipal()}, UnlimitedLimiter{}, UnlimitedLimiter{}, time.Second, 8)

	for _, contentLength := range []int64{9, -1} {
		request := httptest.NewRequest(http.MethodPost, "/v1/private", strings.NewReader("123456789"))
		request.ContentLength = contentLength
		request.Header.Set("Authorization", "Bearer valid")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "REQUEST_TOO_LARGE") {
			t.Fatalf("content length %d response = %d %s", contentLength, response.Code, response.Body.String())
		}
	}
}

func TestGatewayAppliesAnonymousAndPrincipalRateLimits(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	anonymous, err := NewMemoryLimiter(10, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewMemoryLimiter(1, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{principal: syntheticPrincipal()}, anonymous, principal, time.Second, 1024)

	for attempt := 1; attempt <= 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
		request.RemoteAddr = "192.0.2.20:1234"
		request.Header.Set("Authorization", "Bearer valid")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if attempt == 1 && response.Code != http.StatusOK {
			t.Fatalf("first status = %d", response.Code)
		}
		if attempt == 2 && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" || !strings.Contains(response.Body.String(), "RATE_LIMITED")) {
			t.Fatalf("limited response = %d %s headers=%v", response.Code, response.Body.String(), response.Header())
		}
	}
}

func TestGatewayFailsClosedWhenRateLimitStoreIsUnavailable(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request reached upstream while limiter was unavailable")
	}))
	defer upstream.Close()
	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{principal: syntheticPrincipal()}, failingLimiter{}, UnlimitedLimiter{}, time.Second, 1024)
	request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "RATE_LIMIT_UNAVAILABLE") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayMapsTimeoutUnavailableAndInvalidDependencyResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		upstream   func(t *testing.T) (string, func())
		timeout    time.Duration
		wantStatus int
		wantCode   string
	}{
		{
			name: "timeout",
			upstream: func(t *testing.T) (string, func()) {
				server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					<-request.Context().Done()
				}))
				return server.URL, server.Close
			},
			timeout:    20 * time.Millisecond,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "UPSTREAM_TIMEOUT",
		},
		{
			name: "unavailable",
			upstream: func(*testing.T) (string, func()) {
				return "http://127.0.0.1:1", func() {}
			},
			timeout:    time.Second,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "UPSTREAM_UNAVAILABLE",
		},
		{
			name: "invalid error envelope",
			upstream: func(t *testing.T) (string, func()) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.WriteHeader(http.StatusInternalServerError)
					_, _ = writer.Write([]byte("internal stack trace"))
				}))
				return server.URL, server.Close
			},
			timeout:    time.Second,
			wantStatus: http.StatusBadGateway,
			wantCode:   "UPSTREAM_RESPONSE_INVALID",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			upstream, closeUpstream := test.upstream(t)
			defer closeUpstream()
			handler := newGatewayTestHandler(t, upstream, fixedVerifier{principal: syntheticPrincipal()}, UnlimitedLimiter{}, UnlimitedLimiter{}, test.timeout, 1024)
			request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
			request.Header.Set("Authorization", "Bearer valid")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body.String(), test.wantStatus, test.wantCode)
			}
			if strings.Contains(response.Body.String(), "stack trace") {
				t.Fatal("unsafe dependency error leaked")
			}
		})
	}
}

func TestGatewayNormalizesSafeDependencyProblem(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"error":{"code":"LOCATION_INVALID","message":"Choose another location.","retryable":false},"stack":"must be stripped"}`))
	}))
	defer upstream.Close()
	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{principal: syntheticPrincipal()}, UnlimitedLimiter{}, UnlimitedLimiter{}, time.Second, 1024)
	request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
	request.Header.Set("Authorization", "Bearer valid")
	request.Header.Set(correlationIDHeader, "corr-synthetic-normalized")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), "LOCATION_INVALID") ||
		!strings.Contains(response.Body.String(), "corr-synthetic-normalized") ||
		strings.Contains(response.Body.String(), "stack") {
		t.Fatalf("normalized response = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayPublicHealthAndInvalidTraceAreSafe(t *testing.T) {
	t.Parallel()

	capturedTrace := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capturedTrace <- request.Header.Get("traceparent") + request.Header.Get("tracestate")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	handler := newGatewayTestHandler(t, upstream.URL, fixedVerifier{principal: syntheticPrincipal()}, UnlimitedLimiter{}, UnlimitedLimiter{}, time.Second, 1024)

	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRequest.Header.Set(correlationIDHeader, "contains space")
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK || healthResponse.Header().Get(correlationIDHeader) == "" || healthResponse.Header().Get(correlationIDHeader) == "contains space" {
		t.Fatalf("health response = %d headers=%v", healthResponse.Code, healthResponse.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
	request.Header.Set("Authorization", "Bearer valid")
	request.Header.Set("traceparent", "00-invalid")
	request.Header.Set("tracestate", "vendor=value")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || <-capturedTrace != "" {
		t.Fatalf("invalid trace propagated or request failed: %d", response.Code)
	}
}

func TestGatewayReadinessFailsWithoutLeakingDependencyError(t *testing.T) {
	t.Parallel()

	config := DefaultConfig(mustURL(t, "http://127.0.0.1:8090"))
	config.Readiness = func(context.Context) error {
		return errors.New("redis://user:secret@cache.internal")
	}
	handler, err := NewHandler(
		config,
		fixedVerifier{principal: syntheticPrincipal()},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not_ready") {
		t.Fatalf("readiness response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "redis") {
		t.Fatalf("dependency details leaked: %s", response.Body.String())
	}
}

func TestGatewayLogsExcludeCredentials(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	config := DefaultConfig(mustURL(t, upstream.URL))
	config.AnonymousLimiter = UnlimitedLimiter{}
	config.PrincipalLimiter = UnlimitedLimiter{}
	handler, err := NewHandler(config, fixedVerifier{principal: syntheticPrincipal()}, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/private", nil)
	request.Header.Set("Authorization", "Bearer secret-token-must-not-log")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Contains(logs.String(), "secret-token-must-not-log") || strings.Contains(logs.String(), "Authorization") {
		t.Fatalf("credential leaked in log: %s", logs.String())
	}
}

func TestMemoryLimiterResetsAndRemainsBounded(t *testing.T) {
	t.Parallel()

	limiter, err := NewMemoryLimiter(1, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	limiter.now = func() time.Time { return now }
	if allowed, _, _ := limiter.Allow(context.Background(), "one"); !allowed {
		t.Fatal("first request denied")
	}
	if allowed, _, _ := limiter.Allow(context.Background(), "one"); allowed {
		t.Fatal("second request allowed")
	}
	_, _, _ = limiter.Allow(context.Background(), "two")
	_, _, _ = limiter.Allow(context.Background(), "three")
	if len(limiter.entries) != 2 {
		t.Fatalf("entry count = %d", len(limiter.entries))
	}
	now = now.Add(time.Minute)
	if allowed, _, _ := limiter.Allow(context.Background(), "one"); !allowed {
		t.Fatal("request remained denied after window reset")
	}
	if _, err := NewMemoryLimiter(0, time.Minute, 1); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid limiter error = %v", err)
	}
}

func TestRedisLimiterUsesAtomicResultAndFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    []any
		err       error
		wantAllow bool
		wantRetry time.Duration
		wantError bool
	}{
		{name: "inside limit", result: []any{int64(2), int64(45_000)}, wantAllow: true},
		{name: "over limit", result: []any{int64(3), int64(45_000)}, wantRetry: 45 * time.Second},
		{name: "redis unavailable", err: errors.New("synthetic Redis outage"), wantError: true},
		{name: "malformed result", result: []any{"two", int64(45_000)}, wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evaluator := &fakeRedisEvaluator{result: test.result, err: test.err}
			limiter, err := NewRedisLimiter(evaluator, "gateway:test", 2, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			allowed, retryAfter, err := limiter.Allow(context.Background(), "hashed-key")
			if allowed != test.wantAllow || retryAfter != test.wantRetry || (err != nil) != test.wantError {
				t.Fatalf("Allow() = %v, %v, %v", allowed, retryAfter, err)
			}
			if evaluator.key != "gateway:test:hashed-key" {
				t.Fatalf("Redis key = %q", evaluator.key)
			}
		})
	}
}

func newGatewayTestHandler(
	t *testing.T,
	upstream string,
	verifier Verifier,
	anonymous Limiter,
	principal Limiter,
	timeout time.Duration,
	maxRequestBytes int64,
) *Handler {
	t.Helper()
	config := DefaultConfig(mustURL(t, upstream))
	config.RequestTimeout = timeout
	config.MaxRequestBytes = maxRequestBytes
	config.AnonymousLimiter = anonymous
	config.PrincipalLimiter = principal
	handler, err := NewHandler(config, verifier, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}

func syntheticPrincipal() Principal {
	return Principal{
		Subject:   "customer-synthetic-001",
		SessionID: "session-synthetic-001",
		TenantID:  "tenant-synthetic-001",
		Country:   "IN",
		DeviceID:  "device-synthetic-001",
		Roles:     []string{"CUSTOMER"},
	}
}
