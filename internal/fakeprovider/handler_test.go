package fakeprovider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentityProviderAcceptsOnlySyntheticTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "customer",
			body:       `{"provider":"local","token":"synthetic-customer"}`,
			wantStatus: http.StatusOK,
			wantBody:   `"roles":["CUSTOMER"]`,
		},
		{
			name:       "invalid token",
			body:       `{"provider":"local","token":"real-token"}`,
			wantStatus: http.StatusUnauthorized,
			wantBody:   "PROVIDER_TOKEN_INVALID",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := request(t, http.MethodPost, "/v1/identity/verify", test.body, "")
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("response = %d %s, want %d containing %q", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
}

func TestNotificationProviderIsDeterministicAndSafe(t *testing.T) {
	t.Parallel()

	body := `{"channel":"sms","recipient":"synthetic-phone-001","template":"otp"}`
	first := request(t, http.MethodPost, "/v1/notifications/send", body, "synthetic-key-0001")
	second := request(t, http.MethodPost, "/v1/notifications/send", body, "synthetic-key-0001")
	if first.Code != http.StatusAccepted || first.Body.String() != second.Body.String() {
		t.Fatalf("deterministic response mismatch: first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}

	denied := request(
		t,
		http.MethodPost,
		"/v1/notifications/send",
		`{"channel":"email","recipient":"person@example.com","template":"welcome"}`,
		"synthetic-key-0002",
	)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "NON_SYNTHETIC_RECIPIENT_DENIED") {
		t.Fatalf("unsafe recipient response = %d %s", denied.Code, denied.Body.String())
	}
}

func TestGeocodeProviderUsesFixedSyntheticResults(t *testing.T) {
	t.Parallel()

	response := request(t, http.MethodGet, "/v1/maps/geocode?q=chennai", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "13.0827") {
		t.Fatalf("geocode response = %d %s", response.Code, response.Body.String())
	}
}

func TestMalwareScannerRequiresLocalCredentialAndRejectsSyntheticInfection(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/v1/media/scan", strings.NewReader(`{"object_key":"tenants/t/owners/o/media/infected"}`))
	request.Header.Set("Authorization", "Bearer local-media-scanner-token")
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "MALWARE_DETECTED") {
		t.Fatalf("scanner response=%d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/media/scan", strings.NewReader(`{"object_key":"tenants/t/owners/o/media/clean"}`))
	response = httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated scanner response=%d", response.Code)
	}
}

func request(t *testing.T, method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, req)
	return response
}
