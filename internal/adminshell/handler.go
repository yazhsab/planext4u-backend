package adminshell

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
	"github.com/yazhsab/planext4u-backend/internal/audit"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/governance"
	"github.com/yazhsab/planext4u-backend/internal/support"
)

type AuditReader interface {
	Search(context.Context, audit.Principal, audit.SearchFilter) (audit.Page, error)
}

type OperationsService interface {
	Submit(adminops.Principal, adminops.Command) (adminops.Change, error)
	Approve(adminops.Principal, string, int64) (adminops.Change, error)
	Reject(adminops.Principal, string, int64, string) (adminops.Change, error)
	List(adminops.Principal) ([]adminops.Change, error)
	Audit(adminops.Principal) ([]adminops.AuditEvent, error)
}

type CMSAuthoringService interface {
	List(context.Context, string, string) ([]configcms.PageDraft, error)
	Save(context.Context, string, string, string, configcms.Page, int64) (configcms.PageDraft, error)
}

type CMSWorkspaceAuthoringService interface {
	Get(context.Context, string, string) (configcms.WorkspaceDraft, error)
	Save(context.Context, string, string, string, configcms.Workspace, int64) (configcms.WorkspaceDraft, error)
}

type GovernanceService interface {
	Dashboard(governance.Actor) (governance.Dashboard, error)
}

type SupportReader interface {
	AdminList(context.Context, support.AdminPrincipal, support.AdminListFilter) (support.AdminTicketPage, error)
}

type Config struct {
	Sessions       SessionResolver
	Audit          AuditReader
	Operations     OperationsService
	CMS            CMSAuthoringService
	CMSWorkspace   CMSWorkspaceAuthoringService
	Governance     GovernanceService
	Support        SupportReader
	Reports        ReportService
	Clock          func() time.Time
	AllowedOrigins []string
	RequireMFA     bool
}

type Handler struct {
	sessions          SessionResolver
	audit             AuditReader
	operations        OperationsService
	cms               CMSAuthoringService
	cmsWorkspace      CMSWorkspaceAuthoringService
	governanceService GovernanceService
	supportReader     SupportReader
	reports           ReportService
	clock             func() time.Time
	allowedOrigins    map[string]bool
	requireMFA        bool
}

func NewHandler(config Config) (http.Handler, error) {
	if config.Sessions == nil || config.Audit == nil || config.Operations == nil || config.Governance == nil || config.Clock == nil || len(config.AllowedOrigins) == 0 {
		return nil, ErrInvalidRequest
	}
	origins := make(map[string]bool, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, ErrInvalidRequest
		}
		origins[origin] = true
	}
	handler := &Handler{sessions: config.Sessions, audit: config.Audit, operations: config.Operations, cms: config.CMS, cmsWorkspace: config.CMSWorkspace, governanceService: config.Governance, supportReader: config.Support, reports: config.Reports, clock: config.Clock, allowedOrigins: origins, requireMFA: config.RequireMFA}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/v1/session", handler.session)
	mux.HandleFunc("DELETE /admin/api/v1/session", handler.logout)
	mux.HandleFunc("PUT /admin/api/v1/session/country", handler.updateCountry)
	mux.HandleFunc("GET /admin/api/v1/audit/events", handler.auditEvents)
	mux.HandleFunc("GET /admin/api/v1/operations", handler.listOperations)
	mux.HandleFunc("POST /admin/api/v1/operations", handler.submitOperation)
	mux.HandleFunc("GET /admin/api/v1/operations/audit", handler.operationsAudit)
	mux.HandleFunc("GET /admin/api/v1/governance", handler.governance)
	mux.HandleFunc("POST /admin/api/v1/operations/{change_id}/approve", handler.approveOperation)
	mux.HandleFunc("POST /admin/api/v1/operations/{change_id}/reject", handler.rejectOperation)
	if handler.cms != nil {
		mux.HandleFunc("GET /admin/api/v1/cms/pages", handler.listCMSPages)
		mux.HandleFunc("PUT /admin/api/v1/cms/pages/{page_id}/draft", handler.saveCMSPageDraft)
	}
	if handler.cmsWorkspace != nil {
		mux.HandleFunc("GET /admin/api/v1/cms/workspace", handler.getCMSWorkspace)
		mux.HandleFunc("PUT /admin/api/v1/cms/workspace/draft", handler.saveCMSWorkspaceDraft)
	}
	if handler.supportReader != nil {
		mux.HandleFunc("GET /admin/api/v1/support/tickets", handler.supportTickets)
	}
	if handler.reports != nil {
		mux.HandleFunc("GET /admin/api/v1/reports", handler.listReports)
		mux.HandleFunc("GET /admin/api/v1/reports/{report_id}", handler.reportDetail)
		mux.HandleFunc("POST /admin/api/v1/reports/{report_id}/exports", handler.createReportExport)
		mux.HandleFunc("GET /admin/api/v1/report-exports/{export_id}", handler.reportExport)
		mux.HandleFunc("GET /admin/api/v1/report-exports/{export_id}/download", handler.downloadReportExport)
	}
	return securityHeaders(mux), nil
}

