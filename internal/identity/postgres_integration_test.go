//go:build integration

package identity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresIdentityLifecycleAndConcurrentReuse(t *testing.T) {
	databaseURL := os.Getenv("IDENTITY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("IDENTITY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	// The integration target points only at the disposable local identity test
	// database. Cleanup never touches another schema.
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS identity CASCADE`)
	})
	migration, err := os.ReadFile("../../migrations/identity/000001_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply identity migration: %v", err)
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{value: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	hasher, err := NewHMACRefreshHasher([]byte("integration-key-0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		TenantID:         "tenant_integration_001",
		AllowedCountries: []string{"IN"},
		SessionTTL:       30 * 24 * time.Hour,
		Repository:       repository,
		ProviderVerifier: &staticProviderVerifier{},
		TokenIssuer:      &fakeTokenIssuer{},
		RefreshFactory:   &testRefreshFactory{},
		RefreshHasher:    hasher,
		Now:              clock.Now,
		IDFactory:        (&testIDFactory{}).New,
	})
	if err != nil {
		t.Fatal(err)
	}
	authentication, err := service.Exchange(ctx, ExchangeInput{
		Provider:      "local",
		ProviderToken: "provider-postgres-customer",
		DeviceID:      "device-postgres-private",
		Country:       "IN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authentication.Roles[0] != RoleCustomer {
		t.Fatalf("roles = %v", authentication.Roles)
	}

	results := make(chan error, 2)
	var winnerToken string
	var winnerMutex sync.Mutex
	var wait sync.WaitGroup
	for attempt := 0; attempt < 2; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			rotated, rotateErr := service.Refresh(context.Background(), authentication.Tokens.RefreshToken)
			if rotateErr == nil {
				winnerMutex.Lock()
				winnerToken = rotated.Tokens.RefreshToken
				winnerMutex.Unlock()
			}
			results <- rotateErr
		}()
	}
	wait.Wait()
	close(results)
	var successCount, reuseCount int
	for result := range results {
		if result == nil {
			successCount++
		} else if errors.Is(result, ErrRefreshReuse) {
			reuseCount++
		} else {
			t.Fatalf("unexpected refresh result: %v", result)
		}
	}
	if successCount != 1 || reuseCount != 1 {
		t.Fatalf("refresh outcomes = success %d reuse %d", successCount, reuseCount)
	}
	if _, err := service.Refresh(ctx, winnerToken); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("winner family token remained active: %v", err)
	}

	trusted := trustedFor(authentication)
	if _, _, err := service.Principal(ctx, trusted); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked access session error = %v", err)
	}
	var reuseEvents int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM identity.security_events WHERE event_type = 'REFRESH_REUSE_DETECTED'`).Scan(&reuseEvents); err != nil {
		t.Fatal(err)
	}
	if reuseEvents != 1 {
		t.Fatalf("reuse event count = %d", reuseEvents)
	}
	var plaintextMatches int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM identity.refresh_tokens WHERE digest = $1`, authentication.Tokens.RefreshToken).Scan(&plaintextMatches); err != nil {
		t.Fatal(err)
	}
	if plaintextMatches != 0 {
		t.Fatal("plaintext refresh token was stored")
	}

	profileAuth, err := service.Exchange(ctx, ExchangeInput{
		Provider:      "local",
		ProviderToken: "provider-postgres-profile",
		DeviceID:      "device-postgres-profile-1",
		Country:       "IN",
	})
	if err != nil {
		t.Fatal(err)
	}
	profileTrusted := trustedFor(profileAuth)
	profile, err := service.UpdateProfile(ctx, profileTrusted, ProfileUpdate{
		DisplayName: "தமிழ் வாடிக்கையாளர்",
		Locale:      "ta",
		TimeZone:    "Asia/Kolkata",
		Version:     1,
	})
	if err != nil || profile.Version != 2 {
		t.Fatalf("PostgreSQL profile update = %#v, %v", profile, err)
	}
	if _, err := service.UpdateProfile(ctx, profileTrusted, ProfileUpdate{
		DisplayName: "Stale",
		Locale:      "en",
		TimeZone:    "Asia/Kolkata",
		Version:     1,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("PostgreSQL stale profile error = %v", err)
	}
	if _, err := service.RecordConsent(ctx, profileTrusted, ConsentUpdate{
		Purpose:       ConsentAnalytics,
		Granted:       true,
		PolicyVersion: "privacy-2026-04",
	}); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := service.RecordConsent(ctx, profileTrusted, ConsentUpdate{
		Purpose:       ConsentAnalytics,
		Granted:       false,
		PolicyVersion: "privacy-2026-04",
	})
	if err != nil || withdrawn.Version != 2 || withdrawn.Granted {
		t.Fatalf("PostgreSQL consent withdrawal = %#v, %v", withdrawn, err)
	}
	consents, err := service.Consents(ctx, profileTrusted)
	if err != nil || len(consents) != 1 || consents[0].Granted {
		t.Fatalf("PostgreSQL current consents = %#v, %v", consents, err)
	}
	secondProfileSession, err := service.Exchange(ctx, ExchangeInput{
		Provider:      "local",
		ProviderToken: "provider-postgres-profile",
		DeviceID:      "device-postgres-profile-2",
		Country:       "IN",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.Sessions(ctx, profileTrusted)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("PostgreSQL sessions = %#v, %v", sessions, err)
	}
	other, err := service.Exchange(ctx, ExchangeInput{
		Provider:      "local",
		ProviderToken: "provider-postgres-other",
		DeviceID:      "device-postgres-other",
		Country:       "IN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeSession(ctx, profileTrusted, other.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PostgreSQL cross-owner revoke error = %v", err)
	}
	if err := service.RevokeSession(ctx, profileTrusted, secondProfileSession.Session.ID); err != nil {
		t.Fatalf("PostgreSQL owned revoke error = %v", err)
	}
	if err := service.Revoke(ctx, profileAuth.Tokens.RefreshToken); err != nil {
		t.Fatalf("PostgreSQL refresh revoke error = %v", err)
	}
	if err := service.Revoke(ctx, profileAuth.Tokens.RefreshToken); err != nil {
		t.Fatalf("PostgreSQL repeated refresh revoke error = %v", err)
	}
	unknownToken, err := (RandomRefreshTokenFactory{Random: bytes.NewReader(bytes.Repeat([]byte{0xfe}, 32))}).Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, unknownToken); err != nil {
		t.Fatalf("PostgreSQL unknown refresh revoke error = %v", err)
	}
	if _, _, err := service.Principal(ctx, profileTrusted); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("PostgreSQL revoked principal error = %v", err)
	}

	expiring, err := service.Exchange(ctx, ExchangeInput{
		Provider:      "local",
		ProviderToken: "provider-postgres-expiry",
		DeviceID:      "device-postgres-expiry",
		Country:       "IN",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.value = clock.value.Add(31 * 24 * time.Hour)
	if _, err := service.Refresh(ctx, expiring.Tokens.RefreshToken); !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("PostgreSQL expired refresh error = %v", err)
	}
}
