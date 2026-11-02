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

func TestCountryReviewDetailAndApprovalUseOwnerEvidenceAndStripPrivateFields(t *testing.T) {
	var calls atomic.Int64
	var ownerRole string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ownerRole = r.Header.Get("X-Planext4u-Roles")
		if r.URL.Path == "/v1/vendor/applications/"+participantTestID+"/review" && r.Method == http.MethodGet {
			if ownerRole == "REGIONAL_ADMIN" {
				_, _ = w.Write([]byte(`{"id":"30000000-0000-4000-8000-000000000001","vendor_id":"` + participantTestID + `","revision":2,"status":"BANK_REVIEW","business_name":"Synthetic vendor","business_type":"SOLE_PROPRIETOR","contact_name":"","documents":[],"field_visit":{"scheduled_at":"2026-08-27T09:00:00Z","checked_in_at":"2026-08-27T10:00:00Z"},"service_zone_count":1,"bank_status":"","review_actions":[],"updated_at":"2026-08-27T10:00:00Z"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"30000000-0000-4000-8000-000000000001","vendor_id":"` + participantTestID + `","revision":2,"status":"BANK_REVIEW","business_name":"Synthetic vendor","business_type":"SOLE_PROPRIETOR","contact_name":"Synthetic contact","documents":[{"kind":"IDENTITY","ocr_status":"VERIFIED","asset_id":"private"}],"field_visit":{"scheduled_at":"2026-08-27T09:00:00Z","checked_in_at":"2026-08-27T10:00:00Z"},"service_zone_count":1,"bank_status":"VERIFIED","bank_last4":"1234","review_actions":["APPROVE"],"updated_at":"2026-08-27T10:00:00Z","bank_reference":"private"}`))
			return
		}
		if r.URL.Path == "/v1/vendor/applications/"+participantTestID+"/transitions" && r.Method == http.MethodPost {
			var input struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Status != "APPROVED" || input.Reason != "Verified owner evidence and bank" || r.Header.Get("If-Match") != "2" || r.Header.Get("Idempotency-Key") != "country-review-0001" {
				t.Error("review command lost owner semantics")
			}
			w.Header().Set("Idempotency-Replayed", "true")
			_, _ = w.Write([]byte(`{"vendor_id":"` + participantTestID + `","revision":3,"status":"APPROVED","bank_account":{"reference":"private"}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	principal := testPrincipal(RoleCountryAdmin)
	principal.SubjectID = "10000000-0000-4000-8000-000000000001"
	principal.SessionID = "40000000-0000-4000-8000-000000000001"
	principal.OperatorRevision = 7
	token := issue(t, store, principal)
	path := "/admin/api/v1/participants/VENDOR/" + participantTestID
	result := serve(handler, "GET", path+"/review", nil, token, "", "")
	if result.Code != 200 || strings.Contains(result.Body.String(), "private") || !strings.Contains(result.Body.String(), "APPROVE") || ownerRole != "OPS_ADMIN" {
		t.Fatal(result.Code, result.Body.String(), ownerRole)
	}
	var detail ParticipantReviewDetail
	if json.Unmarshal(result.Body.Bytes(), &detail) != nil || detail.ParticipantID != participantTestID || detail.BankLast4 != "1234" || detail.ServiceZoneCount != 1 {
		t.Fatal("invalid safe detail", result.Body.String())
	}
	var session SessionView
	if response := serve(handler, "GET", "/admin/api/v1/session", nil, token, "", ""); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil {
		t.Fatal("session unavailable")
	}
	command := func(body, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path+"/review-action", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		r.Header.Set("Origin", "https://admin.planext4u.net")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", "country-review-0001")
		r.Header.Set("If-Match", `"2"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	before := calls.Load()
	for _, invalid := range []string{`{"action":"VERIFY_BANK","reason":"Verified owner evidence and bank"}`, `{"action":"APPROVE","reason":"short"}`, `{"action":"APPROVE","reason":"Verified owner evidence and bank","asset_id":"private"}`} {
		if result = command(invalid, session.CSRFToken); result.Code != 422 && result.Code != 403 {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	if result = command(`{"action":"APPROVE","reason":"Verified owner evidence and bank"}`, "wrong"); result.Code != 403 {
		t.Fatal(result.Code)
	}
	if calls.Load() != before {
		t.Fatal("invalid request reached owner")
	}
	result = command(`{"action":"APPROVE","reason":"Verified owner evidence and bank"}`, session.CSRFToken)
	if result.Code != 200 || result.Header().Get("Idempotency-Replayed") != "true" || strings.Contains(result.Body.String(), "private") || !strings.Contains(result.Body.String(), `"status":"APPROVED"`) || ownerRole != "OPS_ADMIN" {
		t.Fatal(result.Code, result.Body.String(), ownerRole)
	}
	regional := testPrincipal(RoleRegionalStaff)
	regional.SubjectID = "10000000-0000-4000-8000-000000000003"
	regional.SessionID = "40000000-0000-4000-8000-000000000003"
	regional.OperatorRevision = 1
	regionalToken := issue(t, store, regional)
	before = calls.Load()
	if result = serve(handler, "GET", path+"/review", nil, regionalToken, "", ""); result.Code != 200 || calls.Load() != before+1 || ownerRole != "REGIONAL_ADMIN" {
		t.Fatal("regional read-only detail failed", result.Code, result.Body.String(), ownerRole)
	}
	detail = ParticipantReviewDetail{}
	if json.Unmarshal(result.Body.Bytes(), &detail) != nil || detail.Kind != "VENDOR" || detail.ContactName != "" || len(detail.Documents) != 0 || detail.BankStatus != "" || detail.BankLast4 != "" || len(detail.ReviewActions) != 0 || detail.ServiceZoneCount != 1 {
		t.Fatal("regional detail retained private review data", result.Body.String())
	}
	var regionalFields map[string]json.RawMessage
	if json.Unmarshal(result.Body.Bytes(), &regionalFields) != nil {
		t.Fatal("regional response was not an object")
	}
	for _, privateField := range []string{"contact_name", "phone_masked", "vehicle_number_masked", "documents", "bank_status", "bank_last4", "last_review_reason", "review_actions", "zones"} {
		if _, exists := regionalFields[privateField]; exists {
			t.Fatal("regional response contained country-only field "+privateField, result.Body.String())
		}
	}
	for _, private := range []string{"private", "Synthetic contact", "1234", "IDENTITY", "APPROVE"} {
		if strings.Contains(result.Body.String(), private) {
			t.Fatal("regional detail leaked "+private, result.Body.String())
		}
	}
	before = calls.Load()
	if result = serve(handler, "GET", "/admin/api/v1/participants/RIDER/"+participantTestID+"/review", nil, regionalToken, "", ""); result.Code != 403 || calls.Load() != before {
		t.Fatal("regional rider detail crossed country boundary", result.Code)
	}
	var regionalSession SessionView
	if result = serve(handler, "GET", "/admin/api/v1/session", nil, regionalToken, "", ""); result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &regionalSession) != nil {
		t.Fatal("regional session unavailable", result.Code)
	}
	if result = serve(handler, "POST", path+"/review-action", []byte(`{"action":"APPROVE","reason":"Regional must not approve"}`), regionalToken, regionalSession.CSRFToken, "https://admin.planext4u.net"); result.Code != 403 || calls.Load() != before {
		t.Fatal("regional review action crossed country boundary", result.Code)
	}
}

func TestFinanceReviewRoleMustBeExplicitAndPairedWithCountry(t *testing.T) {
	var role string
	var posts atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role = r.Header.Get("X-Planext4u-Roles")
		if r.Method == "GET" {
			actions := `["REJECT"]`
			if role == "OPS_ADMIN,FINANCE" {
				actions = `["REJECT","VERIFY_BANK"]`
			}
			_, _ = w.Write([]byte(`{"id":"30000000-0000-4000-8000-000000000001","vendor_id":"` + participantTestID + `","revision":2,"status":"BANK_REVIEW","business_name":"Synthetic vendor","business_type":"SOLE_PROPRIETOR","contact_name":"Synthetic contact","documents":[],"service_zone_count":1,"bank_status":"PENDING_VERIFICATION","bank_last4":"1234","review_actions":` + actions + `,"updated_at":"2026-08-27T10:00:00Z"}`))
			return
		}
		posts.Add(1)
		_, _ = w.Write([]byte(`{"vendor_id":"` + participantTestID + `","revision":3,"status":"BANK_REVIEW"}`))
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	actor := func(finance bool) []Role {
		if finance {
			return []Role{RoleCountryAdmin, RoleFinance}
		}
		return []Role{RoleCountryAdmin}
	}
	for _, finance := range []bool{false, true} {
		handler, store := testHandler(t, true, reader)
		principal := testPrincipal(RoleCountryAdmin)
		principal.Roles = actor(finance)
		principal.SubjectID = "10000000-0000-4000-8000-000000000001"
		principal.SessionID = "40000000-0000-4000-8000-000000000001"
		principal.OperatorRevision = 1
		token := issue(t, store, principal)
		result := serve(handler, "GET", "/admin/api/v1/participants/VENDOR/"+participantTestID+"/review", nil, token, "", "")
		if result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
		if finance && role != "OPS_ADMIN,FINANCE" || !finance && role != "OPS_ADMIN" {
			t.Fatal("finance role was inferred or lost", role)
		}
		var session SessionView
		if response := serve(handler, "GET", "/admin/api/v1/session", nil, token, "", ""); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil {
			t.Fatal("session unavailable")
		}
		command := httptest.NewRequest("POST", "/admin/api/v1/participants/VENDOR/"+participantTestID+"/review-action", strings.NewReader(`{"action":"VERIFY_BANK","reason":"Provider bank verification requested"}`))
		command.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		command.Header.Set("Origin", "https://admin.planext4u.net")
		command.Header.Set("X-CSRF-Token", session.CSRFToken)
		command.Header.Set("Idempotency-Key", "finance-review-0001")
		command.Header.Set("If-Match", "2")
		before := posts.Load()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, command)
		if finance {
			if response.Code != 200 || role != "FINANCE" || posts.Load() != before+1 {
				t.Fatal("explicit finance command rejected", response.Code, role)
			}
		} else if response.Code != 403 || posts.Load() != before {
			t.Fatal("bank verification granted without explicit FINANCE", response.Code, posts.Load())
		}
	}
}

func TestRegionalReviewDetailRejectsOverbroadOwnerProjection(t *testing.T) {
	id := participantTestID
	actor := fulfillment.Actor{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", Subject: "10000000-0000-4000-8000-000000000003", Roles: []string{"REGIONAL_ADMIN"}, MFAVerified: true, OperatorSessionID: "40000000-0000-4000-8000-000000000003", OperatorRevision: 1}
	base := `{"id":"30000000-0000-4000-8000-000000000001","vendor_id":"` + id + `","revision":2,"status":"BANK_REVIEW","business_name":"Synthetic vendor","business_type":"SOLE_PROPRIETOR","contact_name":"","documents":[],"field_visit":{"scheduled_at":"2026-08-27T09:00:00Z"},"service_zone_count":1,"bank_status":"","review_actions":[],"updated_at":"2026-08-27T10:00:00Z"}`
	for name, response := range map[string]string{
		"contact":  strings.Replace(base, `"contact_name":""`, `"contact_name":"Private person"`, 1),
		"bank":     strings.Replace(base, `"bank_status":""`, `"bank_status":"VERIFIED"`, 1),
		"document": strings.Replace(base, `"documents":[]`, `"documents":[{"kind":"OWNER_IDENTITY","ocr_status":"VERIFIED"}]`, 1),
		"decision": strings.Replace(base, `"review_actions":[]`, `"review_actions":["APPROVE"]`, 1),
		"extra":    strings.Replace(base, `"updated_at":`, `"bank_reference":"private","updated_at":`, 1),
		"visit":    strings.Replace(base, `"scheduled_at":"2026-08-27T09:00:00Z"`, `"scheduled_at":"2026-08-27T09:00:00Z","latitude":13.08`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer owner.Close()
			endpoint, _ := url.Parse(owner.URL)
			reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
			if _, err := reader.ParticipantReviewDetail(t.Context(), actor, "VENDOR", id); err == nil {
				t.Fatal("regional BFF accepted private owner detail")
			}
		})
	}
}

func TestRiderCountryReviewMapsOwnerDetailAndProviderGatedApproval(t *testing.T) {
	id := "60000000-0000-4000-8000-000000000001"
	var lastPath, lastRole string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastRole = r.URL.Path, r.Header.Get("X-Planext4u-Roles")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"70000000-0000-4000-8000-000000000001","rider_id":"` + id + `","revision":4,"status":"KYC_REVIEW","full_name":"Synthetic rider","phone_masked":"******1234","vehicle_type":"BIKE","vehicle_number_masked":"••••6789","documents":[{"kind":"IDENTITY","ocr_status":"PENDING","asset_id":"private"}],"bank_status":"PENDING","zones":["North"],"review_actions":["APPROVE","REJECT"],"updated_at":"2026-08-27T10:00:00Z","bank_reference":"private"}`))
			return
		}
		var input struct {
			Approved bool   `json:"approved"`
			Reason   string `json:"reason"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || !input.Approved || input.Reason != "Verified rider evidence and bank" || r.Header.Get("If-Match") != "4" {
			t.Error("rider owner approval changed")
		}
		_, _ = w.Write([]byte(`{"rider_id":"` + id + `","revision":5,"status":"APPROVED","bank_reference":"private"}`))
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	actor := fulfillment.Actor{TenantID: "tenant-1", Country: "IN", Subject: "10000000-0000-4000-8000-000000000001", Roles: []string{"OPS_ADMIN"}, MFAVerified: true, OperatorSessionID: "40000000-0000-4000-8000-000000000001", OperatorRevision: 1}
	detail, err := reader.ParticipantReviewDetail(t.Context(), actor, "RIDER", id)
	if err != nil || detail.Kind != "RIDER" || detail.ParticipantID != id || detail.PhoneMasked != "******1234" || detail.VehicleNumberMasked != "••••6789" || len(detail.Documents) != 1 || lastPath != "/v1/rider/applications/"+id+"/review" || lastRole != "OPS_ADMIN" {
		t.Fatal("invalid rider detail", detail, err, lastPath, lastRole)
	}
	value, _, err := reader.ReviewParticipant(t.Context(), actor, "RIDER", id, "APPROVE", "rider-approval-0001", 4, "Verified rider evidence and bank")
	if err != nil || value.Status != "APPROVED" || value.Revision != 5 || lastPath != "/v1/rider/applications/"+id+"/review" || lastRole != "OPS_ADMIN" {
		t.Fatal("invalid rider approval", value, err, lastPath, lastRole)
	}
}
