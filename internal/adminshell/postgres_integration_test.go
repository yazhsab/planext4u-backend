//go:build integration

package adminshell

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresAdminSessionsAreOpaqueDurableScopedAndExpiring(t *testing.T) {
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
	clock := now
	store, err := NewPostgresSessionStore(pool, func() time.Time { return clock })
	if err != nil || store.Ready(ctx) != nil {
		t.Fatalf("store err=%v", err)
	}
	principal := Principal{
		SubjectID: "ca962c33-f9ec-4eb8-b42e-c819a349c61f", SessionID: "89cf9480-ee83-4b7e-bccd-12615f2972ec",
		TenantID: "c0ae2343-74bc-41b6-a8ce-400e943e316b", DisplayName: "Integration Administrator",
		Roles: []Role{RoleSuperAdmin}, AllowedCountries: []string{"IN", "NG"}, SelectedCountry: "IN",
		AuthenticatedAt: now, AuthMethods: []string{"webauthn"},
	}
	token, err := store.Issue(principal, time.Hour)
	if err != nil || len(token) < 32 {
		t.Fatalf("token length=%d err=%v", len(token), err)
	}
	var digest string
	if err := pool.QueryRow(ctx, `SELECT token_digest FROM admin.sessions WHERE session_id=$1`, principal.SessionID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest == token || digest != tokenDigest(token) {
		t.Fatalf("stored token is not an opaque digest")
	}
	restarted, _ := NewPostgresSessionStore(pool, func() time.Time { return clock })
	request := httptest.NewRequest(http.MethodGet, "https://admin.planext4u.test/admin/api/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	resolved, err := restarted.Resolve(request)
	if err != nil || resolved.Principal.SubjectID != principal.SubjectID || resolved.Principal.SelectedCountry != "IN" || resolved.CSRFToken == "" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if err := restarted.SetCountry(ctx, principal.SessionID, "NG"); err != nil {
		t.Fatal(err)
	}
	resolved, err = restarted.Resolve(request)
	if err != nil || resolved.Principal.SelectedCountry != "NG" {
		t.Fatalf("country resolved=%#v err=%v", resolved, err)
	}
	if err := restarted.SetCountry(ctx, principal.SessionID, "US"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("forbidden country error=%v", err)
	}
	clock = now.Add(time.Hour)
	if _, err := restarted.Resolve(request); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("expired session error=%v", err)
	}
	purged, err := restarted.PurgeExpired(ctx, 100)
	if err != nil || purged != 1 {
		t.Fatalf("purged=%d err=%v", purged, err)
	}
}
