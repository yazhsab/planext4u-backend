package adminshell

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperatorInputsDoNotGrantRootOrAcceptAmbiguousChanges(t *testing.T) {
	active := true
	valid := OperatorChange{DisplayName: "தமிழ் நிர்வாகி", Roles: []Role{RoleRegionalStaff}, AllowedCountries: []string{"IN"}, Active: &active, Reason: "Approved staff onboarding"}
	if !validOperatorChange(valid) {
		t.Fatal("valid Unicode display name rejected")
	}
	finance := valid
	finance.Roles = []Role{RoleCountryAdmin, RoleFinance}
	if !validOperatorChange(finance) {
		t.Fatal("paired country finance role rejected")
	}
	if capabilities([]Role{RoleCountryAdmin})[CapabilityParticipantBankVerify] || !capabilities(finance.Roles)[CapabilityParticipantBankVerify] {
		t.Fatal("bank verification capability is not limited to explicit finance role")
	}
	for _, mutate := range []func(*OperatorChange){
		func(v *OperatorChange) { v.Roles = []Role{RoleFinance} },
		func(v *OperatorChange) { v.Roles = []Role{RoleSuperAdmin} },
		func(v *OperatorChange) { v.Roles = []Role{RoleAuditor, RoleAuditor} },
		func(v *OperatorChange) { v.Roles = nil },
		func(v *OperatorChange) { v.AllowedCountries = []string{"IN", "IN"} },
		func(v *OperatorChange) { v.AllowedCountries = []string{"in"} },
		func(v *OperatorChange) { v.Active = nil },
		func(v *OperatorChange) { v.Reason = "short" },
		func(v *OperatorChange) { v.Reason = "Secret\nreason" },
		func(v *OperatorChange) { v.DisplayName = " padded " },
		func(v *OperatorChange) { v.Revision = -1 },
	} {
		input := valid
		mutate(&input)
		if validOperatorChange(input) {
			t.Fatalf("invalid input accepted: %+v", input)
		}
	}
	principal := testPrincipal(RoleSuperAdmin)
	principal.DisplayName = strings.Repeat("ழ", 128)
	if !validPrincipal(principal) {
		t.Fatal("128 Unicode characters rejected by session boundary")
	}
	principal.OperatorRevision = -1
	if validPrincipal(principal) {
		t.Fatal("negative revision accepted")
	}
}

func TestOperatorHTTPGuardsApplyEvenWithoutGlobalMFAEnforcement(t *testing.T) {
	store, _ := NewMemorySessionStore(func() time.Time { return testNow })
	handler := &Handler{sessions: store, clock: func() time.Time { return testNow }, allowedOrigins: map[string]bool{"https://admin.planext4u.net": true}}
	request := func(principal Principal, csrf, origin string) *httptest.ResponseRecorder {
		token := issue(t, store, principal)
		r := requestWithCookie(token)
		r.Method = http.MethodPost
		r.Header.Set("Origin", origin)
		if csrf == "valid" {
			session, _ := store.Resolve(r)
			csrf = session.CSRFToken
		}
		r.Header.Set("X-CSRF-Token", csrf)
		out := httptest.NewRecorder()
		if _, ok := handler.operatorRequest(out, r, true); ok {
			out.WriteHeader(204)
		}
		return out
	}
	principal := testPrincipal(RoleCountryAdmin)
	assertStatusAndCode(t, request(principal, "valid", "https://admin.planext4u.net"), 403, "ADMIN_ROLE_FORBIDDEN")
	principal = testPrincipal(RoleSuperAdmin)
	principal.AuthMethods = []string{"password"}
	assertStatusAndCode(t, request(principal, "valid", "https://admin.planext4u.net"), 403, "ADMIN_MFA_REQUIRED")
	principal = testPrincipal(RoleSuperAdmin)
	principal.AuthenticatedAt = testNow.Add(-6 * time.Minute)
	assertStatusAndCode(t, request(principal, "valid", "https://admin.planext4u.net"), 403, "ADMIN_FRESH_AUTH_REQUIRED")
	principal = testPrincipal(RoleSuperAdmin)
	assertStatusAndCode(t, request(principal, "bad", "https://admin.planext4u.net"), 403, "ADMIN_FRESH_AUTH_REQUIRED")
	assertStatusAndCode(t, request(principal, "valid", "https://attacker.test"), 403, "ADMIN_FRESH_AUTH_REQUIRED")
	if request(principal, "valid", "https://admin.planext4u.net").Code != 204 {
		t.Fatal("authorized mutation rejected")
	}
	for err, status := range map[error]int{ErrForbidden: 403, ErrInvalidRequest: 422, ErrOperatorNotFound: 404, ErrOperatorConflict: 409, errors.New("private database diagnostic"): 503} {
		out := httptest.NewRecorder()
		handler.operatorError(out, httptest.NewRequest("GET", "/", nil), err)
		if out.Code != status || strings.Contains(out.Body.String(), "private database diagnostic") {
			t.Fatalf("unsafe error: %s", out.Body.String())
		}
	}
	accounts := &OperatorAccounts{}
	if _, err := accounts.List(context.Background(), principal, "", "bad-cursor", testNow); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid cursor: %v", err)
	}
	if _, err := accounts.Events(context.Background(), principal, "bad-id", 0, testNow); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid history: %v", err)
	}
}
