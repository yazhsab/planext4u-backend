package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServiceValidationAndDependencyFailuresAreTyped(t *testing.T) {
	t.Parallel()
	if _, err := NewService(ServiceConfig{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("empty service config error = %v", err)
	}
	fixture := newServiceFixture(t)
	invalidExchanges := []ExchangeInput{
		{Provider: "LOCAL", ProviderToken: "synthetic-token", DeviceID: "device", Country: "IN"},
		{Provider: "local", ProviderToken: "token with space", DeviceID: "device", Country: "IN"},
		{Provider: "local", ProviderToken: "synthetic-token", DeviceID: "device", Country: "US"},
	}
	for _, input := range invalidExchanges {
		if _, err := fixture.service.Exchange(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Exchange(%#v) error = %v", input, err)
		}
	}
	fixture.provider.err = ErrProviderRejected
	if _, err := fixture.service.Exchange(context.Background(), ExchangeInput{
		Provider: "local", ProviderToken: "synthetic-token", DeviceID: "device", Country: "IN",
	}); !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("provider rejection error = %v", err)
	}
	fixture.provider.err = errors.New("synthetic dependency outage")
	if _, err := fixture.service.Exchange(context.Background(), ExchangeInput{
		Provider: "local", ProviderToken: "synthetic-token", DeviceID: "device", Country: "IN",
	}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("provider outage error = %v", err)
	}
	fixture.provider.err = nil
	authentication := fixture.exchange(t, "provider-validation", "device-validation")
	trusted := trustedFor(authentication)
	if _, err := fixture.service.Authorize(context.Background(), trusted, Role("SUPERUSER")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid role error = %v", err)
	}
	for _, update := range []ProfileUpdate{
		{DisplayName: "", Locale: "en", TimeZone: "Asia/Kolkata", Version: 1},
		{DisplayName: "Name", Locale: "fr", TimeZone: "Asia/Kolkata", Version: 1},
		{DisplayName: "Name", Locale: "en", TimeZone: "Invalid/Zone", Version: 1},
	} {
		if _, err := fixture.service.UpdateProfile(context.Background(), trusted, update); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("invalid profile %#v error = %v", update, err)
		}
	}
	if _, err := fixture.service.RecordConsent(context.Background(), trusted, ConsentUpdate{
		Purpose: "UNSUPPORTED", Granted: true, PolicyVersion: "policy-v1",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid consent purpose error = %v", err)
	}
	if _, err := fixture.service.RecordConsent(context.Background(), trusted, ConsentUpdate{
		Purpose: ConsentAnalytics, Granted: true, PolicyVersion: "bad policy",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid consent policy error = %v", err)
	}
	if err := fixture.service.RevokeSession(context.Background(), trusted, "bad session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid session ID error = %v", err)
	}
}

func TestHandlerMapsEveryPublicFailureClassToSafeProblem(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	handler, err := NewHandler(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.provider.err = ErrProviderRejected
	rejected := identityRequest(handler, http.MethodPost, "/v1/auth/exchange", `{"provider":"local","provider_token":"synthetic-token","device_id":"device","country":"IN"}`, nil)
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("provider rejection = %d %s", rejected.Code, rejected.Body.String())
	}
	fixture.provider.err = ErrProviderUnavailable
	unavailable := identityRequest(handler, http.MethodPost, "/v1/auth/exchange", `{"provider":"local","provider_token":"synthetic-token","device_id":"device","country":"IN"}`, nil)
	if unavailable.Code != http.StatusServiceUnavailable || unavailable.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("provider unavailable = %d %s", unavailable.Code, unavailable.Body.String())
	}
	fixture.provider.err = nil
	malformedRefresh := identityRequest(handler, http.MethodPost, "/v1/auth/refresh", `{"refresh_token":"bad"}`, nil)
	if malformedRefresh.Code != http.StatusUnauthorized {
		t.Fatalf("malformed refresh = %d %s", malformedRefresh.Code, malformedRefresh.Body.String())
	}
	malformedRevoke := identityRequest(handler, http.MethodPost, "/v1/auth/revoke", `{`, nil)
	if malformedRevoke.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed revoke = %d %s", malformedRevoke.Code, malformedRevoke.Body.String())
	}
	authentication := fixture.exchange(t, "provider-handler-errors", "device-handler-errors")
	headers := trustedHeaders(authentication)
	missingMatch := identityRequest(handler, http.MethodPatch, "/v1/me", `{"display_name":"Name","locale":"en","time_zone":"Asia/Kolkata"}`, headers)
	if missingMatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing If-Match = %d %s", missingMatch.Code, missingMatch.Body.String())
	}
	missingConsentDecision := identityRequest(handler, http.MethodPut, "/v1/me/consents/ANALYTICS", `{"policy_version":"privacy-v1"}`, headers)
	if missingConsentDecision.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing consent decision = %d %s", missingConsentDecision.Code, missingConsentDecision.Body.String())
	}
	unknownSession := identityRequest(handler, http.MethodDelete, "/v1/me/sessions/session_unknown", "", headers)
	if unknownSession.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d %s", unknownSession.Code, unknownSession.Body.String())
	}
	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/v1/me/sessions"},
		{method: http.MethodGet, path: "/v1/me/consents"},
		{method: http.MethodGet, path: "/v1/me/data-export"},
		{method: http.MethodDelete, path: "/v1/me/sessions/session_missing"},
		{method: http.MethodPut, path: "/v1/me/consents/ANALYTICS", body: `{"granted":true,"policy_version":"privacy-v1"}`},
		{method: http.MethodPost, path: "/v1/me/deletion-requests", body: `{"confirmation":"DELETE MY ACCOUNT"}`},
	} {
		response := identityRequest(handler, request.method, request.path, request.body, nil)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "SESSION_INVALID") {
			t.Fatalf("missing trusted identity for %s = %d %s", request.path, response.Code, response.Body.String())
		}
	}
}

func TestTokenConfigurationAndEntropyFailuresFailClosed(t *testing.T) {
	t.Parallel()
	if _, err := NewJWTIssuer(JWTIssuerConfig{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("empty JWT config error = %v", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewJWTIssuer(JWTIssuerConfig{
		Issuer: "issuer", Audience: "audience", KeyID: "kid", Key: key,
		AccessTTL: time.Minute, Now: func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := issuer.Issue(Principal{
		Subject: "subject", Session: "session", TenantID: "tenant", Country: "XX",
		Roles: []Role{"UNSUPPORTED"}, AuthTime: time.Unix(1000, 0),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid principal error = %v", err)
	}
	if _, err := (RandomRefreshTokenFactory{Random: bytes.NewReader([]byte("short"))}).Generate(); err == nil {
		t.Fatal("short entropy source was accepted")
	}
	hasher, err := NewHMACRefreshHasher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hasher.DeviceReference("bad device"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid device reference error = %v", err)
	}
	if _, err := ParseRSAPrivateKeyPEM([]byte("not pem")); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid PEM error = %v", err)
	}
}
