package customerweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/identity"
)

var testNow = time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

type fakeIdentityClient struct {
	guest      PlatformSession
	auth       PlatformSession
	refreshed  identity.TokenPair
	refreshErr error
	revoked    []string
	guestCalls int
	exchanges  int
	refreshes  int
}

func (client *fakeIdentityClient) CreateGuest(context.Context, string) (PlatformSession, error) {
	client.guestCalls++
	return client.guest, nil
}

func (client *fakeIdentityClient) Exchange(context.Context, identity.ExchangeInput) (PlatformSession, error) {
	client.exchanges++
	return client.auth, nil
}

func (client *fakeIdentityClient) Refresh(context.Context, string) (identity.TokenPair, error) {
	client.refreshes++
	return client.refreshed, client.refreshErr
}

func (client *fakeIdentityClient) Revoke(_ context.Context, token string) error {
	client.revoked = append(client.revoked, token)
	return nil
}

type platformCapture struct {
	method        string
	path          string
	authorization string
	spoofed       string
}

func (capture *platformCapture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	capture.method = request.Method
	capture.path = request.URL.Path
	capture.authorization = request.Header.Get("Authorization")
	capture.spoofed = request.Header.Get("X-Planext4u-Principal")
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

func TestGuestSessionCookieAndProxySecurity(t *testing.T) {
	handler, identityClient, platform := newHandlerFixture(t)
	created := perform(handler, http.MethodPost, "/web/v1/customer/session", `{"mode":"GUEST","country":"IN"}`, nil, "https://customer.example")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	cookie := responseCookie(t, created)
	if cookie.Name != SessionCookieName || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge <= 0 {
		t.Fatalf("cookie attributes = %#v", cookie)
	}
	var view SessionView
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Guest || view.IdentityID != "" || view.CSRFToken == "" || strings.Contains(created.Body.String(), "access_token") || strings.Contains(created.Body.String(), "refresh_token") {
		t.Fatalf("unsafe guest view = %s", created.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/platform-api/v1/home?country=IN", nil)
	request.AddCookie(cookie)
	request.Header.Set("Authorization", "Bearer browser-controlled")
	request.Header.Set("X-Planext4u-Principal", "spoofed")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || platform.path != "/v1/home" || platform.authorization != "Bearer guest-access-token" || platform.spoofed != "" {
		t.Fatalf("proxy = %d path=%q auth=%q spoofed=%q", response.Code, platform.path, platform.authorization, platform.spoofed)
	}

	mutation := perform(handler, http.MethodPost, "/platform-api/v1/orders", `{}`, cookie, "https://customer.example")
	if mutation.Code != http.StatusForbidden || !strings.Contains(mutation.Body.String(), "CUSTOMER_GUEST_MUTATION_FORBIDDEN") {
		t.Fatalf("guest mutation = %d %s", mutation.Code, mutation.Body.String())
	}
	if identityClient.guestCalls != 1 {
		t.Fatalf("guest calls = %d", identityClient.guestCalls)
	}
}

func TestOriginAndCSRFAreRequired(t *testing.T) {
	handler, _, _ := newHandlerFixture(t)
	denied := perform(handler, http.MethodPost, "/web/v1/customer/session", `{"mode":"GUEST","country":"IN"}`, nil, "https://attacker.example")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-origin create = %d %s", denied.Code, denied.Body.String())
	}
	created := perform(handler, http.MethodPost, "/web/v1/customer/session", `{"mode":"GUEST","country":"IN"}`, nil, "https://customer.example")
	cookie := responseCookie(t, created)

	missing := perform(handler, http.MethodDelete, "/web/v1/customer/session", "", cookie, "https://customer.example")
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d %s", missing.Code, missing.Body.String())
	}
	crossSite := httptest.NewRequest(http.MethodDelete, "/web/v1/customer/session", nil)
	crossSite.AddCookie(cookie)
	crossSite.Header.Set("Origin", "https://customer.example")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.Header.Set("X-CSRF-Token", decodeView(t, created).CSRFToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, crossSite)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site delete = %d %s", response.Code, response.Body.String())
	}
}

