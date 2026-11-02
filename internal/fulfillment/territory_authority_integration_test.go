//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/organization"
)

func verifyTerritoryAccessFoundation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time, country Actor) {
	t.Helper()
	for _, file := range []string{"platform/000008_admin_roles", "admin/000001_admin_control_plane", "admin/000003_organization", "admin/000004_operator_accounts", "admin/000005_operator_administration", "fulfillment/000006_territory_access_targets", "admin/000006_territory_bindings", "admin/000007_fulfillment_action_scope", "admin/000008_fulfillment_operator_session", "admin/000010_fulfillment_exports", "admin/000018_fulfillment_read_scope"} {
		baseTables := map[string]string{"admin/000001_admin_control_plane": "admin.sessions", "admin/000003_organization": "admin.organization_nodes", "admin/000004_operator_accounts": "admin.operator_accounts", "admin/000005_operator_administration": "admin.operator_events"}
		if table := baseTables[file]; table != "" {
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists {
				continue
			}
		}
		if file == "admin/000008_fulfillment_operator_session" {
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT to_regprocedure('admin.lock_fulfillment_operator(uuid,character,uuid,uuid,bigint)') IS NOT NULL`).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists {
				continue
			}
		}
		if file == "admin/000018_fulfillment_read_scope" {
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT to_regprocedure('admin.fulfillment_read_scope(uuid,character,uuid,uuid,bigint)') IS NOT NULL`).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists {
				continue
			}
		}
		data, err := os.ReadFile("../../migrations/" + file + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS admin CASCADE`) })
	config := pool.Config()
	config.ConnConfig.RuntimeParams["role"] = "planext4u_admin_runtime"
	adminPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	owner, _ := organization.NewService(adminPool, func() time.Time { return now })
	root := organization.Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentAdmin, PlatformAdmin: true, MFA: true}
	parent, err := owner.CreateNode(ctx, root, organization.Node{Name: "Delivery organization", Kind: "ORGANIZATION"})
	if err != nil {
		t.Fatal(err)
	}
	north, err := owner.CreateNode(ctx, root, organization.Node{ParentID: parent.ID, Name: "North deliveries", Kind: "TERRITORY"})
	if err != nil {
		t.Fatal(err)
	}
	south, err := owner.CreateNode(ctx, root, organization.Node{ParentID: parent.ID, Name: "South deliveries", Kind: "TERRITORY"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := owner.CreateRole(ctx, root, organization.JobRole{Name: "Field territory", Capabilities: []string{organization.Read, organization.ResourceRead, organization.ResourceCommand}}, north.ID)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := owner.SetAssignment(ctx, root, organization.Assignment{Subject: fulfillmentField, NodeID: north.ID, RoleID: role.ID, Status: "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.BindTerritory(ctx, root, fulfillmentTerritory, north.ID, 0, "Verified local delivery territory assignment"); err != nil {
		t.Fatal(err)
	}
	otherTerritory := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.territories(id,tenant_id,country,region_id,franchise_identity_id,name,center_latitude,center_longitude,radius_km,postal_codes) VALUES($1,$2,'IN','CHENNAI',$3,'Other delivery territory',13.0827,80.2707,5,ARRAY['600001'])`, otherTerritory, fulfillmentTenantOne, fulfillmentFranchise); err != nil {
		t.Fatal(err)
	}
	if err = owner.BindTerritory(ctx, root, otherTerritory, south.ID, 0, "Separate delivery authority"); err != nil {
		t.Fatal(err)
	}
	targets, err := owner.TerritoryBindings(ctx, root)
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets=%v err=%v", targets, err)
	}
	if err = owner.BindTerritory(ctx, root, fulfillmentTerritory, south.ID, 0, "Stale create must not overwrite"); !errors.Is(err, organization.ErrConflict) {
		t.Fatalf("stale bind=%v", err)
	}
	delegate := root
	delegate.PlatformAdmin = false
	delegate.Subject = fulfillmentField
	commandIDs, commandErr := owner.FieldCommandTerritories(ctx, delegate)
	if commandErr != nil || len(commandIDs) != 1 || commandIDs[0] != fulfillmentTerritory {
		t.Fatalf("current field UI scope=%v %v", commandIDs, commandErr)
	}
	if ids, queryErr := owner.FieldCommandTerritories(ctx, root); queryErr != nil || len(ids) != 0 {
		t.Fatalf("global operator gained field attendance=%v %v", ids, queryErr)
	}
	if err = owner.BindTerritory(ctx, delegate, fulfillmentTerritory, south.ID, 1, "Delegated staff cannot claim territory"); !errors.Is(err, organization.ErrForbidden) {
		t.Fatalf("self escalation=%v", err)
	}
	if err = owner.BindTerritory(ctx, root, uuid.NewString(), north.ID, 0, "Unknown business resource denied"); !errors.Is(err, organization.ErrNotFound) {
		t.Fatalf("unknown target=%v", err)
	}
	foreign := root
	foreign.Country = "GB"
	if err = owner.BindTerritory(ctx, foreign, fulfillmentTerritory, north.ID, 1, "Country switch cannot claim territory"); !errors.Is(err, organization.ErrForbidden) {
		t.Fatalf("country bind=%v", err)
	}
	config = pool.Config()
	config.ConnConfig.RuntimeParams["role"] = "planext4u_fulfillment_runtime"
	runtimePool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	service, _ := NewPostgresService(runtimePool, func() time.Time { return now })
	if _, err = pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,'synthetic-field-operator','Synthetic field operator',ARRAY['REGIONAL_STAFF'],ARRAY['IN'])`, fulfillmentTenantOne, fulfillmentField); err != nil {
		t.Fatal(err)
	}
	seedFieldOperatorSession(t, ctx, pool, fieldOperatorInitialSession, 1, 0)
	regional := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentField, Roles: []string{"REGIONAL_ADMIN"}, MFAVerified: true, OperatorSessionID: fieldOperatorInitialSession, OperatorRevision: 1}
	visible, err := service.Territories(regional)
	if err != nil || len(visible) != 1 {
		t.Fatalf("delegated territories=%v err=%v", visible, err)
	}
	dashboard, err := service.RegionalDashboard(regional, "CHENNAI")
	if err != nil || dashboard.PayoutsPending != nil || dashboard.OnlineRiders != 0 || len(dashboard.LatestLocations) != 0 {
		t.Fatalf("regional finance leaked: %v %v", dashboard, err)
	}
	if attendance, readErr := service.Attendance(regional, fulfillmentRiderOne); readErr != nil || len(attendance) != 0 {
		t.Fatalf("unscoped rider attendance=%+v %v", attendance, readErr)
	}
	forged := regional
	forged.Roles = []string{"OPS_ADMIN"}
	if visible, err = service.Territories(forged); err != nil || len(visible) != 1 {
		t.Fatalf("forged global role expanded territory scope=%v %v", visible, err)
	}
	if dashboard, err = service.RegionalDashboard(forged, "CHENNAI"); err != nil || dashboard.Territories != 1 || dashboard.PayoutsPending != nil {
		t.Fatalf("forged global role expanded dashboard=%v %v", dashboard, err)
	}
	unassigned := regional
	unassigned.Subject = fulfillmentRiderOne
	if visible, err = service.Territories(unassigned); !errors.Is(err, ErrForbidden) || len(visible) != 0 {
		t.Fatalf("unassigned source read=%v %v", visible, err)
	}
	wrongTenant := regional
	wrongTenant.TenantID = fulfillmentTenantTwo
	if visible, err = service.Territories(wrongTenant); !errors.Is(err, ErrForbidden) || len(visible) != 0 {
		t.Fatalf("wrong tenant=%v %v", visible, err)
	}
	for name, alter := range map[string]func(*Actor){"tenant": func(a *Actor) { a.TenantID = fulfillmentTenantTwo }, "country": func(a *Actor) { a.Country = "NG" }, "revision": func(a *Actor) { a.OperatorRevision++ }, "missing-session": func(a *Actor) { a.OperatorSessionID = "" }, "mfa": func(a *Actor) { a.MFAVerified = false }} {
		other := regional
		alter(&other)
		if _, err = service.Territories(other); err == nil {
			t.Fatalf("%s territory read allowed", name)
		}
		if _, err = service.RegionalDashboard(other, "CHENNAI"); err == nil {
			t.Fatalf("%s dashboard read allowed", name)
		}
	}
	field := regional
	field.Roles = []string{"FIELD_OFFICER"}
	verifyRegionalExportAuthority(t, ctx, pool, owner, root, service, field, north.ID)
	if _, _, err = service.FieldCheckIn(field, "scoped-field-proof-command", fulfillmentTerritory, Point{Latitude: 13.0827, Longitude: 80.2707}); err != nil {
		t.Fatal(err)
	}
	otherOfficer, cleanupReadFixtures := verifyScopedOperationalReads(t, ctx, pool, service, regional, country, otherTerritory, now)
	assignment.Status = "SUSPENDED"
	assignment, err = owner.SetAssignment(ctx, root, assignment)
	if ids, queryErr := owner.FieldCommandTerritories(ctx, delegate); queryErr != nil || len(ids) != 0 {
		t.Fatalf("suspended field UI scope=%v %v", ids, queryErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.FieldCheckIn(field, "scoped-field-proof-command", fulfillmentTerritory, Point{Latitude: 13.0827, Longitude: 80.2707}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked replay=%v", err)
	}
	if visible, err = service.Territories(regional); err != nil || len(visible) != 0 {
		t.Fatalf("revoked read=%v %v", visible, err)
	}
	if attendance, readErr := service.Attendance(regional, otherOfficer); readErr != nil || len(attendance) != 0 {
		t.Fatalf("revoked attendance=%+v %v", attendance, readErr)
	}
	if audits, readErr := service.Audits(regional); readErr != nil || len(audits) != 0 {
		t.Fatalf("revoked scoped audits=%+v %v", audits, readErr)
	}
	cleanupReadFixtures()
	assignment.Status = "ACTIVE"
	assignment, err = owner.SetAssignment(ctx, root, assignment)
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.SetNodeActive(ctx, root, parent.ID, parent.Revision, false); err != nil {
		t.Fatal(err)
	}
	if visible, err = service.Territories(regional); err != nil || len(visible) != 0 {
		t.Fatalf("inactive ancestor=%v %v", visible, err)
	}
	if err = owner.SetNodeActive(ctx, root, parent.ID, parent.Revision+1, true); err != nil {
		t.Fatal(err)
	}
	// A command queued behind an in-progress revocation sees the revoked rows.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, root.TenantID+":"+root.Country+":organization"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE admin.staff_assignments SET status='SUSPENDED',revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND subject_id=$3`, root.TenantID, root.Country, field.Subject); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, commandErr := service.FieldCheckIn(field, "field-revocation-concurrent-proof", fulfillmentTerritory, Point{Latitude: 13.0827, Longitude: 80.2707})
		done <- commandErr
	}()
	select {
	case commandErr := <-done:
		t.Fatalf("field command bypassed pending revocation: %v", commandErr)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case commandErr := <-done:
		if !errors.Is(commandErr, ErrForbidden) {
			t.Fatalf("racing revocation=%v", commandErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("field command did not observe revocation")
	}
	assignment.Revision++
	assignment.Status = "ACTIVE"
	if _, err = owner.SetAssignment(ctx, root, assignment); err != nil {
		t.Fatal(err)
	}
	verifyFieldOperatorRevocation(t, ctx, pool, service, field)
	currentReader := regional
	currentReader.OperatorSessionID = fieldOperatorCurrentSession
	currentReader.OperatorRevision = 3
	if visible, err = service.Territories(currentReader); err != nil || len(visible) != 1 {
		t.Fatalf("current operator read=%v %v", visible, err)
	}
	logoutTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer logoutTx.Rollback(context.Background())
	if _, err = logoutTx.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, fieldOperatorCurrentSession); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := service.Territories(currentReader)
		readDone <- readErr
	}()
	select {
	case readErr := <-readDone:
		t.Fatalf("territory read bypassed pending logout: %v", readErr)
	case <-time.After(50 * time.Millisecond):
	}
	if err = logoutTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case readErr := <-readDone:
		if !errors.Is(readErr, ErrForbidden) {
			t.Fatalf("queued territory read after logout=%v", readErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("territory read did not observe logout")
	}
	if _, err = service.RegionalDashboard(currentReader, "CHENNAI"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("logged-out dashboard read=%v", err)
	}
	// The surrounding lifecycle test uses this current fixture for its later
	// field command; restore only the synthetic test session after the proof.
	seedFieldOperatorSession(t, ctx, pool, fieldOperatorCurrentSession, 3, 0)
	if _, err = pool.Exec(ctx, `DELETE FROM admin.fulfillment_territory_bindings WHERE territory_id=$1`, otherTerritory); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM fulfillment.territories WHERE id=$1`, otherTerritory); err != nil {
		t.Fatal(err)
	}
	var directRead, auditUpdate bool
	if err = pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_fulfillment_runtime','admin.staff_assignments','SELECT'),has_table_privilege('planext4u_admin_runtime','admin.fulfillment_territory_binding_events','UPDATE')`).Scan(&directRead, &auditUpdate); err != nil || directRead || auditUpdate {
		t.Fatalf("cross-owner grants direct=%t audit=%t err=%v", directRead, auditUpdate, err)
	}
	// Remove this helper's command data so the existing lifecycle assertions count
	// their own field event. Authority/binding remain for that subsequent command.
	for _, query := range []string{`DELETE FROM fulfillment.attendance_entries WHERE subject_identity_id=$1`, `DELETE FROM fulfillment.field_check_ins WHERE officer_identity_id=$1`, `DELETE FROM fulfillment.audit_events WHERE actor_identity_id=$1`, `DELETE FROM fulfillment.idempotency_records WHERE subject_id=$1`} {
		if _, err = pool.Exec(ctx, query, fulfillmentField); err != nil {
			t.Fatal(err)
		}
	}
}

