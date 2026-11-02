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
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
)

const participantTestID = "20000000-0000-4000-8000-000000000002"

func TestParticipantBFFUsesCurrentSourceAndProjectsPrivateOwnerResponses(t *testing.T) {
	var mode atomic.Int64
	var calls atomic.Int64
	captured := make(chan http.Header, 10)
	summary := participantreview.Summary{ID: "30000000-0000-4000-8000-000000000001", ParticipantID: participantTestID, Kind: "VENDOR", Revision: 2, Status: "KYC_REVIEW", DisplayName: "Synthetic vendor", Category: "SOLE_PROPRIETOR", DocumentCount: 2, UpdatedAt: testNow, Actions: []string{"REJECT"}}
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		captured <- r.Header.Clone()
		if code := int(mode.Load()); code != 0 {
			w.WriteHeader(code)
			return
		}
		if r.Method == "POST" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["status"] != "REJECTED" || body["reason"] != "Review failed for documented reasons" {
				t.Error("owning command changed")
			}
			w.Header().Set("Idempotency-Replayed", "true")
			_, _ = w.Write([]byte(`{"vendor_id":"` + participantTestID + `","status":"REJECTED","revision":3,"bank_reference":"private","documents":[{"asset_id":"private"}]}`))
			return
		}
		if r.URL.Query().Get("search") != "%_Tamil" {
			t.Error("literal query changed")
		}
		_ = json.NewEncoder(w).Encode(participantreview.Page{Items: []participantreview.Summary{summary}})
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
	response := serve(handler, "GET", "/admin/api/v1/participants/VENDOR?search=%25_Tamil", nil, token, "", "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	source := <-captured
	if source.Get("X-Planext4u-Operator-Session") != principal.SessionID || source.Get("X-Planext4u-Operator-Revision") != "7" || source.Get("X-Planext4u-Subject") != principal.SubjectID || source.Get("X-Planext4u-Country") != "IN" || source.Get("X-Planext4u-Roles") != "OPS_ADMIN" || source.Get("Cookie") != "" {
		t.Fatal("wrong source", source)
	}
	sessionResponse := serve(handler, "GET", "/admin/api/v1/session", nil, token, "", "")
	var session SessionView
	if json.Unmarshal(sessionResponse.Body.Bytes(), &session) != nil {
		t.Fatal("session")
	}
	path := "/admin/api/v1/participants/VENDOR/" + participantTestID + "/reject"
	command := func(body, key, revision, csrf string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", path, strings.NewReader(body))
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		request.Header.Set("Origin", "https://admin.planext4u.net")
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("If-Match", revision)
		request.Header.Set("X-Planext4u-Subject", "foreign")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	body := `{"reason":"Review failed for documented reasons"}`
	result := command(body, "participant-rejection-test", "\"2\"", session.CSRFToken)
	if result.Code != 200 || result.Header().Get("Idempotency-Replayed") != "true" || strings.Contains(result.Body.String(), "private") || !strings.Contains(result.Body.String(), participantTestID) {
		t.Fatal(result.Code, result.Body.String())
	}
	source = <-captured
	if source.Get("If-Match") != "2" || source.Get("Idempotency-Key") != "participant-rejection-test" || source.Get("X-Planext4u-Subject") != principal.SubjectID {
		t.Fatal(source)
	}
	before := calls.Load()
	for _, raw := range []string{body + ` {}`, `{"reason":"short"}`, `{"reason":"Review failed for documented reasons","approved":true}`} {
		if result = command(raw, "participant-rejection-test", "2", session.CSRFToken); result.Code != 422 {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	if result = command(body, "participant-rejection-test", "2", "bad"); result.Code != 403 {
		t.Fatal(result.Code)
	}
	for _, path := range []string{"/admin/api/v1/participants/VENDOR?country=US", "/admin/api/v1/participants/UNKNOWN", "/admin/api/v1/participants/VENDOR?status=A&status=B"} {
		if response = serve(handler, "GET", path, nil, token, "", ""); response.Code != 422 {
			t.Fatal(response.Code)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid request reached owner")
	}
	for _, code := range []int{403, 404, 409, 422, 503, 302} {
		mode.Store(int64(code))
		response = serve(handler, "GET", "/admin/api/v1/participants/VENDOR", nil, token, "", "")
		want := code
		if code == 302 {
			want = 503
		}
		if response.Code != want {
			t.Fatalf("owner %d -> %d", code, response.Code)
		}
		<-captured
	}
}

func TestParticipantReviewsDenyOtherRolesStaleMFAAndIncompleteOwners(t *testing.T) {
	for _, role := range []Role{RoleAuditor, RoleSupportAdmin, RoleContentAdmin} {
		handler, store := testHandler(t, true)
		token := issue(t, store, testPrincipal(role))
		response := serve(handler, "GET", "/admin/api/v1/participants/VENDOR", nil, token, "", "")
		if response.Code != 403 {
			t.Fatalf("role %s: %d", role, response.Code)
		}
	}
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleCountryAdmin)
	principal.AuthenticatedAt = testNow.Add(-6 * time.Minute)
	token := issue(t, store, principal)
	if response := serve(handler, "GET", "/admin/api/v1/participants/VENDOR", nil, token, "", ""); response.Code != 403 {
		t.Fatal(response.Code)
	}
	actor := fulfillment.Actor{TenantID: "tenant", Country: "IN", Subject: "actor", Roles: []string{"OPS_ADMIN"}, MFAVerified: true, OperatorSessionID: "40000000-0000-4000-8000-000000000001", OperatorRevision: 1}
	for _, body := range []string{`{}`, `{"items":null}`, `{"items":[{}]}`, `{"items":[],"next_after":"40000000-0000-4000-8000-000000000001"}`} {
		owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		endpoint, _ := url.Parse(owner.URL)
		reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
		if _, err := reader.ParticipantApplications(t.Context(), actor, "VENDOR", participantreview.Filter{}); err == nil {
			t.Error("incomplete owner accepted", body)
		}
		owner.Close()
	}
}

func TestRegionalParticipantQueueRejectsOwnerFilterAndCursorViolations(t *testing.T) {
	var mode atomic.Int64
	item := participantreview.Summary{ID: "30000000-0000-4000-8000-000000000002", ParticipantID: participantTestID,
		Kind: "VENDOR", Revision: 2, Status: "OCR_REVIEW", DisplayName: "Scoped synthetic vendor",
		Category: "SOLE_PROPRIETOR", DocumentCount: 2, BankStatus: "VERIFIED", UpdatedAt: testNow, Actions: []string{"REJECT"}}
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Planext4u-Roles") != "REGIONAL_ADMIN" {
			t.Errorf("regional queue claimed elevated role %q", r.Header.Get("X-Planext4u-Roles"))
		}
		value := item
		switch mode.Load() {
		case 1:
			value.Status = "APPROVED" // Outside the requested regional review queue.
		case 2:
			value.ID = "30000000-0000-4000-8000-000000000001" // At the exclusive cursor.
		}
		_ = json.NewEncoder(w).Encode(participantreview.Page{Items: []participantreview.Summary{value}})
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, err := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	handler, store := testHandler(t, true, reader)
	principal := testPrincipal(RoleRegionalStaff)
	principal.SubjectID = "10000000-0000-4000-8000-000000000003"
	principal.SessionID = "40000000-0000-4000-8000-000000000003"
	principal.OperatorRevision = 1
	token := issue(t, store, principal)
	path := "/admin/api/v1/participants/VENDOR?status=OCR_REVIEW&after=30000000-0000-4000-8000-000000000001"
	response := serve(handler, http.MethodGet, path, nil, token, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), item.DisplayName) {
		t.Fatalf("valid scoped page rejected: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "bank_status") || strings.Contains(response.Body.String(), "document_count") || strings.Contains(response.Body.String(), "VERIFIED") {
		t.Fatal("regional queue exposed country-only document or bank metadata", response.Body.String())
	}
	for _, invalid := range []int64{1, 2} {
		mode.Store(invalid)
		response = serve(handler, http.MethodGet, path, nil, token, "", "")
		assertStatusAndCode(t, response, 503, "ADMIN_FULFILLMENT_UNAVAILABLE")
		if strings.Contains(response.Body.String(), item.DisplayName) {
			t.Fatal("out-of-filter participant leaked to regional browser")
		}
	}
}

func TestRegionalParticipantRejectionUsesRegionalOwnerRole(t *testing.T) {
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vendor/applications/"+participantTestID+"/transitions" || r.Header.Get("X-Planext4u-Roles") != "REGIONAL_ADMIN" {
			t.Errorf("regional rejection escalated owner role: %s %s %q", r.Method, r.URL.Path, r.Header.Get("X-Planext4u-Roles"))
		}
		_, _ = w.Write([]byte(`{"vendor_id":"` + participantTestID + `","status":"REJECTED","revision":3}`))
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, err := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	actor := fulfillment.Actor{TenantID: "30000000-0000-4000-8000-000000000001", Country: "IN", Subject: "10000000-0000-4000-8000-000000000003", Roles: []string{"REGIONAL_ADMIN"}, MFAVerified: true, OperatorSessionID: "40000000-0000-4000-8000-000000000003", OperatorRevision: 1}
	receipt, _, err := reader.RejectParticipant(t.Context(), actor, "VENDOR", participantTestID, "regional-reject-test", 2, "Documented regional rejection")
	if err != nil || receipt.Kind != "VENDOR" || receipt.ParticipantID != participantTestID || receipt.Status != "REJECTED" || receipt.Revision != 3 {
		t.Fatalf("regional owner rejection failed: %+v %v", receipt, err)
	}
}

func TestVendorFieldCheckInUsesCurrentRegionalSourceAndMinimalReceipt(t *testing.T) {
	var calls atomic.Int64
	var mode atomic.Int64
	var lastHeader http.Header
	var lastBody map[string]float64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastHeader = r.Header.Clone()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vendor/applications/"+participantTestID+"/field-check-in" {
			t.Error("wrong owning path")
		}
		if json.NewDecoder(r.Body).Decode(&lastBody) != nil {
			t.Error("wrong owning input")
		}
		if code := int(mode.Load()); code != 0 {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		_, _ = w.Write([]byte(`{"vendor_id":"` + participantTestID + `","status":"FIELD_VISIT_PASSED","revision":3,"documents":[{"asset_id":"private"}],"bank_account":{"reference":"private"}}`))
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	principal := testPrincipal(RoleRegionalStaff)
	principal.SubjectID = "10000000-0000-4000-8000-000000000001"
	principal.SessionID = "40000000-0000-4000-8000-000000000001"
	principal.OperatorRevision = 7
	token := issue(t, store, principal)
	var view SessionView
	if result := serve(handler, "GET", "/admin/api/v1/session", nil, token, "", ""); result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &view) != nil {
		t.Fatal("regional session unavailable")
	}
	path := "/admin/api/v1/participants/VENDOR/" + participantTestID + "/field-check-in"
	command := func(body, key, revision, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		r.Header.Set("Origin", "https://admin.planext4u.net")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("If-Match", revision)
		r.Header.Set("X-Planext4u-Subject", "spoofed")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, r)
		return result
	}
	body := `{"latitude":13.0827,"longitude":80.2707}`
	result := command(body, "vendor-checkin-0001", `"2"`, view.CSRFToken)
	if result.Code != 200 || result.Header().Get("Idempotency-Replayed") != "true" || strings.Contains(result.Body.String(), "private") {
		t.Fatal(result.Code, result.Body.String())
	}
	var receipt ParticipantReceipt
	if json.Unmarshal(result.Body.Bytes(), &receipt) != nil || receipt.Kind != "VENDOR" || receipt.ParticipantID != participantTestID || receipt.Status != "FIELD_VISIT_PASSED" || receipt.Revision != 3 {
		t.Fatal(result.Body.String())
	}
	if lastHeader.Get("X-Planext4u-Roles") != "FIELD_OFFICER" || lastHeader.Get("X-Planext4u-Subject") != principal.SubjectID || lastHeader.Get("X-Planext4u-Operator-Session") != principal.SessionID || lastHeader.Get("X-Planext4u-Operator-Revision") != "7" || lastHeader.Get("X-Planext4u-Country") != "IN" || lastHeader.Get("If-Match") != "2" || lastHeader.Get("Idempotency-Key") != "vendor-checkin-0001" || lastHeader.Get("Cookie") != "" || lastBody["latitude"] != 13.0827 || lastBody["longitude"] != 80.2707 {
		t.Fatal("incorrect source or point", lastHeader, lastBody)
	}
	before := calls.Load()
	for _, malformed := range []string{body + ` {}`, `{"latitude":91,"longitude":80.2707}`, `{"latitude":13.0827}`, `{"latitude":13.0827,"longitude":80.2707,"approved":true}`} {
		if result = command(malformed, "vendor-checkin-0001", "2", view.CSRFToken); result.Code != 422 {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	if result = command(body, "vendor-checkin-0001", "2", "wrong"); result.Code != 403 {
		t.Fatal(result.Code)
	}
	if calls.Load() != before {
		t.Fatal("invalid command reached owner")
	}
	mode.Store(403)
	if result = command(body, "vendor-checkin-0001", "2", view.CSRFToken); result.Code != 403 {
		t.Fatal("owner revocation was not honored", result.Code)
	}
}
