package adminshell

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignedSessionExchangeIssuesOpaqueCookieAndRejectsReplay(t *testing.T) {
	store, _ := NewMemorySessionStore(func() time.Time { return testNow })
	issuer := &onceSessionIssuer{delegate: store}
	secret := []byte(strings.Repeat("s", 32))
	handler, err := NewSessionExchangeHandler(SessionExchangeConfig{
		Sessions: issuer, Secret: secret, Clock: func() time.Time { return testNow }, SessionTTL: time.Hour,
		MaxSkew: time.Minute, RequireMFA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"subject_id":"36b9333a-a80b-4e4a-bbcd-03b86c948f93","session_id":"3a5b573b-9c49-4ab2-8796-7d05ec064f3a","tenant_id":"d72863da-da42-4a6b-8ae5-f6bbb320e414","display_name":"Operations Admin","roles":["SUPER_ADMIN"],"allowed_countries":["IN","NG"],"selected_country":"IN","authenticated_at":%q,"auth_methods":["webauthn"]}`, testNow.Add(-time.Minute).Format(time.RFC3339)))
	response := exchangeRequest(handler, body, secret, testNow)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies=%#v", cookies)
	}
	request := httptest.NewRequest(http.MethodGet, "https://admin.planext4u.test/admin/api/v1/session", nil)
	request.AddCookie(cookies[0])
	resolved, err := store.Resolve(request)
	if err != nil || resolved.Principal.DisplayName != "Operations Admin" || resolved.Principal.SelectedCountry != "IN" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	replayed := exchangeRequest(handler, body, secret, testNow)
	assertStatusAndCode(t, replayed, http.StatusConflict, "ADMIN_SESSION_EXCHANGE_REPLAYED")
}

func TestSignedSessionExchangeRejectsTamperingStaleAssertionsAndMissingMFA(t *testing.T) {
	store, _ := NewMemorySessionStore(func() time.Time { return testNow })
	secret := []byte(strings.Repeat("k", 32))
	handler, _ := NewSessionExchangeHandler(SessionExchangeConfig{
		Sessions: store, Secret: secret, Clock: func() time.Time { return testNow }, SessionTTL: time.Hour,
		MaxSkew: time.Minute, RequireMFA: true,
	})
	body := []byte(fmt.Sprintf(`{"subject_id":"36b9333a-a80b-4e4a-bbcd-03b86c948f93","session_id":"3a5b573b-9c49-4ab2-8796-7d05ec064f3a","tenant_id":"d72863da-da42-4a6b-8ae5-f6bbb320e414","display_name":"Operations Admin","roles":["SUPER_ADMIN"],"allowed_countries":["IN"],"selected_country":"IN","authenticated_at":%q,"auth_methods":["password"]}`, testNow.Format(time.RFC3339)))
	tampered := exchangeRequest(handler, body, []byte(strings.Repeat("x", 32)), testNow)
	assertStatusAndCode(t, tampered, http.StatusUnauthorized, "ADMIN_SESSION_EXCHANGE_INVALID")
	stale := exchangeRequest(handler, body, secret, testNow.Add(-2*time.Minute))
	assertStatusAndCode(t, stale, http.StatusUnauthorized, "ADMIN_SESSION_EXCHANGE_INVALID")
	missingMFA := exchangeRequest(handler, body, secret, testNow)
	assertStatusAndCode(t, missingMFA, http.StatusForbidden, "ADMIN_SESSION_ASSERTION_FORBIDDEN")
}

type onceSessionIssuer struct {
	delegate SessionIssuer
	issued   bool
}

func (issuer *onceSessionIssuer) Issue(principal Principal, ttl time.Duration) (string, error) {
	if issuer.issued {
		return "", ErrSessionExists
	}
	issuer.issued = true
	return issuer.delegate.Issue(principal, ttl)
}

func exchangeRequest(handler http.Handler, body, secret []byte, timestamp time.Time) *httptest.ResponseRecorder {
	rawTimestamp := fmt.Sprintf("%d", timestamp.Unix())
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("v1\n" + rawTimestamp + "\n"))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "https://admin.planext4u.test/internal/v1/admin-sessions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-P4U-Admin-Timestamp", rawTimestamp)
	request.Header.Set("X-P4U-Admin-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-Correlation-ID", "correlation-session-exchange")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
