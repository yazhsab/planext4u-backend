package adminshell

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFulfillmentBFFDerivesScopeAndPreservesOwnerDenials(t *testing.T) {
	var status, requests atomic.Int64
	status.Store(200)
	headers := make(chan http.Header, 1)
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			headers <- r.Header.Clone()
		}
		current := int(status.Load())
		w.WriteHeader(current)
		if current != 200 {
			return
		}
		if r.URL.Path == "/v1/operations/territories" {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"region_id": "NORTH", "territories": 1, "online_riders": 0, "unassigned_tasks": 2, "at_risk_tasks": 0, "completed_today": 1, "generated_at": testNow, "latest_locations": []any{}, "recent_field_check_ins": []any{}})
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, err := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	handler, store := testHandler(t, true, reader)
	principal := testPrincipal(RoleRegionalStaff)
	principal.OperatorRevision = 7
	token := issue(t, store, principal)
	// Use the existing cookie helper's exact name; caller-supplied scope headers
	// are deliberately ignored even when they claim a platform administrator.
	request := requestWithCookie(token)
	request.URL.Path = "/admin/api/v1/fulfillment/territories"
	request.Header.Set("X-Planext4u-Tenant", "foreign-tenant")
	request.Header.Set("X-Planext4u-Roles", "SUPER_ADMIN")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	captured := <-headers
	if response.Code != 200 || captured.Get("X-Planext4u-Tenant") != "tenant-1" || captured.Get("X-Planext4u-Country") != "IN" || captured.Get("X-Planext4u-Subject") != "admin-1" || captured.Get("X-Planext4u-Roles") != "REGIONAL_ADMIN" || captured.Get("X-Planext4u-MFA") != "verified" || captured.Get("X-Planext4u-Operator-Session") != principal.SessionID || captured.Get("X-Planext4u-Operator-Revision") != "7" || captured.Get("Cookie") != "" {
		t.Fatalf("server scope was not preserved: %d %#v", response.Code, captured)
	}
	response = serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/territories?tenant_id=foreign", nil, token, "", "")
	assertStatusAndCode(t, response, 422, "ADMIN_FULFILLMENT_REQUEST_INVALID")
	if requests.Load() != 1 {
		t.Fatal("client authority query reached the owner")
	}
	response = serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/regions/NORTH", nil, token, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"unassigned_tasks":2`) {
		t.Fatalf("owner dashboard=%d %s", response.Code, response.Body.String())
	}
	status.Store(403)
	assertStatusAndCode(t, serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/regions/NORTH", nil, token, "", ""), 403, "ADMIN_FULFILLMENT_FORBIDDEN")
	status.Store(503)
	assertStatusAndCode(t, serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/territories", nil, token, "", ""), 503, "ADMIN_FULFILLMENT_UNAVAILABLE")
	other := issue(t, store, testPrincipal(RoleContentAdmin))
	assertStatusAndCode(t, serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/territories", nil, other, "", ""), 403, "ADMIN_ROLE_FORBIDDEN")
}

func TestFulfillmentBFFRefusesRedirectsAndUnconfiguredOwners(t *testing.T) {
	var forwarded atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	endpoint, _ := url.Parse(redirect.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	token := issue(t, store, testPrincipal(RoleCountryAdmin))
	assertStatusAndCode(t, serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/territories", nil, token, "", ""), 503, "ADMIN_FULFILLMENT_UNAVAILABLE")
	if forwarded.Load() {
		t.Fatal("operator scope headers escaped the configured service origin")
	}
	unconfigured, unconfiguredStore := testHandler(t, true)
	unconfiguredToken := issue(t, unconfiguredStore, testPrincipal(RoleRegionalStaff))
	assertStatusAndCode(t, serve(unconfigured, http.MethodGet, "/admin/api/v1/fulfillment/territories", nil, unconfiguredToken, "", ""), 503, "ADMIN_FULFILLMENT_UNAVAILABLE")
}

func TestRegionalDashboardRejectsOwnerCountryPayoutLeak(t *testing.T) {
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"region_id": "NORTH", "territories": 1, "online_riders": 0,
			"unassigned_tasks": 0, "at_risk_tasks": 0, "completed_today": 0,
			"generated_at": testNow, "latest_locations": []any{},
			"recent_field_check_ins": []any{},
			"payouts_pending":        map[string]any{"currency": "INR", "amount_minor": 97531},
		})
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, err := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	handler, store := testHandler(t, true, reader)
	regional := issue(t, store, testPrincipal(RoleRegionalStaff))
	response := serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/regions/NORTH", nil, regional, "", "")
	assertStatusAndCode(t, response, 503, "ADMIN_FULFILLMENT_UNAVAILABLE")
	if strings.Contains(response.Body.String(), "97531") {
		t.Fatal("country payout total leaked to regional browser")
	}
	country := issue(t, store, testPrincipal(RoleCountryAdmin))
	response = serve(handler, http.MethodGet, "/admin/api/v1/fulfillment/regions/NORTH", nil, country, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "97531") {
		t.Fatalf("country dashboard did not receive permitted payout total: %d %s", response.Code, response.Body.String())
	}
}
