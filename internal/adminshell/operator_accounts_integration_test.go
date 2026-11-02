//go:build integration

package adminshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOperatorAdministrationIsScopedAuditedConcurrentAndRevokesSessions(t *testing.T) {
	url := os.Getenv("ADMIN_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("ADMIN_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	for _, file := range []string{"platform/000001_service_roles", "platform/000008_admin_roles", "admin/000001_admin_control_plane", "admin/000004_operator_accounts", "admin/000005_operator_administration"} {
		data, e := os.ReadFile("../../migrations/" + file + ".up.sql")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(data)); e != nil {
			t.Fatalf("%s: %v", file, e)
		}
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, e := conn.Exec(ctx, `SET ROLE planext4u_admin_runtime`)
		return e
	}
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	accounts, _ := NewOperatorAccounts(runtime)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	actor := Principal{SubjectID: uuid.NewString(), SessionID: uuid.NewString(), TenantID: uuid.NewString(), DisplayName: "Root operator", Roles: []Role{RoleSuperAdmin}, AllowedCountries: []string{"IN", "NG"}, SelectedCountry: "IN", AuthenticatedAt: now, AuthMethods: []string{"firebase_verified", "mfa"}, OperatorRevision: 1}
	seed := func(tenant, id, provider string, roles []Role, countries []string) {
		t.Helper()
		_, e := pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Seeded operator',$4,$5)`, tenant, id, provider, roles, countries)
		if e != nil {
			t.Fatal(e)
		}
	}
	seed(actor.TenantID, actor.SubjectID, "firebase-root", actor.Roles, actor.AllowedCountries)
	active := true
	input := OperatorChange{DisplayName: "Regional operator", Roles: []Role{RoleRegionalStaff}, AllowedCountries: []string{"IN"}, Active: &active, Reason: "Approved regional onboarding"}
	created, err := accounts.Save(ctx, actor, "", "firebase-regional", input, now)
	if err != nil || created.Revision != 1 {
		t.Fatalf("create: %+v %v", created, err)
	}
	if _, err = accounts.Save(ctx, actor, "", "firebase-regional", input, now); !errors.Is(err, ErrOperatorConflict) {
		t.Fatalf("duplicate provider: %v", err)
	}
	for _, bad := range []Principal{
		func() Principal { p := actor; p.TenantID = uuid.NewString(); return p }(),
		func() Principal { p := actor; p.Roles = []Role{RoleCountryAdmin}; return p }(),
		func() Principal { p := actor; p.AuthMethods = []string{"password"}; return p }(),
		func() Principal { p := actor; p.AuthenticatedAt = now.Add(-6 * time.Minute); return p }(),
	} {
		if _, err = accounts.Save(ctx, bad, "", "denied-provider", input, now); !errors.Is(err, ErrForbidden) {
			t.Fatalf("actor boundary: %v", err)
		}
	}
	outside := input
	outside.AllowedCountries = []string{"IN", "US"}
	if _, err = accounts.Save(ctx, actor, "", "outside-provider", outside, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("country expansion: %v", err)
	}
	input.Revision = 1
	if _, err = accounts.Save(ctx, actor, actor.SubjectID, "", input, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self edit: %v", err)
	}
	otherRoot := uuid.NewString()
	seed(actor.TenantID, otherRoot, "other-root", []Role{RoleSuperAdmin}, []string{"IN"})
	if _, err = accounts.Save(ctx, actor, otherRoot, "", input, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("root edit: %v", err)
	}
	otherTenant := uuid.NewString()
	foreignID := uuid.NewString()
	seed(otherTenant, foreignID, "foreign", []Role{RoleAuditor}, []string{"IN"})
	if _, err = accounts.Save(ctx, actor, foreignID, "", input, now); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("cross tenant: %v", err)
	}
	multiID := uuid.NewString()
	seed(actor.TenantID, multiID, "multi-country", []Role{RoleAuditor}, []string{"IN", "NG"})
	limited := actor
	limited.AllowedCountries = []string{"IN"}
	page, err := accounts.List(ctx, limited, "Seeded operator", "", now)
	if err != nil || len(page.Items) != 1 || page.Items[0].SubjectID != otherRoot {
		t.Fatalf("directory scope: %+v %v", page, err)
	}
	if _, err = accounts.Save(ctx, limited, multiID, "", input, now); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("hidden country edit: %v", err)
	}
	if _, err = accounts.Events(ctx, limited, multiID, 0, now); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("hidden country history: %v", err)
	}
	// A forged/stale trusted cookie role is independently checked against rows.
	forged := actor
	forged.SubjectID = created.SubjectID
	if _, err = accounts.List(ctx, forged, "", "", now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("client authority expansion: %v", err)
	}
	store, _ := NewPostgresSessionStore(runtime, func() time.Time { return now })
	user := actor
	user.SubjectID = created.SubjectID
	user.SessionID = uuid.NewString()
	user.Roles = created.Roles
	user.AllowedCountries = created.AllowedCountries
	token, err := store.Issue(user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resolver := OperatorSessionResolver{SessionResolver: store, Accounts: accounts}
	if _, err = resolver.Resolve(requestWithCookie(token)); err != nil {
		t.Fatal(err)
	}
	inactive := false
	input.Active = &inactive
	changed, err := accounts.Save(ctx, actor, created.SubjectID, "", input, now)
	if err != nil || changed.Revision != 2 {
		t.Fatalf("suspend: %+v %v", changed, err)
	}
	if _, err = resolver.Resolve(requestWithCookie(token)); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("old cookie survived: %v", err)
	}
	// Model login lookup before the commit, but session INSERT after revocation.
	user.SessionID = uuid.NewString()
	lateToken, err := store.Issue(user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input.Revision = 2
	input.Active = &active
	if _, err = accounts.Save(ctx, actor, created.SubjectID, "", input, now); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(requestWithCookie(lateToken)); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("reactivation revived cookie: %v", err)
	}
	user.SessionID = uuid.NewString()
	lateToken, err = store.Issue(user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(requestWithCookie(lateToken)); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("late insert bypassed revision: %v", err)
	}
	input.Revision = 3
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := accounts.Save(ctx, actor, created.SubjectID, "", input, now)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for e := range results {
		if e == nil {
			succeeded++
		} else if errors.Is(e, ErrOperatorConflict) {
			conflicted++
		} else {
			t.Fatal(e)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent revisions: %d/%d", succeeded, conflicted)
	}
	restarted, _ := NewOperatorAccounts(runtime)
	events, err := restarted.Events(ctx, actor, created.SubjectID, 0, now)
	if err != nil || len(events.Items) != 4 || events.Items[0].Revision != 4 || events.Items[3].Action != "CREATED" {
		t.Fatalf("audit restart: %+v %v", events, err)
	}
	for _, event := range events.Items {
		if event.ActorID != actor.SubjectID || event.Country != "IN" || event.Reason != input.Reason {
			t.Fatalf("audit binding: %+v", event)
		}
	}
	user.SessionID = uuid.NewString()
	user.OperatorRevision = 4
	freshToken, err := store.Issue(user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(requestWithCookie(freshToken)); err != nil {
		t.Fatalf("fresh session: %v", err)
	}
	// With audit unavailable, neither authority nor session revocation commits.
	if _, err = pool.Exec(ctx, `REVOKE INSERT ON admin.operator_events FROM planext4u_admin_runtime`); err != nil {
		t.Fatal(err)
	}
	input.Revision = 4
	input.Active = &inactive
	if _, err = accounts.Save(ctx, actor, created.SubjectID, "", input, now); err == nil {
		t.Fatal("audit failure committed")
	}
	if _, err = pool.Exec(ctx, `GRANT INSERT ON admin.operator_events TO planext4u_admin_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(requestWithCookie(freshToken)); err != nil {
		t.Fatalf("failed mutation revoked session: %v", err)
	}
	var revision int64
	if err = pool.QueryRow(ctx, `SELECT revision FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2`, actor.TenantID, created.SubjectID).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("rollback: %d %v", revision, err)
	}
	if _, err = runtime.Exec(ctx, `DELETE FROM admin.operator_events`); err == nil {
		t.Fatal("runtime deleted append-only audit")
	}
	if _, err = runtime.Exec(ctx, `UPDATE admin.operator_events SET reason='tampered reason'`); err == nil {
		t.Fatal("runtime changed append-only audit")
	}
	// Bound and deterministic pagination without treating SQL wildcard text as a pattern.
	input.Active = &active
	input.Revision = 0
	for index := range 52 {
		input.DisplayName = fmt.Sprintf("Paging%% operator %02d", index)
		if _, err = accounts.Save(ctx, actor, "", fmt.Sprintf("paging-provider-%d", index), input, now); err != nil {
			t.Fatal(err)
		}
	}
	page, err = accounts.List(ctx, actor, "Paging%", "", now)
	if err != nil || len(page.Items) != 50 || page.NextAfter == "" {
		t.Fatalf("first page: %d %v", len(page.Items), err)
	}
	next, err := accounts.List(ctx, actor, "Paging%", page.NextAfter, now)
	if err != nil || len(next.Items) != 2 || next.NextAfter != "" {
		t.Fatalf("second page: %+v %v", next, err)
	}
	if page.Items[49].SubjectID >= next.Items[0].SubjectID {
		t.Fatal("unstable keyset order")
	}
}
