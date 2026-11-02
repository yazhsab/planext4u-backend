//go:build integration

package organization

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPersistentHierarchyIsolationDelegationRevocationAndInvitations(t *testing.T) {
	url := os.Getenv("ADMIN_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("ADMIN_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS admin CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS admin CASCADE`) })
	for _, file := range []string{"platform/000001_service_roles", "platform/000008_admin_roles", "admin/000001_admin_control_plane", "admin/000002_report_exports", "admin/000003_organization", "admin/000004_operator_accounts"} {
		body, readErr := os.ReadFile("../../migrations/" + file + ".up.sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	service, _ := NewService(pool, func() time.Time { return now })
	admin := Actor{TenantID: uuid.NewString(), Country: "IN", Subject: uuid.NewString(), PlatformAdmin: true, MFA: true}
	other := admin
	other.TenantID = uuid.NewString()
	root, err := service.CreateNode(ctx, admin, Node{Name: "India operations", Kind: "ORGANIZATION"})
	if err != nil {
		t.Fatal(err)
	}
	north, err := service.CreateNode(ctx, admin, Node{ParentID: root.ID, Name: "North territory", Kind: "TERRITORY"})
	if err != nil {
		t.Fatal(err)
	}
	south, err := service.CreateNode(ctx, admin, Node{ParentID: root.ID, Name: "South territory", Kind: "TERRITORY"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateNode(ctx, admin, Node{ParentID: north.ID, Name: "North district", Kind: "TERRITORY"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := service.CreateNode(ctx, other, Node{Name: "Other tenant", Kind: "ORGANIZATION"})
	if err != nil {
		t.Fatal(err)
	}
	all := []string{Read, Manage, StaffManage, ResourceRead, ResourceExport, ResourceCommand}
	managerRole, err := service.CreateRole(ctx, admin, JobRole{Name: "Territory manager", Capabilities: all}, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	readerRole, err := service.CreateRole(ctx, admin, JobRole{Name: "Viewer", Capabilities: []string{Read, ResourceRead}}, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager := admin
	manager.Subject = uuid.NewString()
	manager.PlatformAdmin = false
	assignment, err := service.SetAssignment(ctx, admin, Assignment{Subject: manager.Subject, NodeID: north.ID, RoleID: managerRole.ID, Status: "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{north.ID, child.ID} {
		if err = service.Authorize(ctx, manager, id, ResourceCommand); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{south.ID, root.ID, foreign.ID} {
		if err = service.Authorize(ctx, manager, id, ResourceRead); !errors.Is(err, ErrForbidden) {
			t.Fatalf("scope %s allowed: %v", id, err)
		}
	}
	if _, err = service.CreateNode(ctx, manager, Node{ParentID: south.ID, Name: "Unauthorized child", Kind: "TERRITORY"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = service.CreateNode(ctx, other, Node{ParentID: north.ID, Name: "Cross tenant child", Kind: "TERRITORY"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	page, err := service.Workspace(ctx, manager, "")
	if err != nil || len(page.Nodes) != 2 || len(page.Staff) != 1 {
		t.Fatalf("workspace=%+v err=%v", page, err)
	}
	page, err = service.Workspace(ctx, manager, "South")
	if err != nil || len(page.Nodes) != 0 {
		t.Fatalf("search leaked south: %+v %v", page, err)
	}
	viewer := manager
	viewer.Subject = uuid.NewString()
	if _, err = service.SetAssignment(ctx, manager, Assignment{Subject: viewer.Subject, NodeID: child.ID, RoleID: readerRole.ID, Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if err = service.Authorize(ctx, viewer, child.ID, ResourceRead); err != nil {
		t.Fatal(err)
	}
	if err = service.Authorize(ctx, viewer, child.ID, ResourceExport); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer exported: %v", err)
	}
	if _, err = service.CreateRole(ctx, viewer, JobRole{Name: "Self elevated", Capabilities: all}, child.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	self := assignment
	self.Status = "SUSPENDED"
	if _, err = service.SetAssignment(ctx, manager, self); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self assignment mutation: %v", err)
	}
	invitee := manager
	invitee.Subject = uuid.NewString()
	invitation, err := service.Invite(ctx, manager, invitee.Subject, child.ID, readerRole.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.AcceptInvitation(ctx, viewer, invitation.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invitation stolen: %v", err)
	}
	if err = service.AcceptInvitation(ctx, invitee, invitation.Token); err != nil {
		t.Fatal(err)
	}
	if err = service.AcceptInvitation(ctx, invitee, invitation.Token); !errors.Is(err, ErrConflict) {
		t.Fatalf("invitation replay: %v", err)
	}
	if err = service.Authorize(ctx, invitee, child.ID, ResourceRead); err != nil {
		t.Fatal(err)
	}
	newInvitee := invitee
	newInvitee.Subject = uuid.NewString()
	invitation, err = service.Invite(ctx, manager, newInvitee.Subject, child.ID, readerRole.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	assignment.Status = "SUSPENDED"
	assignment, err = service.SetAssignment(ctx, admin, assignment)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Authorize(ctx, manager, child.ID, ResourceRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended staff retained authority: %v", err)
	}
	if err = service.AcceptInvitation(ctx, newInvitee, invitation.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked inviter retained authority: %v", err)
	}
	stale := assignment
	stale.Revision = 1
	stale.Status = "ACTIVE"
	if _, err = service.SetAssignment(ctx, admin, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale staff accepted: %v", err)
	}
	if err = service.SetNodeActive(ctx, admin, north.ID, 1, false); err != nil {
		t.Fatal(err)
	}
	if err = service.Authorize(ctx, viewer, child.ID, ResourceRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inactive ancestor bypassed: %v", err)
	}
	page, err = service.Workspace(ctx, admin, "North")
	if err != nil || len(page.Nodes) != 2 {
		t.Fatalf("operator cannot inspect disabled subtree: %+v %v", page, err)
	}
	if err = service.Authorize(ctx, admin, north.ID, ResourceExport); err != nil {
		t.Fatal(err)
	}
	if err = service.Authorize(ctx, admin, north.ID, ResourceCommand); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled business command: %v", err)
	}
	if err = service.SetNodeActive(ctx, viewer, north.ID, 2, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("staff reactivated territory: %v", err)
	}
	if err = service.SetNodeActive(ctx, other, north.ID, 2, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign territory modified: %v", err)
	}
	if err = service.SetNodeActive(ctx, admin, north.ID, 1, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale status accepted: %v", err)
	}
	if err = service.SetNodeActive(ctx, admin, north.ID, 2, true); err != nil {
		t.Fatal(err)
	}
	if err = service.Authorize(ctx, viewer, child.ID, ResourceRead); err != nil {
		t.Fatal(err)
	}
	service, _ = NewService(pool, func() time.Time { return now })
	if err = service.Authorize(ctx, viewer, child.ID, ResourceRead); err != nil {
		t.Fatalf("restart lost assignment: %v", err)
	}
	var rawTokens int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM admin.staff_invitations WHERE token_digest=$1`, invitation.Token).Scan(&rawTokens); err != nil || rawTokens != 0 {
		t.Fatalf("plaintext invitation persisted: %d %v", rawTokens, err)
	}
}
