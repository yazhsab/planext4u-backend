package adminshell

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
)

func TestRegionalExportUsesCurrentServerScopeAndProjectsSafeCSV(t *testing.T) {
	var captured http.Header
	var status = 200
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.WriteHeader(status)
		if status != 200 {
			return
		}
		_ = json.NewEncoder(w).Encode(fulfillment.RegionalExport{Country: "IN", RegionID: "NORTH", Territories: 1, OnlineRiders: 2, UnassignedTasks: 3, CompletedToday: 4, GeneratedAt: testNow})
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	principal := testPrincipal(RoleRegionalStaff)
	principal.SessionID = "fa29dc1d-88ee-45f5-81ea-f11cb5dbaad7"
	principal.OperatorRevision = 7
	token := issue(t, store, principal)
	request := requestWithCookie(token)
	request.URL.Path = "/admin/api/v1/fulfillment/regions/NORTH/summary-export"
	request.Header.Set("X-Planext4u-Tenant", "foreign")
	request.Header.Set("X-Planext4u-Operator-Revision", "999")
	request.Header.Set("X-Planext4u-Roles", "SUPER_ADMIN")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	if err != nil || response.Code != 200 || len(rows) != 2 || len(rows[0]) != 8 || rows[1][0] != "IN" || rows[1][3] != "1" || rows[1][4] != "2" || captured.Get("X-Planext4u-Tenant") != "tenant-1" || captured.Get("X-Planext4u-Operator-Session") != principal.SessionID || captured.Get("X-Planext4u-Operator-Revision") != "7" || captured.Get("X-Planext4u-Roles") != "REGIONAL_ADMIN" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("invalid export scope/projection: %d %s %v", response.Code, response.Body.String(), err)
	}
	status = 403
	assertStatusAndCode(t, serve(handler, http.MethodGet, request.URL.Path, nil, token, "", ""), 403, "ADMIN_FULFILLMENT_FORBIDDEN")
	other := issue(t, store, testPrincipal(RoleContentAdmin))
	assertStatusAndCode(t, serve(handler, http.MethodGet, request.URL.Path, nil, other, "", ""), 403, "ADMIN_ROLE_FORBIDDEN")
	assertStatusAndCode(t, serve(handler, http.MethodGet, request.URL.Path+"?country=NG", nil, token, "", ""), 422, "ADMIN_FULFILLMENT_REQUEST_INVALID")
}

func TestRegionalExportRefusesIncompleteOrMismatchedOwnerResults(t *testing.T) {
	for _, document := range []string{`{"country":"IN","region_id":"NORTH","territories":1}`, `{"country":"NG","region_id":"NORTH","territories":1,"online_riders":0,"unassigned_tasks":0,"at_risk_tasks":0,"completed_today":0,"generated_at":"2026-10-25T12:00:00Z"}`} {
		owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(document)) }))
		endpoint, _ := url.Parse(owner.URL)
		reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
		if _, err := reader.RegionalSummaryExport(t.Context(), fulfillment.Actor{Country: "IN", OperatorSessionID: "fa29dc1d-88ee-45f5-81ea-f11cb5dbaad7", OperatorRevision: 1, MFAVerified: true}, "NORTH"); err == nil {
			t.Fatal("incomplete/foreign owner export accepted")
		}
		owner.Close()
	}
}
