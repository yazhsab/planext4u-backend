package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGuestSessionHandlerRequiresSignedCountryScopedRequest(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	secret := []byte(strings.Repeat("g", 32))
	handler, err := NewHandlerWithGuestSessions(fixture.service, nil, GuestSessionHTTPConfig{
		Secret: secret, MaxSkew: time.Minute, RateLimit: 10, RateWindow: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	created := signedGuestRequest(handler, `{"country":"IN"}`, secret, fixture.clock.Now(), "192.0.2.10:1000")
	if created.Code != http.StatusCreated {
		t.Fatalf("created = %d %s", created.Code, created.Body.String())
	}
	var session GuestSession
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Country != "IN" || session.AccessToken == "" || strings.Contains(created.Body.String(), "refresh") {
		t.Fatalf("guest response = %#v body=%s", session, created.Body.String())
	}

	unsupported := signedGuestRequest(handler, `{"country":"US"}`, secret, fixture.clock.Now(), "192.0.2.11:1000")
	if unsupported.Code != http.StatusUnprocessableEntity || !strings.Contains(unsupported.Body.String(), "VALIDATION_FAILED") {
		t.Fatalf("unsupported country = %d %s", unsupported.Code, unsupported.Body.String())
	}

	tampered := signedGuestRequest(handler, `{"country":"IN"}`, []byte(strings.Repeat("x", 32)), fixture.clock.Now(), "192.0.2.12:1000")
	if tampered.Code != http.StatusUnauthorized || !strings.Contains(tampered.Body.String(), "GUEST_SESSION_ASSERTION_INVALID") {
		t.Fatalf("tampered = %d %s", tampered.Code, tampered.Body.String())
	}

	stale := signedGuestRequest(handler, `{"country":"IN"}`, secret, fixture.clock.Now().Add(-2*time.Minute), "192.0.2.13:1000")
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("stale = %d %s", stale.Code, stale.Body.String())
	}
}

func TestGuestSessionHandlerRateLimitsByInternalCaller(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	secret := []byte(strings.Repeat("r", 32))
	handler, err := NewHandlerWithGuestSessions(fixture.service, nil, GuestSessionHTTPConfig{
		Secret: secret, MaxSkew: time.Minute, RateLimit: 1, RateWindow: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	first := signedGuestRequest(handler, `{"country":"IN"}`, secret, fixture.clock.Now(), "192.0.2.20:1000")
	second := signedGuestRequest(handler, `{"country":"IN"}`, secret, fixture.clock.Now(), "192.0.2.20:2000")
	if first.Code != http.StatusCreated || second.Code != http.StatusTooManyRequests ||
		second.Header().Get("Retry-After") == "" || !strings.Contains(second.Body.String(), "GUEST_SESSION_RATE_LIMITED") {
		t.Fatalf("first=%d second=%d %s headers=%v", first.Code, second.Code, second.Body.String(), second.Header())
	}
}

func signedGuestRequest(handler http.Handler, body string, secret []byte, issuedAt time.Time, remoteAddr string) *httptest.ResponseRecorder {
	rawTimestamp := fmt.Sprintf("%d", issuedAt.Unix())
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("v1\n" + rawTimestamp + "\n" + body))
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/customer-guest-sessions", strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-P4U-Guest-Timestamp", rawTimestamp)
	request.Header.Set("X-P4U-Guest-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
