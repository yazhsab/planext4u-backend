//go:build integration

package organization

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
)

func TestPersistentDynamicAudienceMembershipIsolationAndHistory(t *testing.T) {
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
	for _, file := range []string{"platform/000001_service_roles", "platform/000005_phase4_roles", "platform/000008_admin_roles", "admin/000001_admin_control_plane", "admin/000002_report_exports", "admin/000003_organization", "admin/000004_operator_accounts", "admin/000005_operator_administration", "admin/000008_fulfillment_operator_session", "admin/000009_audience_groups"} {
		data, err := os.ReadFile("../../migrations/" + file + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	adminConfig := pool.Config()
	adminConfig.ConnConfig.RuntimeParams["role"] = "planext4u_admin_runtime"
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	service, _ := NewService(adminPool, time.Now)
	admin := Actor{TenantID: uuid.NewString(), Country: "IN", Subject: uuid.NewString(), PlatformAdmin: true, MFA: true}
	root, err := service.CreateNode(ctx, admin, Node{Name: "Operations", Kind: "ORGANIZATION"})
	if err != nil {
		t.Fatal(err)
	}
	node := func(name, parent string) Node {
		v, err := service.CreateNode(ctx, admin, Node{Name: name, ParentID: parent, Kind: "TERRITORY"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	west, east := node("West", root.ID), node("East", root.ID)
	child := node("West mini", west.ID)
	reader, err := service.CreateRole(ctx, admin, JobRole{Name: "Participants", Capabilities: []string{Read}}, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := service.CreateRole(ctx, admin, JobRole{Name: "Other participants", Capabilities: []string{Read}}, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	managerRole, err := service.CreateRole(ctx, admin, JobRole{Name: "West manager", Capabilities: []string{Read, StaffManage}}, west.ID)
	if err != nil {
		t.Fatal(err)
	}
	participant := uuid.NewString()
	manager := Actor{TenantID: admin.TenantID, Country: admin.Country, Subject: uuid.NewString(), MFA: true}
	operatorProof := func(actor Actor, role string) Actor {
		actor.OperatorSessionID, actor.OperatorRevision = uuid.NewString(), 1
		if _, err := pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$4,'Synthetic audience operator',$3,ARRAY['IN'])`, actor.TenantID, actor.Subject, []string{role}, "synthetic-"+actor.Subject); err != nil {
			t.Fatal(err)
		}
		digest := strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 2)
		if _, err := pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES($1,$2,$3,$4,'Synthetic audience operator',$5,ARRAY['IN'],'IN',clock_timestamp(),ARRAY['pwd','totp'],$6,clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, digest, actor.OperatorSessionID, actor.Subject, actor.TenantID, []string{role}, digest); err != nil {
			t.Fatal(err)
		}
		return actor
	}
	admin, manager = operatorProof(admin, "SUPER_ADMIN"), operatorProof(manager, "REGIONAL_STAFF")
	// Normal BFF commands require the current source operator. Bootstrap above
	// only creates synthetic test infrastructure, without impersonating a session.
	admin.RequireLiveOperator, manager.RequireLiveOperator = true, true
	assign := func(a Assignment) Assignment {
		v, err := service.SetAssignment(ctx, admin, a)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	assign(Assignment{Subject: manager.Subject, NodeID: west.ID, RoleID: managerRole.ID, Status: "ACTIVE"})
	original := assign(Assignment{Subject: participant, NodeID: child.ID, RoleID: reader.ID, Status: "ACTIVE"})
	group, err := service.SaveAudienceGroup(ctx, manager, AudienceGroup{Name: "West participants", NodeID: west.ID, RoleIDs: []string{reader.ID}, IncludeDescendants: true, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	expect := func(actor Actor, g AudienceGroup, want bool) {
		t.Helper()
		got, err := service.PreviewAudienceMember(ctx, actor, g.ID, participant)
		if err != nil || got != want {
			t.Fatalf("member=%v want=%v err=%v", got, want, err)
		}
	}
	expect(manager, group, true)
	direct := group
	direct.IncludeDescendants = false
	direct, err = service.SaveAudienceGroup(ctx, manager, direct)
	if err != nil {
		t.Fatal(err)
	}
	expect(manager, direct, false)
	if _, err = service.SaveAudienceGroup(ctx, manager, group); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	group = direct
	group.IncludeDescendants = true
	group, err = service.SaveAudienceGroup(ctx, manager, group)
	if err != nil {
		t.Fatal(err)
	}
	expect(manager, group, true)
	original.Status = "SUSPENDED"
	original = assign(original)
	expect(manager, group, false)
	assign(Assignment{Subject: participant, NodeID: child.ID, RoleID: replacement.ID, Status: "ACTIVE"})
	expect(manager, group, false)
	group.RoleIDs = []string{replacement.ID}
	group, err = service.SaveAudienceGroup(ctx, manager, group)
	if err != nil {
		t.Fatal(err)
	}
	expect(manager, group, true)
	foreignGroup, err := service.SaveAudienceGroup(ctx, admin, AudienceGroup{Name: "East only", NodeID: east.ID, RoleIDs: []string{replacement.ID}, IncludeDescendants: true, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.PreviewAudienceMember(ctx, manager, foreignGroup.ID, participant); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign branch preview: %v", err)
	}
	foreignGroup.Name = "Attempted cross-branch change"
	if _, err = service.SaveAudienceGroup(ctx, manager, foreignGroup); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign branch mutation: %v", err)
	}
	list, err := service.AudienceGroups(ctx, manager, "")
	if err != nil || len(list.Groups) != 1 || list.Groups[0].ID != group.ID || list.NextCursor != "" {
		t.Fatalf("scoped group list=%+v %v", list, err)
	}
	t.Run("bounded complete directory beyond old country cap", func(t *testing.T) {
		// Synthetic directory volume, not a business transition helper. Repeated
		// names prove the cursor follows immutable IDs rather than display names.
		if _, err := pool.Exec(ctx, `INSERT INTO admin.audience_groups(tenant_id,country,id,node_id,name,role_ids,include_descendants,active,revision)
 SELECT $1,$2,gen_random_uuid(),CASE WHEN i<=205 THEN $3::uuid ELSE $4::uuid END,'Same display name',ARRAY[$5::uuid],true,true,1 FROM generate_series(1,2206) i`, admin.TenantID, admin.Country, west.ID, east.ID, reader.ID); err != nil {
			t.Fatal(err)
		}
		walk := func(actor Actor, want int) {
			t.Helper()
			seen := map[string]bool{}
			cursor := ""
			for pageNumber := 0; pageNumber < 30; pageNumber++ {
				page, err := service.AudienceGroups(ctx, actor, cursor)
				if err != nil || len(page.Groups) > AudiencePageSize {
					t.Fatalf("page %d: %+v %v", pageNumber, page, err)
				}
				for _, value := range page.Groups {
					if seen[value.ID] || value.ID <= cursor || (!actor.PlatformAdmin && value.NodeID != west.ID) {
						t.Fatalf("duplicate, unordered or foreign page item: %+v", value)
					}
					seen[value.ID] = true
					cursor = value.ID
				}
				if page.NextCursor == "" {
					if len(seen) != want {
						t.Fatalf("directory truncated: got %d want %d", len(seen), want)
					}
					return
				}
				if len(page.Groups) != AudiencePageSize || page.NextCursor != cursor {
					t.Fatalf("cursor is not the final authorized item: %+v", page)
				}
			}
			t.Fatal("directory did not terminate")
		}
		walk(admin, 2208)
		walk(manager, 206)
		for _, invalid := range []string{"bad", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "{" + group.ID + "}", strings.Repeat("a", 500)} {
			if _, err := service.AudienceGroups(ctx, manager, invalid); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid cursor accepted: %q %v", invalid, err)
			}
		}
		for _, actor := range []Actor{{TenantID: uuid.NewString(), Subject: admin.Subject, Country: "IN", MFA: true, PlatformAdmin: true}, {TenantID: admin.TenantID, Subject: admin.Subject, Country: "NG", MFA: true, PlatformAdmin: true}} {
			page, err := service.AudienceGroups(ctx, actor, group.ID)
			if err != nil || len(page.Groups) != 0 || page.NextCursor != "" {
				t.Fatalf("cursor crossed tenant/country: %+v %v", page, err)
			}
		}
	})
	if err = service.SetNodeActive(ctx, admin, west.ID, west.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PreviewAudienceMember(ctx, admin, group.ID, participant); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled branch preview remained authorized: %v", err)
	}
	var inactiveMember bool
	if err = pool.QueryRow(ctx, `SELECT admin.audience_member($1,$2,$3,$4)`, admin.TenantID, admin.Country, participant, group.ID).Scan(&inactiveMember); err != nil || inactiveMember {
		t.Fatalf("disabled ancestor remained a public audience member: %v %v", inactiveMember, err)
	}
	if err = service.SetNodeActive(ctx, admin, west.ID, west.Revision+1, true); err != nil {
		t.Fatal(err)
	}
	expect(manager, group, true)
	invalid := AudienceGroup{Name: "Foreign role", NodeID: west.ID, RoleIDs: []string{uuid.NewString()}, Active: true}
	if _, err = service.SaveAudienceGroup(ctx, manager, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unowned role accepted: %v", err)
	}
	// Owner history and public membership must not require direct staff reads.
	config := pool.Config()
	config.ConnConfig.RuntimeParams["role"] = "planext4u_configuration_runtime"
	limited, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	resolver, err := configcms.NewPostgresAudienceResolver(limited)
	if err != nil {
		t.Fatal(err)
	}
	memberships, err := resolver.Memberships(ctx, admin.TenantID, admin.Country, participant, []string{group.ID, foreignGroup.ID})
	if err != nil || !memberships[group.ID] || memberships[foreignGroup.ID] {
		t.Fatalf("actual configuration batch membership: %v %v", memberships, err)
	}
	var member bool
	if err = limited.QueryRow(ctx, `SELECT admin.audience_member($1,$2,$3,$4)`, admin.TenantID, admin.Country, participant, group.ID).Scan(&member); err != nil || !member {
		t.Fatalf("bounded configuration membership: %v %v", member, err)
	}
	for _, scope := range [][2]string{{uuid.NewString(), "IN"}, {admin.TenantID, "NG"}} {
		if err = limited.QueryRow(ctx, `SELECT admin.audience_member($1,$2,$3,$4)`, scope[0], scope[1], participant, group.ID).Scan(&member); err != nil || member {
			t.Fatalf("foreign scope membership: %v %v", member, err)
		}
	}
	var staffRead, definitionRead, historyMutation bool
	if err = pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_configuration_runtime','admin.staff_assignments','SELECT'),has_table_privilege('planext4u_configuration_runtime','admin.audience_groups','SELECT'),has_table_privilege('planext4u_admin_runtime','admin.audience_group_versions','UPDATE,DELETE,TRUNCATE')`).Scan(&staffRead, &definitionRead, &historyMutation); err != nil || staffRead || definitionRead || historyMutation {
		t.Fatalf("audience privilege leak: %v %v %v %v", staffRead, definitionRead, historyMutation, err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION admin.fail_audience_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic history failure'; END $$; CREATE TRIGGER fail_audience_history BEFORE INSERT ON admin.audience_group_versions FOR EACH ROW EXECUTE FUNCTION admin.fail_audience_history()`); err != nil {
		t.Fatal(err)
	}
	changed := group
	changed.Active = false
	if _, err = service.SaveAudienceGroup(ctx, manager, changed); err == nil {
		t.Fatal("history failure did not abort group update")
	}
	expect(manager, group, true)
	var revision int64
	if err = pool.QueryRow(ctx, `SELECT revision FROM admin.audience_groups WHERE tenant_id=$1 AND country=$2 AND id=$3`, admin.TenantID, admin.Country, group.ID).Scan(&revision); err != nil || revision != group.Revision {
		t.Fatalf("failed history changed group: %d %v", revision, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_audience_history ON admin.audience_group_versions; DROP FUNCTION admin.fail_audience_history()`); err != nil {
		t.Fatal(err)
	}
	group.Active = false
	group, err = service.SaveAudienceGroup(ctx, manager, group)
	if err != nil {
		t.Fatal(err)
	}
	expect(manager, group, false)
	var versions int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM admin.audience_group_versions WHERE tenant_id=$1 AND country=$2 AND group_id=$3`, admin.TenantID, admin.Country, group.ID).Scan(&versions); err != nil || int64(versions) != group.Revision {
		t.Fatalf("history=%d revision=%d %v", versions, group.Revision, err)
	}
	missing := manager
	missing.OperatorSessionID = ""
	if _, err = service.SaveAudienceGroup(ctx, missing, group); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing operator proof accepted: %v", err)
	}
	stale := manager
	stale.OperatorSessionID = uuid.NewString()
	if _, err = service.PreviewAudienceMember(ctx, stale, group.ID, participant); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign source session accepted: %v", err)
	}
	missingRoot := admin
	missingRoot.OperatorSessionID = ""
	if _, err = service.CreateNode(ctx, missingRoot, Node{Name: "Missing source root", Kind: "ORGANIZATION"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("normal root command accepted missing source proof: %v", err)
	}
	newStaff := uuid.NewString()
	if _, err = service.SetAssignment(ctx, missing, Assignment{Subject: newStaff, NodeID: west.ID, RoleID: reader.ID, Status: "ACTIVE"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("normal staff command accepted missing source proof: %v", err)
	}
	// A command queued behind live directory revocation must not use an earlier
	// BFF principal after that revocation commits.
	revocation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = revocation.Rollback(context.Background()) }()
	if _, err = revocation.Exec(ctx, `UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, manager.TenantID, manager.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err = revocation.Exec(ctx, `DELETE FROM admin.sessions WHERE tenant_id=$1 AND subject_id=$2`, manager.TenantID, manager.Subject); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := service.SaveAudienceGroup(ctx, manager, group); done <- err }()
	staffDone := make(chan error, 1)
	go func() {
		_, err := service.SetAssignment(ctx, manager, Assignment{Subject: newStaff, NodeID: west.ID, RoleID: reader.ID, Status: "ACTIVE"})
		staffDone <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("audience edit passed uncommitted operator revocation: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	select {
	case err := <-staffDone:
		t.Fatalf("staff edit bypassed uncommitted operator revocation: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	if err = revocation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("queued audience edit used revoked authority: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-staffDone:
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("queued staff edit used revoked authority: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var inserted int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM admin.staff_assignments WHERE tenant_id=$1 AND country=$2 AND subject_id=$3`, admin.TenantID, admin.Country, newStaff).Scan(&inserted); err != nil || inserted != 0 {
		t.Fatalf("revoked source command changed staff: %d %v", inserted, err)
	}
}
