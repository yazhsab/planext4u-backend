package adminshell

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
	"github.com/yazhsab/planext4u-backend/internal/audit"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/governance"
	"github.com/yazhsab/planext4u-backend/internal/support"
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

func TestReportDetailsAndAuditedSignedExportAreCountryScoped(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleCountryAdmin)
	token := issue(t, store, principal)
	csrf := resolveCSRF(t, store, token)

	listed := serve(handler, http.MethodGet, "/admin/api/v1/reports?domain=social&limit=20", nil, token, "", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"id":"social-active"`)) || !bytes.Contains(listed.Body.Bytes(), []byte(`"export_policy":"MFA_AND_AUDIT_REQUIRED"`)) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	detail := serve(handler, http.MethodGet, "/admin/api/v1/reports/social-active?limit=20", nil, token, "", "")
	if detail.Code != http.StatusOK || !bytes.Contains(detail.Body.Bytes(), []byte(`"source_projection":"governance.report_cards"`)) || !bytes.Contains(detail.Body.Bytes(), []byte(`"country":"IN"`)) {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}

	exported := serve(handler, http.MethodPost, "/admin/api/v1/reports/social-active/exports", []byte(`{"format":"CSV","reason":"Quarterly reporting review"}`), token, csrf, "https://admin.planext4u.net")
	if exported.Code != http.StatusAccepted {
		t.Fatalf("export=%d %s", exported.Code, exported.Body.String())
	}
	if exported.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export response must not be cached: %v", exported.Header())
	}
	var export ReportExport
	if err := json.Unmarshal(exported.Body.Bytes(), &export); err != nil || export.Status != "READY" || export.DownloadURL == "" || export.ChecksumSHA256 == "" {
		t.Fatalf("export=%#v err=%v", export, err)
	}
	status := serve(handler, http.MethodGet, "/admin/api/v1/report-exports/"+export.ID, nil, token, "", "")
	if status.Code != http.StatusOK || status.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(status.Body.Bytes(), []byte(`"status":"READY"`)) {
		t.Fatalf("status=%d %s", status.Code, status.Body.String())
	}
	download := serve(handler, http.MethodGet, export.DownloadURL, nil, token, "", "")
	if download.Code != http.StatusOK || download.Header().Get("Cache-Control") != "no-store" || download.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !bytes.Contains(download.Body.Bytes(), []byte("social-active")) {
		t.Fatalf("download=%d headers=%v body=%s", download.Code, download.Header(), download.Body.String())
	}
	invalid := serve(handler, http.MethodGet, "/admin/api/v1/report-exports/"+export.ID+"/download?token=invalid", nil, token, "", "")
	assertStatusAndCode(t, invalid, http.StatusForbidden, "ADMIN_REPORT_EXPORT_TOKEN_INVALID")

	stale := testPrincipal(RoleCountryAdmin)
	stale.SessionID = "session-stale-report"
	stale.AuthenticatedAt = testNow.Add(-10 * time.Minute)
	staleToken := issue(t, store, stale)
	staleExport := serve(handler, http.MethodPost, "/admin/api/v1/reports/social-active/exports", []byte(`{"format":"CSV","reason":"Quarterly reporting review"}`), staleToken, resolveCSRF(t, store, staleToken), "https://admin.planext4u.net")
	assertStatusAndCode(t, staleExport, http.StatusForbidden, "ADMIN_FRESH_MFA_REQUIRED")
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

func TestBE004SupportProjectionIsSessionAuthorizedAndCountryScoped(t *testing.T) {
	handler, store := testHandler(t, true)
	supportToken := issue(t, store, testPrincipal(RoleSupportAdmin))
	response := serve(handler, http.MethodGet, "/admin/api/v1/support/tickets?owner_role=CUSTOMER", nil, supportToken, "", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"subject":"Synthetic customer support request"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"owner_reference":"customer-001"`)) {
		t.Fatalf("support projection=%d %s", response.Code, response.Body.String())
	}
	contentToken := issue(t, store, testPrincipal(RoleContentAdmin))
	forbidden := serve(handler, http.MethodGet, "/admin/api/v1/support/tickets", nil, contentToken, "", "")
	assertStatusAndCode(t, forbidden, http.StatusForbidden, "ADMIN_ROLE_FORBIDDEN")

	principal := testPrincipal(RoleSupportAdmin)
	principal.SelectedCountry = "US"
	principal.AllowedCountries = []string{"IN", "US"}
	usToken := issue(t, store, principal)
	empty := serve(handler, http.MethodGet, "/admin/api/v1/support/tickets", nil, usToken, "", "")
	if empty.Code != http.StatusOK || !bytes.Contains(empty.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("country-scoped projection=%d %s", empty.Code, empty.Body.String())
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

func TestCMSPageDraftIsCountryScopedCSRFProtectedAndRevisionSafe(t *testing.T) {
	handler, store := testHandler(t, true)
	principal := testPrincipal(RoleContentAdmin)
	token := issue(t, store, principal)
	csrf := resolveCSRF(t, store, token)
	body := []byte(`{"expected_revision":0,"page":{"id":"customer-home","route":"/home","title_key":"page.home","audience":["CUSTOMER"],"enabled":true,"blocks":[{"id":"hero","kind":"HERO","enabled":true,"priority":10,"content":{"title":"Local first"}}]}}`)

	withoutCSRF := serve(handler, http.MethodPut, "/admin/api/v1/cms/pages/customer-home/draft", body, token, "", "https://admin.planext4u.net")
	assertStatusAndCode(t, withoutCSRF, http.StatusForbidden, "ADMIN_CSRF_INVALID")
	saved := serve(handler, http.MethodPut, "/admin/api/v1/cms/pages/customer-home/draft", body, token, csrf, "https://admin.planext4u.net")
	if saved.Code != http.StatusOK || !bytes.Contains(saved.Body.Bytes(), []byte(`"revision":1`)) || !bytes.Contains(saved.Body.Bytes(), []byte(`"country":"IN"`)) {
		t.Fatalf("saved=%d %s", saved.Code, saved.Body.String())
	}
	stale := serve(handler, http.MethodPut, "/admin/api/v1/cms/pages/customer-home/draft", body, token, csrf, "https://admin.planext4u.net")
	assertStatusAndCode(t, stale, http.StatusConflict, "CMS_REVISION_CONFLICT")
	listed := serve(handler, http.MethodGet, "/admin/api/v1/cms/pages", nil, token, "", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"id":"customer-home"`)) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
}

func TestCMSWorkspaceDraftControlsGlobalMobileConfiguration(t *testing.T) {
	handler, store := testHandler(t, true)
	token := issue(t, store, testPrincipal(RoleContentAdmin))
	csrf := resolveCSRF(t, store, token)
	missing := serve(handler, http.MethodGet, "/admin/api/v1/cms/workspace", nil, token, "", "")
	assertStatusAndCode(t, missing, http.StatusNotFound, "CMS_PAGE_NOT_FOUND")
	body := []byte(`{"expected_revision":0,"workspace":{"minimum_versions":{"ANDROID":"1.0.0","IOS":"1.0.0","WEB":"1.0.0"},"latest_versions":{"ANDROID":"1.2.0","IOS":"1.2.0","WEB":"1.2.0"},"supported_locales":["en","ta"],"default_locale":"en","consent_policies":[{"purpose":"ANALYTICS","policy_version":"privacy-2026-01","required":false}],"flags":{"customer_home":true,"vendor_home":true,"rider_home":true},"home_sections":[{"id":"hero","kind":"HERO","title_key":"home.hero","enabled":true,"priority":10}]}}`)
	withoutCSRF := serve(handler, http.MethodPut, "/admin/api/v1/cms/workspace/draft", body, token, "", "https://admin.planext4u.net")
	assertStatusAndCode(t, withoutCSRF, http.StatusForbidden, "ADMIN_CSRF_INVALID")
	saved := serve(handler, http.MethodPut, "/admin/api/v1/cms/workspace/draft", body, token, csrf, "https://admin.planext4u.net")
	if saved.Code != http.StatusOK || !bytes.Contains(saved.Body.Bytes(), []byte(`"revision":1`)) || !bytes.Contains(saved.Body.Bytes(), []byte(`"rider_home":true`)) {
		t.Fatalf("saved=%d %s", saved.Code, saved.Body.String())
	}
	loaded := serve(handler, http.MethodGet, "/admin/api/v1/cms/workspace", nil, token, "", "")
	if loaded.Code != http.StatusOK || !bytes.Contains(loaded.Body.Bytes(), []byte(`"vendor_home":true`)) {
		t.Fatalf("loaded=%d %s", loaded.Code, loaded.Body.String())
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
	cms, err := configcms.NewAuthoringService(configcms.NewMemoryDraftRepository(), func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create CMS authoring: %v", err)
	}
	workspace, err := configcms.NewWorkspaceAuthoringService(configcms.NewMemoryWorkspaceDraftRepository(), func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create CMS workspace authoring: %v", err)
	}
	governanceService, err := governance.NewService(governance.Configuration{
		TenantID:  "tenant-1",
		Countries: []governance.CountryControl{{Country: "IN", Currency: "INR", Locales: []string{"en", "ta"}, FeatureFlags: map[string]bool{"socio": true, "homes": true, "classifieds": true, "emergency": true}, PolicyVersion: "policy-IN-2026.08"}},
		Reports:   []governance.ReportCard{{ID: "social-active", Title: "Socio active", Value: 5600, Unit: "count", Freshness: testNow}, {ID: "homes-active", Title: "Homes active", Value: 98, Unit: "count", Freshness: testNow}, {ID: "classifieds-active", Title: "Classifieds active", Value: 340, Unit: "count", Freshness: testNow}, {ID: "emergency-sla", Title: "Emergency within SLA", Value: 99, Unit: "percent", Freshness: testNow}},
	}, func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create governance: %v", err)
	}
	supportService, err := support.NewService(support.NewMemoryRepository(), func() time.Time { return testNow })
	if err != nil {
		t.Fatalf("create support service: %v", err)
	}
	if _, _, err = supportService.Create(context.Background(), support.Actor{TenantID: "tenant-1", Country: "IN", Subject: "customer-001", Roles: []support.Role{support.RoleCustomer}}, support.CreateTicketRequest{OwnerRole: support.RoleCustomer, Category: support.CategoryOrder, Subject: "Synthetic customer support request", Description: "The synthetic order needs assistance."}, "admin-projection-ticket-0001"); err != nil {
		t.Fatalf("seed support service: %v", err)
	}
	handler, err := NewHandler(Config{
		Sessions: store, Audit: auditService, Operations: operations, CMS: cms, CMSWorkspace: workspace, Governance: governanceService, Support: supportService, Reports: &testReportService{}, Clock: func() time.Time { return testNow },
		AllowedOrigins: []string{"https://admin.planext4u.net"}, RequireMFA: requireMFA,
	})
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	return handler, store
}

type testReportService struct{}

func (service *testReportService) List(_ context.Context, principal Principal, _ ReportQuery) (ReportPage, error) {
	return ReportPage{Items: []ReportSummary{{ID: "social-active", Title: "Socio active", Domain: "social", Metric: "active", Value: 5600, Unit: "count", Freshness: testNow, Masked: true, ExportPolicy: "MFA_AND_AUDIT_REQUIRED"}}}, nil
}

func (service *testReportService) Detail(_ context.Context, principal Principal, reportID string, _ ReportQuery) (ReportDetail, error) {
	if reportID != "social-active" || principal.SelectedCountry != "IN" {
		return ReportDetail{}, ErrReportNotFound
	}
	report := ReportSummary{ID: reportID, Title: "Socio active", Domain: "social", Metric: "active", Value: 5600, Unit: "count", Freshness: testNow, Masked: true, ExportPolicy: "MFA_AND_AUDIT_REQUIRED"}
	return ReportDetail{Report: report, Country: principal.SelectedCountry, Items: []ReportRow{{Label: report.Title, Dimensions: map[string]string{"country": principal.SelectedCountry}, Value: 5600, Unit: "count"}}, Lineage: ReportLineage{SourceProjection: "governance.report_cards", Aggregation: "country_aggregate", Freshness: testNow, GeneratedAt: testNow}}, nil
}

func (service *testReportService) CreateExport(_ context.Context, _ Principal, reportID string, _ ReportQuery, _ string) (ReportExport, error) {
	return ReportExport{ID: "report-export-0123456789abcdef0123456789abcdef", ReportID: reportID, Format: "CSV", Status: "READY", ContentType: "text/csv; charset=utf-8", FileName: "social-active-in.csv", ChecksumSHA256: strings.Repeat("a", 64), SizeBytes: 32, DownloadURL: "/admin/api/v1/report-exports/report-export-0123456789abcdef0123456789abcdef/download?token=valid", ExpiresAt: testNow.Add(10 * time.Minute), CreatedAt: testNow}, nil
}

func (service *testReportService) Export(ctx context.Context, principal Principal, exportID string) (ReportExport, error) {
	return service.CreateExport(ctx, principal, "social-active", ReportQuery{}, exportID)
}

func (service *testReportService) Download(_ context.Context, _ Principal, _ string, token string) (ReportArtifact, error) {
	if token != "valid" {
		return ReportArtifact{}, ErrExportToken
	}
	return ReportArtifact{Bytes: []byte("report_id,value\nsocial-active,5600\n"), ContentType: "text/csv; charset=utf-8", FileName: "social-active-in.csv"}, nil
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