func (handler *Handler) logout(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityShellRead)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	if err := handler.sessions.Revoke(request.Context(), session.Principal.SessionID); err != nil && !errors.Is(err, ErrSessionNotFound) {
		writeProblem(writer, request, http.StatusServiceUnavailable, "ADMIN_SESSION_REVOKE_FAILED", "The administrator session could not be closed. Try again.")
		return
	}
	ClearSessionCookie(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) session(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, CapabilityShellRead)
	if !ok {
		return
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	view := SessionView{
		SubjectID:        session.Principal.SubjectID,
		DisplayName:      session.Principal.DisplayName,
		Roles:            append([]Role(nil), session.Principal.Roles...),
		Capabilities:     capabilityList(capabilities),
		AllowedCountries: append([]string(nil), session.Principal.AllowedCountries...),
		SelectedCountry:  session.Principal.SelectedCountry,
		Assurance:        assurance,
		Navigation:       navigation(capabilities),
		CSRFToken:        session.CSRFToken,
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) governance(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityGovernanceRead)
	if !ok {
		return
	}
	roles := make([]string, 0, len(session.Principal.Roles))
	for _, role := range session.Principal.Roles {
		roles = append(roles, string(role))
	}
	dashboard, err := handler.governanceService.Dashboard(governance.Actor{TenantID: session.Principal.TenantID, Country: session.Principal.SelectedCountry, Subject: session.Principal.SubjectID, Roles: roles, MFAVerified: true})
	if err != nil {
		if errors.Is(err, governance.ErrForbidden) || errors.Is(err, governance.ErrMFARequired) {
			writeProblem(writer, request, http.StatusForbidden, "ADMIN_GOVERNANCE_FORBIDDEN", "This governance workspace is not available.")
			return
		}
		writeProblem(writer, request, http.StatusServiceUnavailable, "ADMIN_GOVERNANCE_UNAVAILABLE", "Governance data is temporarily unavailable.")
		return
	}
	view := GovernanceView{Country: session.Principal.SelectedCountry, FeatureFlags: map[string]bool{}, Metrics: []GovernanceMetric{}, PrivacyMode: dashboard.PrivacyMode, GeneratedAt: dashboard.GeneratedAt}
	for _, country := range dashboard.Countries {
		if country.Country == session.Principal.SelectedCountry {
			view.PolicyVersion, view.FeatureFlags = country.PolicyVersion, cloneGovernanceFlags(country.FeatureFlags)
			break
		}
	}
	for _, report := range dashboard.Reports {
		view.Metrics = append(view.Metrics, GovernanceMetric{ID: report.ID, Title: report.Title, Value: report.Value, Unit: report.Unit, Freshness: report.Freshness, Masked: report.Masked})
	}
	writeJSON(writer, http.StatusOK, view)
}

func cloneGovernanceFlags(value map[string]bool) map[string]bool {
	result := make(map[string]bool, len(value))
	for key, enabled := range value {
		result[key] = enabled
	}
	return result
}

func (handler *Handler) updateCountry(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityShellRead)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	var input struct {
		Country string `json:"country"`
	}
	if !decodeJSON(request, &input) || !validCountry(input.Country) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_COUNTRY_INVALID", "Choose an available country.")
		return
	}
	if err := handler.sessions.SetCountry(request.Context(), session.Principal.SessionID, input.Country); err != nil {
		if errors.Is(err, ErrForbidden) {
			writeProblem(writer, request, http.StatusForbidden, "ADMIN_COUNTRY_FORBIDDEN", "You do not have access to that country.")
			return
		}
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_COUNTRY_INVALID", "Choose an available country.")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) auditEvents(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityAuditRead)
	if !ok {
		return
	}
	filter, err := auditFilter(request, session.Principal.SelectedCountry)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_AUDIT_FILTER_INVALID", "Check the audit filters and try again.")
		return
	}
	page, err := handler.audit.Search(request.Context(), audit.Principal{
		TenantID:     session.Principal.TenantID,
		SubjectID:    session.Principal.SubjectID,
		Capabilities: map[string]bool{"audit.read": true},
	}, filter)
	if err != nil {
		if errors.Is(err, audit.ErrForbidden) {
			writeProblem(writer, request, http.StatusForbidden, "ADMIN_AUDIT_FORBIDDEN", "Audit events are not available for this role.")
			return
		}
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_AUDIT_FILTER_INVALID", "Check the audit filters and try again.")
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) authorize(writer http.ResponseWriter, request *http.Request, capability string) (ResolvedSession, map[string]bool, bool) {
	session, err := handler.sessions.Resolve(request)
	if err != nil || !validPrincipal(session.Principal) || session.CSRFToken == "" {
		writeProblem(writer, request, http.StatusUnauthorized, "ADMIN_AUTHENTICATION_REQUIRED", "Sign in with an administrator account.")
		return ResolvedSession{}, nil, false
	}
	capabilities := capabilities(session.Principal.Roles)
	if !capabilities[CapabilityShellRead] || !capabilities[capability] {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_ROLE_FORBIDDEN", "This administrator role cannot access the requested area.")
		return ResolvedSession{}, nil, false
	}
	if handler.requireMFA && !assuranceFor(session.Principal, handler.clock()).MFASatisfied {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_MFA_REQUIRED", "Complete multi-factor authentication to continue.")
		return ResolvedSession{}, nil, false
	}
	return session, capabilities, true
}

