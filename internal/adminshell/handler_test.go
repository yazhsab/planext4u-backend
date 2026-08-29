package adminshell

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
	"github.com/yazhsab/planext4u-backend/internal/audit"
)

var testNow = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

func TestHandlerRequiresAdministratorSessionAndMFA(t *testing.T) {
	handler, store := testHandler(t, true)

	request := httptest.NewRequest(http.MethodGet, "/admin/api/v1/session", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatusAndCode(t, response, http.StatusUnauthorized, "ADMIN_AUTHENTICATION_REQUIRED")

	principal := testPrincipal(RoleSuperAdmin)
	principal.AuthMethods = []string{"password"}
	token := issue(t, store, principal)
	response = serve(handler, http.MethodGet, "/admin/api/v1/session", nil, token, "", "")
	assertStatusAndCode(t, response, http.StatusForbidden, "ADMIN_MFA_REQUIRED")
}

func TestSessionReturnsServerAuthorizedNavigationAndAssurance(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleAuditor)
	token := issue(t, store, principal)

	response := serve(handler, http.MethodGet, "/admin/api/v1/session", nil, token, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var view SessionView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if view.SelectedCountry != "IN" || len(view.Navigation) != 4 || view.Navigation[1].Path != "/operations" || view.Navigation[2].Path != "/governance" || view.Navigation[3].Path != "/audit" {
		t.Fatalf("unexpected session view: %#v", view)
	}
	if !view.Assurance.MFASatisfied || !view.Assurance.FreshAuth || view.CSRFToken == "" {
		t.Fatalf("expected MFA, fresh authentication and CSRF state: %#v", view.Assurance)
	}
}

func TestBEP5010GovernanceDashboardIsMFAProtectedCountryScopedAndMasked(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleCountryAdmin)
	token := issue(t, store, principal)
	response := serve(handler, http.MethodGet, "/admin/api/v1/governance", nil, token, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var view GovernanceView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Country != "IN" || view.PrivacyMode != "aggregate_and_masked" || len(view.Metrics) != 4 || !view.Metrics[0].Masked || !view.FeatureFlags["emergency"] {
		t.Fatalf("governance=%#v", view)
	}
}

func TestSupportAndContentRolesCannotReadAudit(t *testing.T) {
	handler, store := testHandler(t, true)
	for _, role := range []Role{RoleSupportAdmin, RoleContentAdmin} {
		t.Run(string(role), func(t *testing.T) {
			token := issue(t, store, testPrincipal(role))
			response := serve(handler, http.MethodGet, "/admin/api/v1/audit/events", nil, token, "", "")
			assertStatusAndCode(t, response, http.StatusForbidden, "ADMIN_ROLE_FORBIDDEN")
		})
	}
}

func TestAuditIsAlwaysRestrictedToSelectedCountry(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleCountryAdmin)
	principal.AllowedCountries = []string{"IN", "US"}
	token := issue(t, store, principal)

	response := serve(handler, http.MethodGet, "/admin/api/v1/audit/events?country=US&limit=50", nil, token, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var page audit.Page
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Country != "IN" {
		t.Fatalf("expected only selected-country entries, got %#v", page.Entries)
	}
}

func TestCountryChangeRequiresSameOriginCSRFAndAllowedCountry(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleCountryAdmin)
	principal.AllowedCountries = []string{"IN", "SG"}
	token := issue(t, store, principal)
	csrf := resolveCSRF(t, store, token)

	crossSite := serve(handler, http.MethodPut, "/admin/api/v1/session/country", []byte(`{"country":"SG"}`), token, csrf, "https://attacker.example")
	assertStatusAndCode(t, crossSite, http.StatusForbidden, "ADMIN_CSRF_INVALID")

	forbidden := serve(handler, http.MethodPut, "/admin/api/v1/session/country", []byte(`{"country":"US"}`), token, csrf, "https://admin.planext4u.net")
	assertStatusAndCode(t, forbidden, http.StatusForbidden, "ADMIN_COUNTRY_FORBIDDEN")

	changed := serve(handler, http.MethodPut, "/admin/api/v1/session/country", []byte(`{"country":"SG"}`), token, csrf, "https://admin.planext4u.net")
	if changed.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", changed.Code, changed.Body.String())
	}
	session, err := store.Resolve(requestWithCookie(token))
	if err != nil || session.Principal.SelectedCountry != "SG" {
		t.Fatalf("expected persisted SG context, session=%#v err=%v", session, err)
	}
}

func TestSecurityHeadersAreAppliedToFailures(t *testing.T) {
	handler, _ := testHandler(t, true)
	response := serve(handler, http.MethodGet, "/admin/api/v1/session", nil, "", "", "")
	for header, expected := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if response.Header().Get(header) != expected {
			t.Errorf("expected %s=%q, got %q", header, expected, response.Header().Get(header))
		}
	}
}

