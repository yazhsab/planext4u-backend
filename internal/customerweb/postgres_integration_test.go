//go:build integration

package customerweb

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

func TestPostgresCustomerSessionsAreDurableEncryptedRotatingAndExpiring(t *testing.T) {
	databaseURL := os.Getenv("CUSTOMER_WEB_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("CUSTOMER_WEB_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS customer_web CASCADE`)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS customer_web CASCADE`) })
	for _, path := range []string{"../../migrations/platform/000019_customer_web_roles.up.sql", "../../migrations/customer_web/000001_customer_sessions.up.sql"} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	clock := now
	tokenCipher, _ := NewTokenCipher([]byte(strings.Repeat("k", 32)))
	store, err := NewPostgresSessionStore(pool, func() time.Time { return clock }, tokenCipher)
	if err != nil || store.Ready(ctx) != nil {
		t.Fatalf("store err=%v", err)
	}
	session := Session{
		ID: "ca962c33-f9ec-4eb8-b42e-c819a349c61f", PlatformSession: "platform-session", IdentityID: "identity-1",
		TenantID: "tenant-in", Country: "IN", DisplayName: "Customer One", Roles: []identity.Role{identity.RoleCustomer},
		AccessToken: "secret-access-token", AccessExpiresAt: now.Add(time.Hour), RefreshToken: "secret-refresh-token",
		RefreshExpiresAt: now.Add(24 * time.Hour), CSRFToken: strings.Repeat("c", 32), ExpiresAt: now.Add(12 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	token, err := store.Create(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	var accessCiphertext, refreshCiphertext []byte
	if err := pool.QueryRow(ctx, `SELECT token_digest,access_token,refresh_token FROM customer_web.sessions WHERE session_id=$1`, session.ID).Scan(&digest, &accessCiphertext, &refreshCiphertext); err != nil {
		t.Fatal(err)
	}
	if digest == token || digest != tokenDigest(token) || strings.Contains(string(accessCiphertext), session.AccessToken) || strings.Contains(string(refreshCiphertext), session.RefreshToken) {
		t.Fatal("stored browser and platform credentials are not protected")
	}
	restarted, _ := NewPostgresSessionStore(pool, func() time.Time { return clock }, tokenCipher)
	resolved, err := restarted.Resolve(ctx, token)
	if err != nil || resolved.AccessToken != session.AccessToken || resolved.RefreshToken != session.RefreshToken {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if err := restarted.AcquireRefresh(ctx, token, 30*time.Second); err != nil {
		t.Fatalf("first refresh lease: %v", err)
	}
	if err := restarted.AcquireRefresh(ctx, token, 30*time.Second); !errors.Is(err, ErrRefreshInProgress) {
		t.Fatalf("concurrent refresh lease error=%v", err)
	}
	if err := restarted.ReleaseRefresh(ctx, token); err != nil {
		t.Fatalf("release refresh lease: %v", err)
	}
	session.AccessToken = "rotated-access-token"
	session.RefreshToken = "rotated-refresh-token"
	session.UpdatedAt = now.Add(time.Minute)
	rotated, err := restarted.Replace(ctx, token, session)
	if err != nil || rotated == token {
		t.Fatalf("rotate token=%q err=%v", rotated, err)
	}
	if _, err := restarted.Resolve(ctx, token); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("old opaque token error=%v", err)
	}
	clock = session.ExpiresAt
	if _, err := restarted.Resolve(ctx, rotated); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("expired session error=%v", err)
	}
	purged, err := restarted.PurgeExpired(ctx, 100)
	if err != nil || purged != 1 {
		t.Fatalf("purged=%d err=%v", purged, err)
	}
}
