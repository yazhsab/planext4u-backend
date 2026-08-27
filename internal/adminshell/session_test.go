package adminshell

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionExpiresAndIsStoredByDigest(t *testing.T) {
	now := testNow
	store, err := NewMemorySessionStore(func() time.Time { return now })
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	token, err := store.Issue(testPrincipal(RoleSuperAdmin), time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, plaintextStored := store.sessions[token]; plaintextStored {
		t.Fatal("session token must never be stored in plaintext")
	}
	now = now.Add(time.Minute)
	if _, err := store.Resolve(requestWithCookie(token)); err != ErrAuthenticationRequired {
		t.Fatalf("expected expired session, got %v", err)
	}
}

func TestSessionCookieUsesHostOnlySecurityAttributes(t *testing.T) {
	response := httptest.NewRecorder()
	if err := SetSessionCookie(response, strings.Repeat("a", 32), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set cookie: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("insecure session cookie: %#v", cookie)
	}
}

func TestUnknownRoleCannotCreateSession(t *testing.T) {
	store, err := NewMemorySessionStore(func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	principal := testPrincipal(Role("CUSTOMER"))
	if _, err := store.Issue(principal, time.Hour); err != ErrInvalidRequest {
		t.Fatalf("expected role rejection, got %v", err)
	}
}
