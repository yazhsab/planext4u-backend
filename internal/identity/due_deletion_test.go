package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEffectiveDeletionBarsCurrentNewAndRefreshSessions(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository()
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	params := StartSessionParams{
		Provider:   ProviderIdentity{Provider: "local", Subject: "due-deletion-owner"},
		IdentityID: "due-deletion-owner", SessionID: "due-deletion-session",
		TenantID: "due-deletion-tenant", Country: "IN", DeviceID: "test-device",
		DeviceReference: "private-test-device", RefreshDigest: "initial-digest",
		RefreshExpires: now.Add(30 * 24 * time.Hour), Now: now,
	}
	account, session, err := repository.StartSession(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	trusted := TrustedIdentity{Subject: account.ID, Session: session.ID, TenantID: account.TenantID, Country: session.Country}
	if _, _, err = repository.Authenticate(ctx, trusted, now); err != nil {
		t.Fatalf("access before deletion deadline: %v", err)
	}
	due := now.Add(time.Hour)
	repository.deletions[account.ID] = DeletionRequest{ID: "due-deletion-request", IdentityID: account.ID, Status: "SCHEDULED", RequestedAt: now, EffectiveAt: due}
	if _, _, err = repository.Authenticate(ctx, trusted, due); !errors.Is(err, ErrForbidden) {
		t.Fatalf("current access after deletion deadline: %v", err)
	}
	params.SessionID, params.RefreshDigest, params.Now = "new-session", "new-digest", due
	if _, _, err = repository.StartSession(ctx, params); !errors.Is(err, ErrForbidden) {
		t.Fatalf("new session after deletion deadline: %v", err)
	}
	if _, _, err = repository.RotateSession(ctx, RotateSessionParams{CurrentDigest: "initial-digest", NextDigest: "rotated-digest", Now: due}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("refresh after deletion deadline: %v", err)
	}
	if value := repository.sessions[session.ID]; value.RevokedAt == nil {
		t.Fatal("due deletion left the refresh source session active")
	}
}
