package adminshell

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yazhsab/planext4u-backend/internal/adminops"
	"github.com/yazhsab/planext4u-backend/internal/audit"
	"github.com/yazhsab/planext4u-backend/internal/governance"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

type privacyGovernanceFixture struct {
	calls    int
	actor    identity.DeletionGovernor
	proposal identity.DeletionPolicyProposal
	err      error
}

func (f *privacyGovernanceFixture) ProposeDeletionPolicy(_ context.Context, a identity.DeletionGovernor, p identity.DeletionPolicyProposal) (string, error) {
	f.calls++
	f.actor = a
	f.proposal = p
	return p.ID, f.err
}
func (f *privacyGovernanceFixture) PublishDeletionPolicy(_ context.Context, a identity.DeletionGovernor, _ string) (int64, error) {
	f.calls++
	f.actor = a
	return 2, f.err
}
func (f *privacyGovernanceFixture) PlaceDeletionHold(_ context.Context, a identity.DeletionGovernor, _, _, _ string) (int64, error) {
	f.calls++
	f.actor = a
	return 17, f.err
}
func (f *privacyGovernanceFixture) ReleaseDeletionHold(_ context.Context, a identity.DeletionGovernor, _, _ int64, _ string) (int64, error) {
	f.calls++
	f.actor = a
	return 2, f.err
}
func (f *privacyGovernanceFixture) ListDeletionPolicyProposals(_ context.Context, a identity.DeletionGovernor) ([]identity.DeletionPolicyReview, error) {
	f.calls++
	f.actor = a
	return []identity.DeletionPolicyReview{{ID: "50000000-0000-4000-8000-000000000001", Owner: "supply", Status: "PROPOSED"}}, f.err
}
func (f *privacyGovernanceFixture) GetDeletionPolicyProposal(_ context.Context, a identity.DeletionGovernor, id string) (identity.DeletionPolicyReview, error) {
	f.calls++
	f.actor = a
	return identity.DeletionPolicyReview{ID: id, Owner: "supply", Status: "PROPOSED"}, f.err
}
func (f *privacyGovernanceFixture) ListDeletionRequests(_ context.Context, a identity.DeletionGovernor) ([]identity.DeletionRequestReview, error) {
	f.calls++
	f.actor = a
	return []identity.DeletionRequestReview{{ID: "deletion-00000001", Status: "SCHEDULED"}}, f.err
}
func (f *privacyGovernanceFixture) GetDeletionRequest(_ context.Context, a identity.DeletionGovernor, id string) (identity.DeletionRequestReview, error) {
	f.calls++
	f.actor = a
	return identity.DeletionRequestReview{ID: id, Status: "SCHEDULED"}, f.err
}
func (f *privacyGovernanceFixture) ListDeletionHolds(_ context.Context, a identity.DeletionGovernor) ([]identity.DeletionHoldReview, error) {
	f.calls++
	f.actor = a
	return []identity.DeletionHoldReview{{ID: 17, RequestID: "deletion-00000001", Owner: "supply", Revision: 1}}, f.err
}

type privacyAuditFixture struct{}

func (privacyAuditFixture) Search(context.Context, audit.Principal, audit.SearchFilter) (audit.Page, error) {
	return audit.Page{}, nil
}

type privacyDashboardFixture struct{}

func (privacyDashboardFixture) Dashboard(governance.Actor) (governance.Dashboard, error) {
	return governance.Dashboard{}, nil
}

