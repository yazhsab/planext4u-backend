//go:build integration

package adminops

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresAdminOperationsAreDurableIdempotentIsolatedAndFourEyesControlled(t *testing.T) {
	databaseURL := os.Getenv("ADMIN_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("ADMIN_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS admin CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS admin CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000008_admin_roles.up.sql",
		"../../migrations/admin/000001_admin_control_plane.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	executor, err := NewPostgresDomainExecutor(pool, func() time.Time { return now })
	if err != nil || executor.Ready(ctx) != nil {
		t.Fatalf("executor err=%v", err)
	}
	service, err := NewPostgresService(pool, func() time.Time { return now }, executor)
	if err != nil || service.Ready(ctx) != nil {
		t.Fatalf("service err=%v", err)
	}
	requester := postgresAdminPrincipal(postgresAdminSubjectOne, postgresAdminTenantOne, now)
	standardCommand := Command{
		Domain: DomainCatalog, Action: ActionCatalogUpsert, TargetID: "item-admin-001",
		Reason: "Create the approved local catalog item", Payload: map[string]any{"name": "Local millet flour"},
		CorrelationID: "correlation-admin-standard-001",
	}

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.Submit(requester, standardCommand); !errors.Is(err, ErrAdapterUnavailable) {
			t.Fatalf("unsupported catalog execution: %v", err)
		}
	}
	var changes, records, executions, failures int
	for query, dest := range map[string]*int{
		`SELECT count(*) FROM admin.changes`:                                 &changes,
		`SELECT count(*) FROM admin.domain_records`:                          &records,
		`SELECT count(*) FROM admin.domain_executions`:                       &executions,
		`SELECT count(*) FROM admin.operation_events WHERE outcome='FAILED'`: &failures,
	} {
		if err := pool.QueryRow(ctx, query).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if changes != 0 || records != 0 || executions != 0 || failures != 2 {
		t.Fatalf("changes=%d projections=%d executions=%d failures=%d", changes, records, executions, failures)
	}

	highRiskCommand := Command{
		Domain: DomainWallet, Action: ActionWalletAdjust, TargetID: "customer-admin-001",
		Reason: "Correct an audited loyalty points discrepancy", Payload: map[string]any{"points": float64(125)},
		CorrelationID: "correlation-admin-high-001",
	}
	pending, err := service.Submit(requester, highRiskCommand)
	if err != nil || pending.Status != StatusPending {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if _, err := service.Approve(requester, pending.ID, pending.Revision); !errors.Is(err, ErrFourEyesRequired) {
		t.Fatalf("self approval error=%v", err)
	}
	approver := postgresAdminPrincipal(postgresAdminSubjectTwo, postgresAdminTenantOne, now)

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.Approve(approver, pending.ID, pending.Revision); !errors.Is(err, ErrAdapterUnavailable) {
			t.Fatalf("unsupported wallet execution: %v", err)
		}
	}
	if _, found, err := executor.Record(ctx, requester.TenantID, requester.Country, DomainWallet, highRiskCommand.TargetID); err != nil || found {
		t.Fatalf("fabricated wallet record found=%t err=%v", found, err)
	}

	restartedExecutor, _ := NewPostgresDomainExecutor(pool, func() time.Time { return now.Add(time.Minute) })
	restarted, _ := NewPostgresService(pool, func() time.Time { return now.Add(time.Minute) }, restartedExecutor)
	listed, err := restarted.List(requester)
	foundApproved := false
	for _, change := range listed {
		foundApproved = foundApproved || change.ID == pending.ID && change.Status == StatusPending && change.Revision == 1
	}
	if err != nil || len(listed) != 1 || !foundApproved {
		t.Fatalf("restarted changes=%#v err=%v", listed, err)
	}
	events, err := restarted.Audit(requester)
	if err != nil || len(events) != 5 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	otherTenant := postgresAdminPrincipal(postgresAdminSubjectThree, postgresAdminTenantTwo, now)
	isolated, err := restarted.List(otherTenant)
	if err != nil || len(isolated) != 0 {
		t.Fatalf("other tenant changes=%#v err=%v", isolated, err)
	}
	if _, found, err := restartedExecutor.Record(ctx, otherTenant.TenantID, otherTenant.Country, DomainWallet, highRiskCommand.TargetID); err != nil || found {
		t.Fatalf("cross tenant record found=%t err=%v", found, err)
	}
}

func postgresAdminPrincipal(subject, tenant string, now time.Time) Principal {
	return Principal{
		TenantID: tenant, Country: "IN", SubjectID: subject,
		Capabilities: map[string]bool{
			CapabilityCatalog: true, CapabilityWallet: true, CapabilityReporting: true,
		},
		AuthMethods: []string{"webauthn"}, AuthenticatedAt: now,
	}
}

const (
	postgresAdminTenantOne    = "d3919834-df73-418a-85a7-a0bbb90e48f3"
	postgresAdminTenantTwo    = "5d31ff08-e419-421f-90c2-a6fe89e96507"
	postgresAdminSubjectOne   = "9dcb1253-3ef5-43d6-a359-158031383127"
	postgresAdminSubjectTwo   = "cba38fdf-78df-4b13-90ee-c9327e0d8230"
	postgresAdminSubjectThree = "36539595-cd5b-4570-86e4-45e585356f80"
)
