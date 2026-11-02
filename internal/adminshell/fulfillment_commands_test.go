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

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
)

const fieldTestTerritory = "3313866b-8f82-41aa-81ec-8321b302e6fc"
const fieldTestPath = "/admin/api/v1/fulfillment/territories/" + fieldTestTerritory + "/field-check-ins"

func fieldPrincipal() Principal {
	p := testPrincipal(RoleRegionalStaff)
	p.SessionID = "93435139-1eae-4214-8b1d-3d7bde384f28"
	p.OperatorRevision = 1
	return p
}

func fieldCommandRequest(handler http.Handler, token, csrf, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, fieldTestPath, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	request.Header.Set("Origin", "https://admin.planext4u.net")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("X-Planext4u-Subject", "forged-officer")
	request.Header.Set("X-Planext4u-Roles", "SUPER_ADMIN")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestFieldCommandDerivesOfficerAndPreservesIdempotencyAndOwnerDenial(t *testing.T) {
	var status atomic.Int64
	status.Store(201)
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/operations/territories/"+fieldTestTerritory+"/field-check-in" || r.Header.Get("X-Planext4u-Subject") != "admin-1" || r.Header.Get("X-Planext4u-Roles") != "FIELD_OFFICER" || r.Header.Get("X-Planext4u-Tenant") != "tenant-1" || r.Header.Get("X-Planext4u-Country") != "IN" || r.Header.Get("X-Planext4u-MFA") != "verified" || r.Header.Get("X-Planext4u-Operator-Session") != fieldPrincipal().SessionID || r.Header.Get("X-Planext4u-Operator-Revision") != "1" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "field-key-001" {
			t.Errorf("field command scope/protocol = %s %s %#v", r.Method, r.URL.Path, r.Header)
		}
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(int(status.Load()))
		if status.Load() != 201 {
			return
		}
		_ = json.NewEncoder(w).Encode(fulfillment.FieldCheckIn{ID: "visit-001", OfficerID: "admin-1", TerritoryID: fieldTestTerritory, Point: fulfillment.Point{Latitude: 13.08, Longitude: 80.27}, DistanceM: 1, RecordedAt: testNow})
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	token := issue(t, store, fieldPrincipal())
	csrf := resolveCSRF(t, store, token)
	response := fieldCommandRequest(handler, token, csrf, `{"point":{"latitude":13.08,"longitude":80.27}}`, "field-key-001")
	if response.Code != 201 || response.Header().Get("Idempotency-Replayed") != "true" || !strings.Contains(response.Body.String(), `"officer_id":"admin-1"`) {
		t.Fatalf("field result = %d %s", response.Code, response.Body.String())
	}
	for upstream, expected := range map[int]string{403: "ADMIN_FULFILLMENT_FORBIDDEN", 404: "ADMIN_FULFILLMENT_NOT_FOUND", 409: "ADMIN_FULFILLMENT_CONFLICT", 422: "ADMIN_FULFILLMENT_REQUEST_INVALID", 503: "ADMIN_FULFILLMENT_UNAVAILABLE"} {
		status.Store(int64(upstream))
		assertStatusAndCode(t, fieldCommandRequest(handler, token, csrf, `{"point":{"latitude":13.08,"longitude":80.27}}`, "field-key-001"), upstream, expected)
	}
	if calls.Load() != 6 {
		t.Fatalf("owner calls=%d", calls.Load())
	}
}

func TestFieldCommandRejectsUntrustedMissingAndStaleRequestsBeforeOwner(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(201) }))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	token := issue(t, store, fieldPrincipal())
	csrf := resolveCSRF(t, store, token)
	for _, body := range []string{`{}`, `{"point":{}}`, `{"point":{"latitude":13}}`, `{"point":{"latitude":91,"longitude":80}}`, `{"point":{"latitude":13,"longitude":181}}`, `{"point":{"latitude":13,"longitude":80},"subject_id":"forged"}`} {
		assertStatusAndCode(t, fieldCommandRequest(handler, token, csrf, body, "field-key-001"), 422, "ADMIN_FULFILLMENT_REQUEST_INVALID")
	}
	assertStatusAndCode(t, fieldCommandRequest(handler, token, csrf, `{"point":{"latitude":13,"longitude":80}}`, "bad"), 422, "ADMIN_FULFILLMENT_REQUEST_INVALID")
	assertStatusAndCode(t, fieldCommandRequest(handler, token, "invalid", `{"point":{"latitude":13,"longitude":80}}`, "field-key-001"), 403, "ADMIN_CSRF_INVALID")
	stale := fieldPrincipal()
	stale.SessionID = "stale-session-1"
	stale.AuthenticatedAt = testNow.Add(-6 * time.Minute)
	staleToken := issue(t, store, stale)
	assertStatusAndCode(t, fieldCommandRequest(handler, staleToken, resolveCSRF(t, store, staleToken), `{"point":{"latitude":13,"longitude":80}}`, "field-key-001"), 403, "ADMIN_FRESH_MFA_REQUIRED")
	contentToken := issue(t, store, testPrincipal(RoleContentAdmin))
	assertStatusAndCode(t, fieldCommandRequest(handler, contentToken, resolveCSRF(t, store, contentToken), `{"point":{"latitude":13,"longitude":80}}`, "field-key-001"), 403, "ADMIN_ROLE_FORBIDDEN")
	if calls.Load() != 0 {
		t.Fatal("rejected command reached owner")
	}
}

func TestFieldCommandNeverAcceptsIncompleteOrMismatchedOwnerSuccess(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":"visit"}`, `{"id":"visit","officer_id":"foreign","territory_id":"3313866b-8f82-41aa-81ec-8321b302e6fc","point":{"latitude":13.08,"longitude":80.27},"distance_m":0,"recorded_at":"2026-10-25T09:00:00Z"}`, `{"id":"visit","officer_id":"admin-1","territory_id":"3313866b-8f82-41aa-81ec-8321b302e6fc","point":{},"distance_m":0,"recorded_at":"2026-10-25T09:00:00Z"}`} {
		owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); _, _ = w.Write([]byte(body)) }))
		endpoint, _ := url.Parse(owner.URL)
		reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
		handler, store := testHandler(t, true, reader)
		token := issue(t, store, fieldPrincipal())
		assertStatusAndCode(t, fieldCommandRequest(handler, token, resolveCSRF(t, store, token), `{"point":{"latitude":13.08,"longitude":80.27}}`, "field-key-001"), 503, "ADMIN_FULFILLMENT_UNAVAILABLE")
		owner.Close()
	}
}
