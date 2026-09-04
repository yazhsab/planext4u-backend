package customerweb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/identity"
)

func TestMemorySessionStoreSerializesRefreshRotation(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	clock := now
	store, err := NewMemorySessionStore(func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	session := Session{
		ID: "ca962c33-f9ec-4eb8-b42e-c819a349c61f", PlatformSession: "platform-session", IdentityID: "identity-1",
		TenantID: "tenant-in", Country: "IN", DisplayName: "Customer One", Roles: []identity.Role{identity.RoleCustomer},
		AccessToken: "access-token", AccessExpiresAt: now.Add(time.Hour), RefreshToken: "refresh-token",
		RefreshExpiresAt: now.Add(24 * time.Hour), CSRFToken: strings.Repeat("c", 32), ExpiresAt: now.Add(12 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	token, err := store.Create(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireRefresh(context.Background(), token, 30*time.Second); err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if err := store.AcquireRefresh(context.Background(), token, 30*time.Second); !errors.Is(err, ErrRefreshInProgress) {
		t.Fatalf("concurrent lease error = %v", err)
	}
	if err := store.ReleaseRefresh(context.Background(), token); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if err := store.AcquireRefresh(context.Background(), token, 30*time.Second); err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	clock = now.Add(31 * time.Second)
	if err := store.AcquireRefresh(context.Background(), token, 30*time.Second); err != nil {
		t.Fatalf("lease after expiry: %v", err)
	}
}
