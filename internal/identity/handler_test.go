package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerAuthenticationLifecycleIsBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	handler, err := NewHandler(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	providerToken := "provider-http-customer"
	deviceID := "device-http-private"
	exchangeResponse := identityRequest(handler, http.MethodPost, "/v1/auth/exchange", `{
      "provider":"local",
      "provider_token":"`+providerToken+`",
      "device_id":"`+deviceID+`",
      "country":"IN"
    }`, nil)
	if exchangeResponse.Code != http.StatusCreated {
		t.Fatalf("exchange response = %d %s", exchangeResponse.Code, exchangeResponse.Body.String())
	}
	if strings.Contains(exchangeResponse.Body.String(), providerToken) || strings.Contains(exchangeResponse.Body.String(), deviceID) {
		t.Fatalf("restricted provider/device input leaked: %s", exchangeResponse.Body.String())
	}
	var authentication Authentication
	if err := json.Unmarshal(exchangeResponse.Body.Bytes(), &authentication); err != nil {
		t.Fatal(err)
	}
	if authentication.Tokens.RefreshToken == "" || authentication.IdentityID == "" {
		t.Fatalf("authentication response incomplete: %#v", authentication)
	}

	refreshBody := `{"refresh_token":"` + authentication.Tokens.RefreshToken + `"}`
	refreshResponse := identityRequest(handler, http.MethodPost, "/v1/auth/refresh", refreshBody, nil)
	if refreshResponse.Code != http.StatusOK {
		t.Fatalf("refresh response = %d %s", refreshResponse.Code, refreshResponse.Body.String())
	}
	reuseResponse := identityRequest(handler, http.MethodPost, "/v1/auth/refresh", refreshBody, nil)
	if reuseResponse.Code != http.StatusUnauthorized ||
		!strings.Contains(reuseResponse.Body.String(), "SESSION_INVALID") ||
		strings.Contains(strings.ToLower(reuseResponse.Body.String()), "reuse") {
		t.Fatalf("reuse response is unsafe = %d %s", reuseResponse.Code, reuseResponse.Body.String())
	}

	unknown := identityRequest(handler, http.MethodPost, "/v1/auth/exchange", `{"provider":"local","provider_token":"synthetic-token","device_id":"device","country":"IN","role":"ADMIN"}`, nil)
	if unknown.Code != http.StatusUnprocessableEntity || !strings.Contains(unknown.Body.String(), "VALIDATION_FAILED") {
		t.Fatalf("unknown-field response = %d %s", unknown.Code, unknown.Body.String())
	}
	oversized := identityRequest(handler, http.MethodPost, "/v1/auth/exchange", `{"padding":"`+strings.Repeat("x", maxIdentityRequestBytes)+`"}`, nil)
	if oversized.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversized.Body.String(), "REQUEST_TOO_LARGE") {
		t.Fatalf("oversized response = %d %s", oversized.Code, oversized.Body.String())
	}
}