func TestOperationsEnforceCSRFDomainRBACAndFourEyes(t *testing.T) {
	handler, store := testHandler(t, true)
	requester := testPrincipal(RoleCountryAdmin)
	requester.SubjectID = "admin-requester"
	requester.SessionID = "session-requester"
	requesterToken := issue(t, store, requester)
	requesterCSRF := resolveCSRF(t, store, requesterToken)
	body := []byte(`{"domain":"WALLET","action":"ADJUST","target_id":"customer-001","reason":"Correct verified settlement discrepancy","payload":{"points":100}}`)

	missingCSRF := serve(handler, http.MethodPost, "/admin/api/v1/operations", body, requesterToken, "", "https://admin.planext4u.net")
	assertStatusAndCode(t, missingCSRF, http.StatusForbidden, "ADMIN_CSRF_INVALID")

	created := serve(handler, http.MethodPost, "/admin/api/v1/operations", body, requesterToken, requesterCSRF, "https://admin.planext4u.net")
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", created.Code, created.Body.String())
	}
	var change adminops.Change
	if err := json.Unmarshal(created.Body.Bytes(), &change); err != nil || change.Status != adminops.StatusPending {
		t.Fatalf("created=%#v err=%v", change, err)
	}

	approvalBody := []byte(`{"expected_revision":1}`)
	selfApproval := serve(handler, http.MethodPost, "/admin/api/v1/operations/"+change.ID+"/approve", approvalBody, requesterToken, requesterCSRF, "https://admin.planext4u.net")
	assertStatusAndCode(t, selfApproval, http.StatusConflict, "ADMIN_FOUR_EYES_REQUIRED")

	approver := testPrincipal(RoleCountryAdmin)
	approver.SubjectID = "admin-approver"
	approver.SessionID = "session-approver"
	approverToken := issue(t, store, approver)
	approved := serve(handler, http.MethodPost, "/admin/api/v1/operations/"+change.ID+"/approve", approvalBody, approverToken, resolveCSRF(t, store, approverToken), "https://admin.planext4u.net")
	if approved.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", approved.Code, approved.Body.String())
	}

	supportToken := issue(t, store, testPrincipal(RoleSupportAdmin))
	forbidden := serve(handler, http.MethodPost, "/admin/api/v1/operations", body, supportToken, resolveCSRF(t, store, supportToken), "https://admin.planext4u.net")
	assertStatusAndCode(t, forbidden, http.StatusForbidden, "ADMIN_OPERATION_FORBIDDEN")
}

func TestOperationsListIsCountryAndCapabilityScoped(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleCountryAdmin)
	token := issue(t, store, principal)
	csrf := resolveCSRF(t, store, token)
	created := serve(handler, http.MethodPost, "/admin/api/v1/operations", []byte(`{"domain":"CATALOG","action":"UPSERT","target_id":"item-001","reason":"Publish verified catalogue information","payload":{"name":"Synthetic item"}}`), token, csrf, "https://admin.planext4u.net")
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}

	contentToken := issue(t, store, testPrincipal(RoleContentAdmin))
	listed := serve(handler, http.MethodGet, "/admin/api/v1/operations", nil, contentToken, "", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"domain":"CATALOG"`)) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}

	supportToken := issue(t, store, testPrincipal(RoleSupportAdmin))
	filtered := serve(handler, http.MethodGet, "/admin/api/v1/operations", nil, supportToken, "", "")
	if filtered.Code != http.StatusOK || bytes.Contains(filtered.Body.Bytes(), []byte(`"domain":"CATALOG"`)) {
		t.Fatalf("filtered list=%d %s", filtered.Code, filtered.Body.String())
	}
}

func testHandler(t *testing.T, requireMFA bool) (http.Handler, *MemorySessionStore) {
	t.Helper()
	store, err := NewMemorySessionStore(func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create sessions: %v", err)
	}
	repository := audit.NewMemoryRepository()
	auditService, err := audit.NewService(repository, func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create audit: %v", err)
	}
	for index, country := range []string{"IN", "US"} {
		_, err = auditService.Record(context.Background(), audit.Principal{TenantID: "tenant-1", SubjectID: "system", Capabilities: map[string]bool{"audit.write": true}}, audit.RecordRequest{
			Country: country, Actor: audit.Actor{SubjectID: "system", ActorType: "SYSTEM"}, Action: "admin.session.opened",
			Target: audit.Target{Type: "session", ID: "session-" + country}, Outcome: audit.OutcomeSucceeded,
			ReasonCode: "SESSION_OPENED", CorrelationID: "corr-" + country, OccurredAt: testNow.Add(time.Duration(index) * time.Second),
		})
		if err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}
	operations, err := adminops.NewService(func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create operations: %v", err)
	}
	handler, err := NewHandler(Config{
		Sessions: store, Audit: auditService, Operations: operations, Clock: func() time.Time { return testNow },
		AllowedOrigins: []string{"https://admin.planext4u.net"}, RequireMFA: requireMFA,
	})
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	return handler, store
}

func testPrincipal(role Role) Principal {
	return Principal{
		SubjectID: "admin-1", SessionID: "session-1", TenantID: "tenant-1", DisplayName: "Planext Administrator",
		Roles: []Role{role}, AllowedCountries: []string{"IN"}, SelectedCountry: "IN",
		AuthenticatedAt: testNow.Add(-time.Minute), AuthMethods: []string{"password", "webauthn"},
	}
}

func issue(t *testing.T, store *MemorySessionStore, principal Principal) string {
	t.Helper()
	token, err := store.Issue(principal, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return token
}

func serve(handler http.Handler, method, path string, body []byte, token, csrf, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.Header.Set("X-Correlation-ID", "corr-admin-shell-test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requestWithCookie(token string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	return request
}

func resolveCSRF(t *testing.T, store *MemorySessionStore, token string) string {
	t.Helper()
	session, err := store.Resolve(requestWithCookie(token))
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	return session.CSRFToken
}

func assertStatusAndCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("expected %d, got %d: %s", status, response.Code, response.Body.String())
	}
	var problem struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Error.Code != code {
		t.Fatalf("expected code %s, got %#v (decode err=%v)", code, problem, err)
	}
}