func (handler *Handler) csrfAllowed(request *http.Request, expected string) bool {
	if request.Header.Get("Sec-Fetch-Site") == "cross-site" || !handler.allowedOrigins[request.Header.Get("Origin")] {
		return false
	}
	provided := request.Header.Get("X-CSRF-Token")
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func assuranceFor(principal Principal, now time.Time) Assurance {
	mfa := false
	for _, method := range principal.AuthMethods {
		switch strings.ToLower(method) {
		case "mfa", "otp", "totp", "webauthn":
			mfa = true
		}
	}
	age := now.UTC().Sub(principal.AuthenticatedAt.UTC())
	return Assurance{MFASatisfied: mfa, FreshAuth: age >= -time.Minute && age <= 5*time.Minute, AuthTime: principal.AuthenticatedAt.UTC()}
}

func navigation(capabilities map[string]bool) []NavigationItem {
	items := []NavigationItem{{ID: "workspace", Label: "Workspace", Path: "/", Capability: CapabilityShellRead}}
	if capabilities[CapabilityOperationsRead] {
		items = append(items, NavigationItem{ID: "operations", Label: "Operations", Path: "/operations", Capability: CapabilityOperationsRead})
	}
	if capabilities[CapabilityGovernanceRead] {
		items = append(items, NavigationItem{ID: "governance", Label: "Governance", Path: "/governance", Capability: CapabilityGovernanceRead})
	}
	if capabilities[CapabilitySupportManage] {
		items = append(items, NavigationItem{ID: "support", Label: "Support", Path: "/support", Capability: CapabilitySupportManage})
	}
	if capabilities[CapabilityConfigManage] {
		items = append(items, NavigationItem{ID: "cms", Label: "Page builder", Path: "/cms", Capability: CapabilityConfigManage})
	}
	if capabilities[CapabilityAuditRead] {
		items = append(items, NavigationItem{ID: "audit", Label: "Audit trail", Path: "/audit", Capability: CapabilityAuditRead})
	}
	return items
}

func (handler *Handler) supportTickets(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilitySupportManage)
	if !ok {
		return
	}
	limit, err := strconv.Atoi(defaultString(request.URL.Query().Get("limit"), "50"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_SUPPORT_FILTER_INVALID", "Check the support filters and try again.")
		return
	}
	page, err := handler.supportReader.AdminList(request.Context(), support.AdminPrincipal{
		TenantID: session.Principal.TenantID, Country: session.Principal.SelectedCountry,
		Subject: session.Principal.SubjectID, Capabilities: map[string]bool{support.CapabilitySupportManage: true},
	}, support.AdminListFilter{OwnerRole: support.Role(request.URL.Query().Get("owner_role")), Status: support.Status(request.URL.Query().Get("status")), Limit: limit, Cursor: request.URL.Query().Get("cursor")})
	if err != nil {
		if errors.Is(err, support.ErrForbidden) {
			writeProblem(writer, request, http.StatusForbidden, "ADMIN_SUPPORT_FORBIDDEN", "Support tickets are not available for this role.")
			return
		}
		if errors.Is(err, support.ErrInvalidRequest) {
			writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_SUPPORT_FILTER_INVALID", "Check the support filters and try again.")
			return
		}
		writeProblem(writer, request, http.StatusServiceUnavailable, "ADMIN_SUPPORT_UNAVAILABLE", "Support tickets are temporarily unavailable.")
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func auditFilter(request *http.Request, selectedCountry string) (audit.SearchFilter, error) {
	query := request.URL.Query()
	filter := audit.SearchFilter{Country: selectedCountry, ActorID: query.Get("actor_id"), Action: query.Get("action"), TargetID: query.Get("target_id"), Limit: 50}
	var err error
	if raw := query.Get("limit"); raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil {
			return audit.SearchFilter{}, ErrInvalidRequest
		}
	}
	if filter.After, err = audit.DecodeCursor(query.Get("cursor")); err != nil {
		return audit.SearchFilter{}, ErrInvalidRequest
	}
	if raw := query.Get("from"); raw != "" {
		if filter.From, err = time.Parse(time.RFC3339, raw); err != nil {
			return audit.SearchFilter{}, ErrInvalidRequest
		}
	}
	if raw := query.Get("to"); raw != "" {
		if filter.To, err = time.Parse(time.RFC3339, raw); err != nil {
			return audit.SearchFilter{}, ErrInvalidRequest
		}
	}
	return filter, nil
}

func decodeJSON(request *http.Request, target any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 32*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := strings.TrimSpace(request.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = "unavailable"
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "correlation_id": correlationID, "retryable": false,
		"field_errors": []any{}, "details": map[string]any{},
	}})
}