func privacyHandler(t *testing.T, fixture *privacyGovernanceFixture) (http.Handler, *MemorySessionStore) {
	t.Helper()
	store, err := NewMemorySessionStore(func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	operations, err := adminops.NewService(func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(Config{Sessions: store, Audit: privacyAuditFixture{}, Operations: operations, Governance: privacyDashboardFixture{}, PrivacyGovernance: fixture, Clock: func() time.Time { return testNow }, AllowedOrigins: []string{"https://admin.planext4u.net"}, RequireMFA: true})
	if err != nil {
		t.Fatal(err)
	}
	return handler, store
}

func privacyPrincipal(role Role) Principal {
	p := testPrincipal(role)
	p.TenantID = "afc1e0db-73cf-40b3-9927-33590133da0b"
	p.SubjectID = "10000000-0000-4000-8000-000000000001"
	p.SessionID = "40000000-0000-4000-8000-000000000001"
	p.OperatorRevision = 7
	return p
}

func TestPrivacyGovernanceBFFRequiresFreshCountryAuthorityAndStrictCommands(t *testing.T) {
	fixture := &privacyGovernanceFixture{}
	handler, store := privacyHandler(t, fixture)
	path := "/admin/api/v1/privacy/retention-proposals"
	body := []byte(`{"id":"50000000-0000-4000-8000-000000000001","owner":"supply","policy_version":"retention-v1","retention_seconds":86400,"expected_revision":0,"reason_reference":"approved-schedule"}`)
	unauthenticated := serve(handler, "POST", path, body, "", "", "https://admin.planext4u.net")
	assertStatusAndCode(t, unauthenticated, 401, "ADMIN_AUTHENTICATION_REQUIRED")
	content := issue(t, store, privacyPrincipal(RoleContentAdmin))
	forbidden := serve(handler, "POST", path, body, content, resolveCSRF(t, store, content), "https://admin.planext4u.net")
	assertStatusAndCode(t, forbidden, 403, "ADMIN_PRIVACY_GOVERNANCE_FORBIDDEN")
	stale := privacyPrincipal(RoleCountryAdmin)
	stale.SessionID = "40000000-0000-4000-8000-000000000002"
	stale.AuthenticatedAt = testNow.Add(-10 * time.Minute)
	staleToken := issue(t, store, stale)
	assertStatusAndCode(t, serve(handler, "POST", path, body, staleToken, resolveCSRF(t, store, staleToken), "https://admin.planext4u.net"), 403, "ADMIN_PRIVACY_GOVERNANCE_FORBIDDEN")
	admin := issue(t, store, privacyPrincipal(RoleCountryAdmin))
	csrf := resolveCSRF(t, store, admin)
	assertStatusAndCode(t, serve(handler, "POST", path, body, admin, csrf, "https://attacker.example"), 403, "ADMIN_PRIVACY_GOVERNANCE_FORBIDDEN")
	assertStatusAndCode(t, serve(handler, "POST", path, []byte(`{"id":"50000000-0000-4000-8000-000000000001","owner":"supply","policy_version":"retention-v1","retention_seconds":86400,"expected_revision":0,"reason_reference":"approved-schedule","asset_id":"private"}`), admin, csrf, "https://admin.planext4u.net"), 422, "ADMIN_PRIVACY_COMMAND_INVALID")
	if fixture.calls != 0 {
		t.Fatalf("untrusted command reached identity owner %d times", fixture.calls)
	}
	proposed := serve(handler, "POST", path, body, admin, csrf, "https://admin.planext4u.net")
	if proposed.Code != 201 || !strings.Contains(proposed.Body.String(), `"status":"PROPOSED"`) || fixture.calls != 1 || fixture.actor.Country != "IN" || fixture.actor.OperatorRevision != 7 || fixture.proposal.Retention != 24*time.Hour {
		t.Fatalf("bad scoped proposal: %d %s %#v %#v", proposed.Code, proposed.Body.String(), fixture.actor, fixture.proposal)
	}
	fixture.err = &pgconn.PgError{Code: "P0001"}
	conflict := serve(handler, "POST", path, body, admin, csrf, "https://admin.planext4u.net")
	assertStatusAndCode(t, conflict, 409, "ADMIN_PRIVACY_GOVERNANCE_CONFLICT")
	fixture.err = nil
	publication := serve(handler, "POST", path+"/50000000-0000-4000-8000-000000000001/publish", nil, admin, csrf, "https://admin.planext4u.net")
	if publication.Code != 200 || !strings.Contains(publication.Body.String(), `"revision":2`) {
		t.Fatal(publication.Code, publication.Body.String())
	}
	hold := serve(handler, "POST", "/admin/api/v1/privacy/deletions/deletion-00000001/holds", []byte(`{"owner":"supply","reason_reference":"legal-reference-123"}`), admin, csrf, "https://admin.planext4u.net")
	if hold.Code != 201 || !strings.Contains(hold.Body.String(), `"id":17`) {
		t.Fatal(hold.Code, hold.Body.String())
	}
	release := serve(handler, "POST", "/admin/api/v1/privacy/holds/17/release", []byte(`{"expected_revision":1,"reason_reference":"release-reference-123"}`), admin, csrf, "https://admin.planext4u.net")
	if release.Code != 200 || !strings.Contains(release.Body.String(), `"status":"RELEASED"`) {
		t.Fatal(release.Code, release.Body.String())
	}
	listed := serve(handler, "GET", "/admin/api/v1/privacy/retention-proposals", nil, admin, "", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"status":"PROPOSED"`) || listed.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(listed.Code, listed.Body.String())
	}
	deletions := serve(handler, "GET", "/admin/api/v1/privacy/deletions", nil, admin, "", "")
	if deletions.Code != 200 || !strings.Contains(deletions.Body.String(), `"status":"SCHEDULED"`) {
		t.Fatal(deletions.Code, deletions.Body.String())
	}
	holds := serve(handler, "GET", "/admin/api/v1/privacy/holds", nil, admin, "", "")
	if holds.Code != 200 || !strings.Contains(holds.Body.String(), `"request_id":"deletion-00000001"`) {
		t.Fatal(holds.Code, holds.Body.String())
	}
}
