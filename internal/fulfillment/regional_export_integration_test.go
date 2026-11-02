//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/organization"
)

func verifyRegionalExportAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner *organization.Service, root organization.Actor, service *PostgresService, actor Actor, node string) {
	t.Helper()
	if _, err := service.RegionalSummaryExport(actor, "CHENNAI"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read/command assignment gained export: %v", err)
	}
	role, err := owner.CreateRole(ctx, root, organization.JobRole{Name: "Regional export verifier", Capabilities: []string{organization.Read, organization.ResourceRead, organization.ResourceExport}}, node)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.SetAssignment(ctx, root, organization.Assignment{Subject: actor.Subject, NodeID: node, RoleID: role.ID, Status: "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RegionalSummaryExport(actor, "CHENNAI")
	if err != nil || result.Territories != 1 || result.Country != "IN" || result.OnlineRiders != 0 {
		t.Fatalf("scoped export=%+v %v", result, err)
	}
	reader := actor
	reader.Roles = []string{"REGIONAL_ADMIN"}
	dashboard, err := service.RegionalDashboard(reader, "CHENNAI")
	if err != nil || result.Territories != dashboard.Territories || result.OnlineRiders != dashboard.OnlineRiders || result.UnassignedTasks != dashboard.UnassignedTasks || result.AtRiskTasks != dashboard.AtRiskTasks || result.CompletedToday != dashboard.CompletedToday {
		t.Fatalf("summary differs from scoped owner counters: %+v %+v %v", result, dashboard, err)
	}
	for name, alter := range map[string]func(*Actor){"tenant": func(a *Actor) { a.TenantID = fulfillmentTenantTwo }, "country": func(a *Actor) { a.Country = "NG" }, "revision": func(a *Actor) { a.OperatorRevision++ }, "missing-session": func(a *Actor) { a.OperatorSessionID = "" }, "mfa": func(a *Actor) { a.MFAVerified = false }} {
		other := actor
		alter(&other)
		if _, err := service.RegionalSummaryExport(other, "CHENNAI"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s export allowed: %v", name, err)
		}
	}
	// A trusted/internal role field must not turn this directory's scoped staff
	// member into a country operator. The owning DB derives country authority.
	forged := actor
	forged.Roles = []string{"SUPER_ADMIN", "OPS_ADMIN"}
	if value, err := service.RegionalSummaryExport(forged, "CHENNAI"); err != nil || value.Territories != 1 {
		t.Fatalf("role field expanded export: %+v %v", value, err)
	}
	// Real directory/session country authority sees both territories even when
	// the caller's role hint is deliberately less privileged.
	source := uuid.NewString()
	countrySubject := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Synthetic export country operator',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'])`, root.TenantID, countrySubject, "export-country-"+source); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES(repeat(replace($1::text,'-',''),2),$1::uuid,$2,$3,'Synthetic export operator',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'],'IN',clock_timestamp(),ARRAY['pwd','totp'],repeat('e',32),clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, source, countrySubject, root.TenantID); err != nil {
		t.Fatal(err)
	}
	country := actor
	country.Subject = countrySubject
	country.OperatorSessionID = source
	country.OperatorRevision = 1
	country.Roles = []string{"REGIONAL_ADMIN"}
	if value, err := service.RegionalSummaryExport(country, "CHENNAI"); err != nil || value.Territories != 2 {
		t.Fatalf("live country export=%+v %v", value, err)
	}
	if territories, err := service.Territories(country); err != nil || len(territories) != 2 {
		t.Fatalf("live country territory read=%+v %v", territories, err)
	}
	if dashboard, err := service.RegionalDashboard(country, "CHENNAI"); err != nil || dashboard.Territories != 2 || dashboard.PayoutsPending == nil {
		t.Fatalf("live country dashboard=%+v %v", dashboard, err)
	}
	// Mandatory audit failure cannot return prepared data as a success.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fulfillment.reject_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='REGIONAL_SUMMARY_EXPORTED' THEN RAISE EXCEPTION 'Synthetic export audit outage'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_export_audit BEFORE INSERT ON fulfillment.audit_events FOR EACH ROW EXECUTE FUNCTION fulfillment.reject_export_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RegionalSummaryExport(actor, "CHENNAI"); err == nil {
		t.Fatal("unaudited export succeeded")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_export_audit ON fulfillment.audit_events; DROP FUNCTION fulfillment.reject_export_audit()`); err != nil {
		t.Fatal(err)
	}
	// Observe the export actually waiting behind an owning scope revocation.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":organization"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE admin.staff_assignments SET status='SUSPENDED',revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND node_id=$4 AND role_id=$5`, actor.TenantID, actor.Country, actor.Subject, node, role.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, exportErr := service.RegionalSummaryExport(actor, "CHENNAI"); done <- exportErr }()
	observed := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event='advisory' AND query LIKE 'SELECT pg_advisory_xact_lock_shared%')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("export did not wait for the pending scope revocation")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case exportErr := <-done:
		if !errors.Is(exportErr, ErrForbidden) {
			t.Fatalf("queued export retained revoked scope: %v", exportErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued export did not finish after revocation")
	}
	if _, err = service.RegionalSummaryExport(actor, "CHENNAI"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended export persisted: %v", err)
	}
	var privateRead bool
	if err = pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_fulfillment_runtime','admin.operator_accounts','SELECT')`).Scan(&privateRead); err != nil || privateRead {
		t.Fatalf("export broadened private directory grants: %t %v", privateRead, err)
	}
}
