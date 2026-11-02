package identity

import (
	"net/http"
	"strings"
	"testing"
)

func TestOccupationHandlerRequiresCurrentPrincipalAndPersistentProvider(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	handler, err := NewHandler(fixture.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := fixture.exchange(t, "occupation-private-provider", "occupation-private-device")
	headers := trustedHeaders(auth)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		body := ""
		if method == http.MethodPut {
			body = `{"occupation_id":"","expected_revision":0}`
		}
		anonymous := identityRequest(handler, method, "/v1/me/occupation", body, nil)
		if anonymous.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d %s", method, anonymous.Code, anonymous.Body.String())
		}
		unavailable := identityRequest(handler, method, "/v1/me/occupation", body, headers)
		if unavailable.Code != http.StatusServiceUnavailable || unavailable.Header().Get("Cache-Control") != "no-store" || strings.Contains(unavailable.Body.String(), "occupation-private") {
			t.Fatalf("unconfigured persistent provider %s: %d %s", method, unavailable.Code, unavailable.Body.String())
		}
	}
	for _, input := range []struct {
		name, path, body string
	}{
		{"caller scope", "/v1/me/occupation?country=US", `{"occupation_id":"","expected_revision":0}`},
		{"caller authority", "/v1/me/occupation", `{"occupation_id":"","expected_revision":0,"identity_id":"another-person"}`},
		{"malformed json", "/v1/me/occupation", `{"occupation_id":`},
		{"invalid id", "/v1/me/occupation", `{"occupation_id":"invalid","expected_revision":0}`},
		{"negative revision", "/v1/me/occupation", `{"occupation_id":"","expected_revision":-1}`},
		{"unsafe revision", "/v1/me/occupation", `{"occupation_id":"","expected_revision":4611686018427387904}`},
	} {
		t.Run(input.name, func(t *testing.T) {
			response := identityRequest(handler, http.MethodPut, input.path, input.body, headers)
			if response.Code != http.StatusUnprocessableEntity || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("invalid own occupation command: %d %s", response.Code, response.Body.String())
			}
		})
	}
	wrongMethod := identityRequest(handler, http.MethodPost, "/v1/me/occupation", `{}`, headers)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method: %d", wrongMethod.Code)
	}
	revoked := identityRequest(handler, http.MethodPost, "/v1/auth/revoke", `{"refresh_token":"`+auth.Tokens.RefreshToken+`"}`, nil)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke source session: %d %s", revoked.Code, revoked.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := identityRequest(handler, method, "/v1/me/occupation", `{"occupation_id":"","expected_revision":0}`, headers)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("revoked source %s: %d %s", method, response.Code, response.Body.String())
		}
	}
}