func verifyScopedOperationalReads(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, regional, country Actor, southTerritory string, now time.Time) (string, func()) {
	t.Helper()
	officer, northCheck, southCheck := uuid.NewString(), uuid.NewString(), uuid.NewString()
	northAudit, southAudit, financeAudit := uuid.NewString(), uuid.NewString(), uuid.NewString()
	foreignTenantAudit, foreignCountryAudit := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO fulfillment.field_check_ins(id,tenant_id,country,officer_identity_id,territory_id,latitude,longitude,distance_m,recorded_at) VALUES($1,$3,'IN',$4,$5,13.0827,80.2707,0,$7),($2,$3,'IN',$4,$6,13.0827,80.2707,0,$7)`, northCheck, southCheck, regional.TenantID, officer, fulfillmentTerritory, southTerritory, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO fulfillment.attendance_entries(id,tenant_id,country,subject_identity_id,session_id,kind,latitude,longitude,source,recorded_at) VALUES($1,$5,'IN',$6,$3,'FIELD_CHECK_IN',13.0827,80.2707,'FIELD_APP',$7),($2,$5,'IN',$6,$4,'FIELD_CHECK_IN',13.0827,80.2707,'FIELD_APP',$7)`, uuid.NewString(), uuid.NewString(), northCheck, southCheck, regional.TenantID, officer, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO fulfillment.audit_events(id,tenant_id,country,actor_identity_id,action,resource_type,resource_id,reason,created_at) VALUES($1,$6,'IN',$7,'FIELD_CHECK_IN','TERRITORY',$8,'Geofence verified',$10),($2,$6,'IN',$7,'FIELD_CHECK_IN','TERRITORY',$9,'Geofence verified',$10),($3,$6,'IN',$7,'PAYOUT_APPROVED','PAYOUT',$11,'Synthetic finance detail',$10),($4,$12,'IN',$7,'FIELD_CHECK_IN','TERRITORY',$8,'Foreign tenant',$10),($5,$6,'NG',$7,'FIELD_CHECK_IN','TERRITORY',$8,'Foreign country',$10)`, northAudit, southAudit, financeAudit, foreignTenantAudit, foreignCountryAudit, regional.TenantID, officer, fulfillmentTerritory, southTerritory, now, uuid.NewString(), fulfillmentTenantTwo); err != nil {
		t.Fatal(err)
	}
	for name, actor := range map[string]Actor{"regional": regional, "forged-ops": {TenantID: regional.TenantID, Country: regional.Country, Subject: regional.Subject, Roles: []string{"OPS_ADMIN"}, MFAVerified: true, OperatorSessionID: regional.OperatorSessionID, OperatorRevision: regional.OperatorRevision}} {
		attendance, err := service.Attendance(actor, officer)
		if err != nil || len(attendance) != 1 || attendance[0].SessionID != northCheck {
			t.Fatalf("%s scoped attendance=%+v %v", name, attendance, err)
		}
		audits, err := service.Audits(actor)
		if err != nil || len(audits) == 0 {
			t.Fatalf("%s scoped audits=%+v %v", name, audits, err)
		}
		seenNorth := false
		for _, event := range audits {
			if event.ID == northAudit {
				seenNorth = true
			}
			if event.Action != "FIELD_CHECK_IN" || event.ResourceType != "TERRITORY" || event.ResourceID != fulfillmentTerritory || event.ID == southAudit || event.ID == financeAudit || event.ID == foreignTenantAudit || event.ID == foreignCountryAudit {
				t.Fatalf("%s leaked scoped audit=%+v", name, event)
			}
		}
		if !seenNorth {
			t.Fatalf("%s omitted assigned-territory audit", name)
		}
	}
	if attendance, err := service.Attendance(country, officer); err != nil || len(attendance) != 2 {
		t.Fatalf("country attendance=%+v %v", attendance, err)
	}
	countryAudits, err := service.Audits(country)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range countryAudits {
		seen[event.ID] = true
	}
	if !seen[northAudit] || !seen[southAudit] || !seen[financeAudit] || seen[foreignTenantAudit] || seen[foreignCountryAudit] {
		t.Fatalf("country audit scope=%v", seen)
	}
	for name, alter := range map[string]func(*Actor){"tenant": func(a *Actor) { a.TenantID = fulfillmentTenantTwo }, "country": func(a *Actor) { a.Country = "NG" }, "revision": func(a *Actor) { a.OperatorRevision++ }, "missing-session": func(a *Actor) { a.OperatorSessionID = "" }, "mfa": func(a *Actor) { a.MFAVerified = false }} {
		candidate := regional
		alter(&candidate)
		if _, err := service.Attendance(candidate, officer); err == nil {
			t.Fatalf("%s attendance source allowed", name)
		}
		if _, err := service.Audits(candidate); err == nil {
			t.Fatalf("%s audit source allowed", name)
		}
	}
	return officer, func() {
		for _, query := range []string{`DELETE FROM fulfillment.attendance_entries WHERE subject_identity_id=$1`, `DELETE FROM fulfillment.field_check_ins WHERE officer_identity_id=$1`, `DELETE FROM fulfillment.audit_events WHERE actor_identity_id=$1`} {
			if _, err := pool.Exec(ctx, query, officer); err != nil {
				t.Fatal(err)
			}
		}
	}
}
