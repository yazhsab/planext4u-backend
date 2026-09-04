//go:build integration

package support

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBE004PostgresOwnershipIdempotencyRestartAndAdminProjection(t *testing.T) {
	databaseURL := os.Getenv("SUPPORT_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("SUPPORT_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS support CASCADE`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS support CASCADE`)
	}()
	for _, path := range []string{"../../migrations/platform/000018_support_roles.up.sql", "../../migrations/support/000001_support.up.sql"} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	service, err := NewService(repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{TenantID: "8e0c1976-ab83-4d41-9fd7-3b58b97a20aa", Country: "IN", Subject: "vendor-postgres-001", Roles: []Role{RoleVendor}}
	input := CreateTicketRequest{OwnerRole: RoleVendor, Category: CategoryVendorOperations, Subject: "Catalogue review needs assistance", Description: "A verified catalogue item remains pending.", RelatedReference: "item-postgres-001", Priority: PriorityHigh}
	created, inserted, err := service.Create(ctx, actor, input, "support-create-postgres-0001")
	if err != nil || !inserted || len(created.Messages) != 1 {
		t.Fatalf("create=%#v inserted=%v err=%v", created, inserted, err)
	}
	restarted, err := NewService(repository, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	replay, inserted, err := restarted.Create(ctx, actor, input, "support-create-postgres-0001")
	if err != nil || inserted || replay.ID != created.ID || len(replay.Messages) != 1 {
		t.Fatalf("replay=%#v inserted=%v err=%v", replay, inserted, err)
	}
	updated, inserted, err := restarted.AddMessage(ctx, actor, created.ID, AddMessageRequest{Body: "Please confirm the review queue position."}, "support-message-postgres-0001")
	if err != nil || !inserted || len(updated.Messages) != 2 {
		t.Fatalf("message=%#v inserted=%v err=%v", updated, inserted, err)
	}
	replayed, inserted, err := restarted.AddMessage(ctx, actor, created.ID, AddMessageRequest{Body: "Please confirm the review queue position."}, "support-message-postgres-0001")
	if err != nil || inserted || len(replayed.Messages) != 2 {
		t.Fatalf("message replay=%#v inserted=%v err=%v", replayed, inserted, err)
	}
	if _, _, err = restarted.AddMessage(ctx, actor, created.ID, AddMessageRequest{Body: "Different message"}, "support-message-postgres-0001"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("message conflict=%v", err)
	}
	crossRole := actor
	crossRole.Roles = []Role{RoleRider}
	if _, err = restarted.Get(ctx, crossRole, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-role get=%v", err)
	}
	admin := AdminPrincipal{TenantID: actor.TenantID, Country: actor.Country, Subject: "support-admin-postgres", Capabilities: map[string]bool{CapabilitySupportManage: true}}
	page, err := restarted.AdminList(ctx, admin, AdminListFilter{OwnerRole: RoleVendor, Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ID || page.Items[0].MessageCount != 2 {
		t.Fatalf("admin projection=%#v err=%v", page, err)
	}
	var ticketCount, messageCount int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM support.tickets), (SELECT count(*) FROM support.messages)`).Scan(&ticketCount, &messageCount); err != nil {
		t.Fatal(err)
	}
	if ticketCount != 1 || messageCount != 2 {
		t.Fatalf("ticket count=%d message count=%d", ticketCount, messageCount)
	}
}
