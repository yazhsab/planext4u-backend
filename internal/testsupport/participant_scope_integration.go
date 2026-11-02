//go:build integration

package testsupport

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/organization"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"os"
	"testing"
	"time"
)

type ParticipantScopeCommands struct {
	List        func(participantreview.Scope) (participantreview.Page, error)
	Detail      func(participantreview.Scope) error
	Reject      func(participantreview.Scope, string) (bool, error)
	CountryOnly func(participantreview.Scope) error
}

func RegionalParticipantScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, databaseURL, kind, id string, country participantreview.Scope, commands ParticipantScopeCommands) {
	t.Helper()
	domain := "supply"
	migration := "000005_participant_access_targets"
	if kind == "RIDER" {
		domain = "fulfillment"
		migration = "000009_participant_access_targets"
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, domain+".participant_access_targets").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		data, err := os.ReadFile("../../migrations/" + domain + "/" + migration + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(c context.Context, connection *pgx.Conn) error {
		_, e := connection.Exec(c, "SET ROLE planext4u_admin_runtime")
		return e
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	service, err := organization.NewService(adminPool, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	administrator := organization.Actor{TenantID: country.TenantID, Country: country.Country, Subject: country.Subject, PlatformAdmin: true, MFA: true, OperatorSessionID: country.SourceSession, OperatorRevision: country.Revision, RequireLiveOperator: true}
	root, leaf, other, readRole, commandRole, staff := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, node := range []struct{ id, parent, name, kind string }{{root, "", "Regional fixture root", "ORGANIZATION"}, {leaf, root, "Regional fixture territory", "TERRITORY"}, {other, root, "Unassigned fixture territory", "TERRITORY"}} {
		if _, err = pool.Exec(ctx, `INSERT INTO admin.organization_nodes(tenant_id,country,id,parent_id,kind,name) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6)`, country.TenantID, country.Country, node.id, node.parent, node.kind, node.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []struct {
		id           string
		capabilities []string
	}{{readRole, []string{"resource.read"}}, {commandRole, []string{"resource.read", "resource.command"}}} {
		if _, err = pool.Exec(ctx, `INSERT INTO admin.job_roles(tenant_id,country,id,name,capabilities) VALUES($1,$2,$3,'Synthetic participant role',$4)`, country.TenantID, country.Country, role.id, role.capabilities); err != nil {
			t.Fatal(err)
		}
	}
	source := ParticipantReviewer(t, ctx, pool, country.TenantID, staff)
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET roles=ARRAY['REGIONAL_STAFF'],revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2;`, country.TenantID, staff); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.sessions SET roles=ARRAY['REGIONAL_STAFF'],operator_revision=2 WHERE session_id=$1`, source); err != nil {
		t.Fatal(err)
	}
	regional := participantreview.Scope{TenantID: country.TenantID, Country: country.Country, Subject: staff, SourceSession: source, Revision: 2, MFA: true}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.staff_assignments(tenant_id,country,subject_id,node_id,role_id,status) VALUES($1,$2,$3,$4,$5,'ACTIVE')`, country.TenantID, country.Country, staff, root, readRole); err != nil {
		t.Fatal(err)
	}
	binding := organization.ParticipantBinding{Kind: kind, ParticipantID: id, NodeID: leaf, Active: true}
	if err = service.BindParticipant(ctx, administrator, binding, "Assign participant to verified territory"); err != nil {
		t.Fatalf("bind real participant: %v", err)
	}
	current, err := service.ParticipantBinding(ctx, administrator, kind, id)
	if err != nil || current.Revision != 1 || !current.Active {
		t.Fatalf("binding readback %+v %v", current, err)
	}
	if err = service.BindParticipant(ctx, administrator, binding, "Stale duplicate binding must conflict"); !errors.Is(err, organization.ErrConflict) {
		t.Fatalf("binding CAS %v", err)
	}
	forged := administrator
	forged.Subject = regional.Subject
	forged.OperatorSessionID = source
	forged.OperatorRevision = 2
	if err = service.BindParticipant(ctx, forged, current, "Regional authority cannot rebind itself"); !errors.Is(err, organization.ErrForbidden) {
		t.Fatalf("regional bind %v", err)
	}
	found := func(page participantreview.Page) bool {
		for _, item := range page.Items {
			if item.ParticipantID == id {
				return true
			}
		}
		return false
	}
	page, err := commands.List(regional)
	if err != nil || len(page.Items) != 1 || !found(page) || len(page.Items[0].Actions) != 0 {
		t.Fatalf("read-only inherited scope %+v %v", page, err)
	}
	if commands.Detail != nil {
		if err = commands.Detail(regional); err != nil {
			t.Fatalf("read-only inherited detail: %v", err)
		}
	}
	if _, err = commands.Reject(regional, "regional-read-only-command-01"); err == nil {
		t.Fatal("read-only staff rejected participant")
	}
	if err = commands.CountryOnly(regional); err == nil {
		t.Fatal("regional staff acquired final approval authority")
	}
	for name, change := range map[string]func(*participantreview.Scope){"country": func(s *participantreview.Scope) { s.Country = "NG" }, "tenant": func(s *participantreview.Scope) { s.TenantID = uuid.NewString() }, "source": func(s *participantreview.Scope) { s.SourceSession = uuid.NewString() }, "revision": func(s *participantreview.Scope) { s.Revision++ }} {
		bad := regional
		change(&bad)
		if _, err = commands.List(bad); err == nil {
			t.Fatalf("%s crossed source boundary", name)
		}
		if commands.Detail != nil {
			if err = commands.Detail(bad); err == nil {
				t.Fatalf("%s crossed detail source boundary", name)
			}
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.staff_assignments SET status='SUSPENDED',revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND subject_id=$3`, country.TenantID, country.Country, staff); err != nil {
		t.Fatal(err)
	}
	if page, err = commands.List(regional); err != nil || len(page.Items) != 0 {
		t.Fatalf("revoked staff retained rows %+v %v", page, err)
	}
	if commands.Detail != nil {
		if err = commands.Detail(regional); err == nil {
			t.Fatal("revoked staff retained detail")
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.staff_assignments SET status='ACTIVE',role_id=$4,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND subject_id=$3`, country.TenantID, country.Country, staff, commandRole); err != nil {
		t.Fatal(err)
	}
	// Mandatory audit failure must leave the binding and its versions unchanged.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION admin.participant_binding_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected binding audit failure'; END $$;CREATE TRIGGER participant_binding_failure BEFORE INSERT ON admin.organization_events FOR EACH ROW EXECUTE FUNCTION admin.participant_binding_failure()`); err != nil {
		t.Fatal(err)
	}
	current.NodeID = other
	if err = service.BindParticipant(ctx, administrator, current, "Reassignment must retain audit atomically"); err == nil {
		t.Fatal("binding acknowledged without audit")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER participant_binding_failure ON admin.organization_events;DROP FUNCTION admin.participant_binding_failure()`); err != nil {
		t.Fatal(err)
	}
	if read, e := service.ParticipantBinding(ctx, administrator, kind, id); e != nil || read.NodeID != leaf || read.Revision != 1 {
		t.Fatalf("binding rollback %+v %v", read, e)
	}
	// Observe a queued owning read behind the same exclusive organization lock
	// used by real binding/assignment mutations, then revoke before releasing it.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, country.TenantID+":"+country.Country+":organization"); err != nil {
		t.Fatal(err)
	}
	result := make(chan participantreview.Page, 1)
	failure := make(chan error, 1)
	go func() { p, e := commands.List(regional); result <- p; failure <- e }()
	waiting := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND position('lock_participant_access' in query)>0)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("did not observe regional owning read wait")
	}
	if _, err = blocker.Exec(ctx, `UPDATE admin.organization_nodes SET active=false,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND id=$3`, country.TenantID, country.Country, root); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if page = <-result; len(page.Items) != 0 {
		t.Fatal("queued read retained inactive ancestor")
	}
	if err = <-failure; err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.organization_nodes SET active=true,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND id=$3`, country.TenantID, country.Country, root); err != nil {
		t.Fatal(err)
	}
	page, err = commands.List(regional)
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Actions) != 1 {
		t.Fatalf("current command scope %+v %v", page, err)
	}
	key := "regional-participant-rejection-01"
	if replay, e := commands.Reject(regional, key); e != nil || replay {
		t.Fatalf("scoped owning command %t %v", replay, e)
	}
	if replay, e := commands.Reject(regional, key); e != nil || !replay {
		t.Fatalf("scoped owning replay %t %v", replay, e)
	}
	current.NodeID = leaf
	current.Active = false
	if err = service.BindParticipant(ctx, administrator, current, "Remove regional participant access"); err != nil {
		t.Fatal(err)
	}
	if commands.Detail != nil {
		if err = commands.Detail(regional); err == nil {
			t.Fatal("revoked participant binding retained detail")
		}
	}
	if _, err = commands.Reject(regional, key); err == nil {
		t.Fatal("revoked binding replay acknowledged")
	}
	var versions int
	if err = adminPool.QueryRow(ctx, `SELECT count(*) FROM admin.participant_territory_events WHERE tenant_id=$1 AND country=$2 AND kind=$3 AND participant_id=$4`, country.TenantID, country.Country, kind, id).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("append-only binding history %d %v", versions, err)
	}
}