func TestHandlerProfileConsentSessionsAndTrustedContext(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	handler, err := NewHandler(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := fixture.exchange(t, "provider-http-profile", "device-http-profile-1")
	second := fixture.exchange(t, "provider-http-profile", "device-http-profile-2")
	headers := trustedHeaders(first)
	headers.Set("X-Planext4u-Roles", "ADMIN")
	headers.Set(identityCorrelationHeader, "corr-identity-http-001")

	missing := identityRequest(handler, http.MethodGet, "/v1/me", "", nil)
	if missing.Code != http.StatusUnauthorized || !strings.Contains(missing.Body.String(), "SESSION_INVALID") {
		t.Fatalf("missing context response = %d %s", missing.Code, missing.Body.String())
	}
	tamperedHeaders := headers.Clone()
	tamperedHeaders.Set("X-Planext4u-Tenant", "tenant_other")
	tampered := identityRequest(handler, http.MethodGet, "/v1/me", "", tamperedHeaders)
	if tampered.Code != http.StatusForbidden {
		t.Fatalf("tampered context response = %d %s", tampered.Code, tampered.Body.String())
	}

	me := identityRequest(handler, http.MethodGet, "/v1/me", "", headers)
	if me.Code != http.StatusOK || me.Header().Get("ETag") != `"1"` ||
		me.Header().Get(identityCorrelationHeader) != "corr-identity-http-001" ||
		strings.Contains(me.Body.String(), "ADMIN") || strings.Contains(me.Body.String(), "provider-http-profile") {
		t.Fatalf("me response = %d %s headers=%v", me.Code, me.Body.String(), me.Header())
	}

	updateHeaders := headers.Clone()
	updateHeaders.Set("If-Match", `"1"`)
	updated := identityRequest(handler, http.MethodPatch, "/v1/me", `{"display_name":"தமிழ் வாடிக்கையாளர்","locale":"ta","time_zone":"Asia/Kolkata"}`, updateHeaders)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("profile update = %d %s headers=%v", updated.Code, updated.Body.String(), updated.Header())
	}
	stale := identityRequest(handler, http.MethodPatch, "/v1/me", `{"display_name":"Stale","locale":"en","time_zone":"Asia/Kolkata"}`, updateHeaders)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "PROFILE_VERSION_CONFLICT") {
		t.Fatalf("stale update = %d %s", stale.Code, stale.Body.String())
	}

	consent := identityRequest(handler, http.MethodPut, "/v1/me/consents/ANALYTICS", `{"granted":true,"policy_version":"privacy-2026-04"}`, headers)
	if consent.Code != http.StatusOK || !strings.Contains(consent.Body.String(), `"granted":true`) {
		t.Fatalf("consent response = %d %s", consent.Code, consent.Body.String())
	}
	consents := identityRequest(handler, http.MethodGet, "/v1/me/consents", "", headers)
	if consents.Code != http.StatusOK || !strings.Contains(consents.Body.String(), "ANALYTICS") {
		t.Fatalf("consents response = %d %s", consents.Code, consents.Body.String())
	}
	sessions := identityRequest(handler, http.MethodGet, "/v1/me/sessions", "", headers)
	if sessions.Code != http.StatusOK || strings.Count(sessions.Body.String(), `"device_reference"`) != 2 || strings.Contains(sessions.Body.String(), "device-http-profile") {
		t.Fatalf("sessions response = %d %s", sessions.Code, sessions.Body.String())
	}
	revoked := identityRequest(handler, http.MethodDelete, "/v1/me/sessions/"+second.Session.ID, "", headers)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("session revoke = %d %s", revoked.Code, revoked.Body.String())
	}
}

func TestHandlerReadinessAndRevokeDoNotCreateTokenOracle(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	handler, err := NewHandler(fixture.service, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	readiness := identityRequest(handler, http.MethodGet, "/readyz", "", nil)
	if readiness.Code != http.StatusServiceUnavailable || strings.Contains(readiness.Body.String(), "database") {
		t.Fatalf("readiness response = %d %s", readiness.Code, readiness.Body.String())
	}
	health := identityRequest(handler, http.MethodGet, "/healthz", "", nil)
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Fatalf("health response = %d %s", health.Code, health.Body.String())
	}
	revoke := identityRequest(handler, http.MethodPost, "/v1/auth/revoke", `{"refresh_token":"unknown"}`, nil)
	if revoke.Code != http.StatusNoContent || revoke.Body.Len() != 0 {
		t.Fatalf("unknown revoke response = %d %s", revoke.Code, revoke.Body.String())
	}
	wrongMethod := identityRequest(handler, http.MethodGet, "/v1/auth/refresh", "", nil)
	if wrongMethod.Code != http.StatusMethodNotAllowed || !strings.Contains(wrongMethod.Body.String(), "METHOD_NOT_ALLOWED") {
		t.Fatalf("wrong method response = %d %s", wrongMethod.Code, wrongMethod.Body.String())
	}
	missing := identityRequest(handler, http.MethodGet, "/v1/unknown", "", nil)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "RESOURCE_NOT_FOUND") {
		t.Fatalf("missing route response = %d %s", missing.Code, missing.Body.String())
	}
}

func identityRequest(handler http.Handler, method, path, body string, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func trustedHeaders(authentication Authentication) http.Header {
	headers := make(http.Header)
	headers.Set("X-Planext4u-Subject", authentication.IdentityID)
	headers.Set("X-Planext4u-Session", authentication.Session.ID)
	headers.Set("X-Planext4u-Tenant", authentication.TenantID)
	headers.Set("X-Planext4u-Country", authentication.Country)
	return headers
}
