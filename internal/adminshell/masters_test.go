package adminshell

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/organization"
)

type masterStub struct {
	actor organization.Actor
	calls int
}

func (stub *masterStub) Masters(_ context.Context, a organization.Actor, _ organization.MasterFilter) (organization.MasterPage, error) {
	stub.actor = a
	stub.calls++
	return organization.MasterPage{Items: []organization.Master{}}, nil
}
func (stub *masterStub) SaveMaster(_ context.Context, a organization.Actor, v organization.MasterChange) (organization.Master, error) {
	stub.actor = a
	stub.calls++
	return v.Master, nil
}
func (stub *masterStub) MasterHistory(_ context.Context, a organization.Actor, _ string, _ int64) (organization.MasterHistory, error) {
	stub.actor = a
	stub.calls++
	return organization.MasterHistory{Items: []organization.MasterVersion{}}, nil
}
func TestMasterHTTPAuthorityInputCSRFAndRoles(t *testing.T) {
	store, _ := NewMemorySessionStore(func() time.Time { return testNow })
	stub := &masterStub{}
	handler := &Handler{sessions: store, masters: stub, clock: func() time.Time { return testNow }, allowedOrigins: map[string]bool{"https://admin.planext4u.net": true}, requireMFA: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/v1/masters", handler.listMasters)
	mux.HandleFunc("PUT /admin/api/v1/masters/{master_id}", handler.saveMaster)
	mux.HandleFunc("GET /admin/api/v1/masters/{master_id}/history", handler.masterHistory)
	principal := testPrincipal(RoleCountryAdmin)
	principal.OperatorRevision = 9
	token := issue(t, store, principal)
	csrf := resolveCSRF(t, store, token)
	value := organization.MasterChange{Master: organization.Master{ID: "00000000-0000-4000-8000-000000000123", Kind: "OCCUPATION", Code: "TEACHER", NameEN: "Teacher", NameTA: "ஆசிரியர்", Active: true}, Reason: "Approved new occupation"}
	raw, _ := json.Marshal(value)
	path := "/admin/api/v1/masters/" + value.ID
	for _, item := range []struct {
		method, path, body, token, csrf string
		status                          int
	}{
		{"GET", "/admin/api/v1/masters?kind=OCCUPATION", "", "", "", 401},
		{"GET", "/admin/api/v1/masters?kind=OCCUPATION&country=US", "", token, "", 422},
		{"GET", "/admin/api/v1/masters?kind=OCCUPATION&kind=REASON", "", token, "", 422},
		{"PUT", path, string(raw), token, "invalid", 403},
		{"PUT", path, strings.TrimSuffix(string(raw), "}") + `,"tenant_id":"forged"}`, token, csrf, 422},
		{"PUT", path + "?country=US", string(raw), token, csrf, 422},
		{"GET", path + "/history?before=bad", "", token, "", 422},
		{"GET", path + "/history?tenant_id=forged", "", token, "", 422},
	} {
		response := serve(mux, item.method, item.path, []byte(item.body), item.token, item.csrf, "https://admin.planext4u.net")
		if response.Code != item.status {
			t.Fatalf("%s %s: %d %s", item.method, item.path, response.Code, response.Body.String())
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid requests reached owner")
	}
	response := serve(mux, "GET", "/admin/api/v1/masters?kind=OCCUPATION", nil, token, "", "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response = serve(mux, "PUT", path, raw, token, csrf, "https://admin.planext4u.net")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if stub.actor.TenantID != principal.TenantID || stub.actor.Country != principal.SelectedCountry || stub.actor.Subject != principal.SubjectID || stub.actor.OperatorSessionID != principal.SessionID || stub.actor.OperatorRevision != 9 || !stub.actor.RequireLiveOperator {
		t.Fatalf("owner authority: %+v", stub.actor)
	}
	for _, role := range []Role{RoleRegionalStaff, RoleContentAdmin, RoleSupportAdmin, RoleAuditor} {
		roleToken := issue(t, store, testPrincipal(role))
		roleCSRF := resolveCSRF(t, store, roleToken)
		response = serve(mux, "PUT", path, raw, roleToken, roleCSRF, "https://admin.planext4u.net")
		if response.Code != 403 {
			t.Fatalf("role %s write: %d", role, response.Code)
		}
		response = serve(mux, "GET", "/admin/api/v1/masters?kind=OCCUPATION", nil, roleToken, "", "")
		expected := 403
		if role == RoleAuditor {
			expected = 200
		}
		if response.Code != expected {
			t.Fatalf("role %s read: %d", role, response.Code)
		}
	}
	old := principal
	old.AuthenticatedAt = testNow.Add(-10 * time.Minute)
	oldToken := issue(t, store, old)
	response = serve(mux, "PUT", path, raw, oldToken, resolveCSRF(t, store, oldToken), "https://admin.planext4u.net")
	if response.Code != 403 {
		t.Fatalf("stale auth write: %d", response.Code)
	}
	response = serve(mux, "GET", "/admin/api/v1/masters?kind=OCCUPATION", nil, oldToken, "", "")
	if response.Code != 200 {
		t.Fatalf("nonfresh read: %d", response.Code)
	}
}