func TestGuestUpgradeAndRefreshRotateOpaqueSession(t *testing.T) {
	handler, identityClient, _ := newHandlerFixture(t)
	guestResponse := perform(handler, http.MethodPost, "/web/v1/customer/session", `{"mode":"GUEST","country":"IN"}`, nil, "https://customer.example")
	guestCookie := responseCookie(t, guestResponse)

	upgradeBody := `{"mode":"AUTHENTICATED","country":"IN","provider":"supabase","provider_token":"provider-token","device_id":"web-browser"}`
	upgraded := perform(handler, http.MethodPost, "/web/v1/customer/session", upgradeBody, guestCookie, "https://customer.example")
	if upgraded.Code != http.StatusCreated {
		t.Fatalf("upgrade = %d %s", upgraded.Code, upgraded.Body.String())
	}
	authCookie := responseCookie(t, upgraded)
	authView := decodeView(t, upgraded)
	if authView.Guest || authView.IdentityID != "identity-1" || authView.CSRFToken == "" || authCookie.Value == guestCookie.Value {
		t.Fatalf("upgrade view=%#v cookie rotated=%v", authView, authCookie.Value != guestCookie.Value)
	}
	oldGuest := perform(handler, http.MethodGet, "/web/v1/customer/session", "", guestCookie, "")
	if oldGuest.Code != http.StatusUnauthorized {
		t.Fatalf("old guest cookie = %d", oldGuest.Code)
	}

	refreshed := performWithCSRF(handler, http.MethodPost, "/web/v1/customer/session/refresh", "", authCookie, authView.CSRFToken)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh = %d %s", refreshed.Code, refreshed.Body.String())
	}
	refreshCookie := responseCookie(t, refreshed)
	refreshView := decodeView(t, refreshed)
	if refreshCookie.Value == authCookie.Value || refreshView.CSRFToken != authView.CSRFToken || identityClient.refreshes != 1 {
		t.Fatalf("rotation cookie=%v csrf-stable=%v refreshes=%d", refreshCookie.Value != authCookie.Value, refreshView.CSRFToken == authView.CSRFToken, identityClient.refreshes)
	}
	oldAuth := perform(handler, http.MethodGet, "/web/v1/customer/session", "", authCookie, "")
	if oldAuth.Code != http.StatusUnauthorized {
		t.Fatalf("old auth cookie = %d", oldAuth.Code)
	}

	loggedOut := performWithCSRF(handler, http.MethodDelete, "/web/v1/customer/session", "", refreshCookie, refreshView.CSRFToken)
	if loggedOut.Code != http.StatusNoContent || len(identityClient.revoked) != 1 || identityClient.revoked[0] != "refresh-token-2" {
		t.Fatalf("logout=%d revoked=%v", loggedOut.Code, identityClient.revoked)
	}
	cleared := responseCookie(t, loggedOut)
	if cleared.MaxAge != -1 {
		t.Fatalf("clear cookie = %#v", cleared)
	}
}

func TestRefreshFailurePreservesOnlyRecoverableSessions(t *testing.T) {
	for _, test := range []struct {
		name          string
		upstream      error
		wantStatus    int
		wantAvailable bool
		wantCleared   bool
	}{
		{name: "temporary upstream failure", upstream: &IdentityUpstreamError{Status: http.StatusServiceUnavailable}, wantStatus: http.StatusServiceUnavailable, wantAvailable: true},
		{name: "invalid refresh credential", upstream: &IdentityUpstreamError{Status: http.StatusUnauthorized}, wantStatus: http.StatusUnauthorized, wantCleared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, identityClient, _ := newHandlerFixture(t)
			created := perform(handler, http.MethodPost, "/web/v1/customer/session", `{"mode":"AUTHENTICATED","country":"IN","provider":"oidc","provider_token":"provider-token","device_id":"web-browser"}`, nil, "https://customer.example")
			if created.Code != http.StatusCreated {
				t.Fatalf("create = %d %s", created.Code, created.Body.String())
			}
			cookie := responseCookie(t, created)
			identityClient.refreshErr = test.upstream

			refreshed := performWithCSRF(handler, http.MethodPost, "/web/v1/customer/session/refresh", "", cookie, decodeView(t, created).CSRFToken)
			if refreshed.Code != test.wantStatus {
				t.Fatalf("refresh = %d %s", refreshed.Code, refreshed.Body.String())
			}
			available := perform(handler, http.MethodGet, "/web/v1/customer/session", "", cookie, "").Code == http.StatusOK
			if available != test.wantAvailable {
				t.Fatalf("session available = %v, want %v", available, test.wantAvailable)
			}
			cookies := refreshed.Result().Cookies()
			cleared := len(cookies) == 1 && cookies[0].MaxAge == -1
			if cleared != test.wantCleared {
				t.Fatalf("cookie cleared = %v, want %v", cleared, test.wantCleared)
			}
		})
	}
}

func newHandlerFixture(t *testing.T) (*Handler, *fakeIdentityClient, *platformCapture) {
	t.Helper()
	store, err := NewMemorySessionStore(func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	identityClient := &fakeIdentityClient{
		guest:     PlatformSession{PlatformSession: "guest-session", TenantID: "tenant-in", Country: "IN", DisplayName: "Guest", Roles: []identity.Role{identity.RoleGuest}, Guest: true, AccessToken: "guest-access-token", AccessExpiresAt: testNow.Add(time.Hour)},
		auth:      PlatformSession{PlatformSession: "identity-session", IdentityID: "identity-1", TenantID: "tenant-in", Country: "IN", DisplayName: "Customer One", Roles: []identity.Role{identity.RoleCustomer}, AccessToken: "access-token-1", AccessExpiresAt: testNow.Add(15 * time.Minute), RefreshToken: "refresh-token-1", RefreshExpiresAt: testNow.Add(24 * time.Hour)},
		refreshed: identity.TokenPair{AccessToken: "access-token-2", AccessExpiresAt: testNow.Add(30 * time.Minute), RefreshToken: "refresh-token-2", RefreshExpiresAt: testNow.Add(48 * time.Hour), TokenType: "Bearer"},
	}
	platform := &platformCapture{}
	handler, err := NewHandler(Config{Sessions: store, Identity: identityClient, Platform: platform, Clock: func() time.Time { return testNow }, AllowedOrigins: []string{"https://customer.example"}, SessionTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return handler, identityClient, platform
}

func perform(handler http.Handler, method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func performWithCSRF(handler http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://customer.example")
	request.Header.Set("X-CSRF-Token", csrf)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	return cookies[0]
}

func decodeView(t *testing.T, response *httptest.ResponseRecorder) SessionView {
	t.Helper()
	var view SessionView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}
