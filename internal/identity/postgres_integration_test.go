//go:build integration

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
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
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000002_commerce_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/platform/000004_booking_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/platform/000006_phase5_roles.up.sql",
		"../../migrations/platform/000007_transaction_runtime.up.sql",
		"../../migrations/identity/000001_identity.up.sql",
		"../../migrations/identity/000002_profile_contacts.up.sql",
		"../../migrations/identity/000003_profile_locales.up.sql",
		"../../migrations/identity/000004_account_lifecycle.up.sql",
		"../../migrations/identity/000005_security_evidence_append_only.up.sql",
		"../../migrations/identity/000006_scoped_consent_decisions.up.sql",
		"../../migrations/identity/000008_contact_matching.up.sql",
		"../../migrations/identity/000009_profile_occupations.up.sql",
	} {
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
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	// Keep the scheduled deletion in the future relative to PostgreSQL's
	// independent clock; the production due-deletion guard uses database time.
	clock := &testClock{value: time.Now().UTC().Truncate(time.Second)}
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
	var participantID string
	for _, role := range []Role{RoleVendor, RoleRider, RoleVendor} {
		result, enrollErr := service.Exchange(ctx, ExchangeInput{Provider: "local", ProviderToken: "provider-participant-account", DeviceID: "participant-device", Country: "IN", EnrollAs: role})
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		if participantID == "" {
			participantID = result.IdentityID
		}
		if result.IdentityID != participantID {
			t.Fatal("linked participation duplicated identity")
		}
	}
	var participantRoles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.identity_roles WHERE identity_id=$1`, participantID).Scan(&participantRoles); err != nil || participantRoles != 3 {
		t.Fatalf("participant roles=%d err=%v", participantRoles, err)
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
	if _, err := pool.Exec(ctx, `UPDATE identity.profiles SET email=$2, phone=$3 WHERE identity_id=$1`, profileAuth.IdentityID, "customer-postgres@example.test", "+919876543210"); err != nil {
		t.Fatal(err)
	}
	profile, err := service.UpdateProfile(ctx, profileTrusted, ProfileUpdate{
		DisplayName: "தமிழ் வாடிக்கையாளர்",
		Locale:      "gu",
		TimeZone:    "Asia/Kolkata",
		Version:     1,
	})
	if err != nil || profile.Version != 2 || profile.Locale != "gu" || profile.Email != "customer-postgres@example.test" || profile.Phone != "+919876543210" {
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
		ExpectedVersion: new(int64(0)),
		Purpose:         ConsentAnalytics,
		Granted:         true,
		PolicyVersion:   "privacy-2026-04",
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
	exported, err := service.ExportData(ctx, profileTrusted)
	if err != nil || exported.IdentityID != profileAuth.IdentityID || exported.Profile.DisplayName != profile.DisplayName || len(exported.Consents) != 1 || len(exported.Sessions) != 1 || len(exported.ConsentHistory) != 2 || !exported.ConsentHistory[0].Granted || exported.ConsentHistory[1].Granted || exported.SchemaVersion != 1 || len(exported.Coverage.Excluded) == 0 {
		t.Fatalf("PostgreSQL data export = %#v, %v", exported, err)
	}
	if exported.ContactSnapshots != nil {
		t.Fatal("legacy portable export changed its schema")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.contact_snapshots(identity_id,country,revision,key_id,phone_hash,policy_version,request_hash,contact_count,expires_at) VALUES($1,'IN',3,'private-key-id',repeat('a',64),'contacts-v1',repeat('b',64),1,$2)`, profileAuth.IdentityID, clock.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.contact_hashes(identity_id,country,phone_hash) VALUES($1,'IN',repeat('c',64))`, profileAuth.IdentityID); err != nil {
		t.Fatal(err)
	}
	exportedV2, err := service.ExportDataVersion(ctx, profileTrusted, 2)
	if err != nil || exportedV2.SchemaVersion != 2 || exportedV2.ContactSnapshots == nil || len(*exportedV2.ContactSnapshots) != 1 || (*exportedV2.ContactSnapshots)[0].ContactCount != 1 || (*exportedV2.ContactSnapshots)[0].Revision != 3 {
		t.Fatalf("safe contact metadata export = %#v, %v", exportedV2, err)
	}
	encodedV2, err := json.Marshal(exportedV2)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key-id", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		if strings.Contains(string(encodedV2), secret) {
			t.Fatalf("contact export leaked private material: %s", secret)
		}
	}
	deletion, err := service.RequestDeletion(ctx, profileTrusted, "Customer-requested account closure")
	if err != nil || deletion.Status != "SCHEDULED" || deletion.IdentityID != profileAuth.IdentityID || !deletion.EffectiveAt.Equal(deletion.RequestedAt.Add(30*24*time.Hour)) {
		t.Fatalf("PostgreSQL deletion request = %#v, %v", deletion, err)
	}
	updatedDeletion, err := service.RequestDeletion(ctx, profileTrusted, "Updated closure reason")
	if err != nil || updatedDeletion.ID != deletion.ID || updatedDeletion.Reason != "Updated closure reason" {
		t.Fatalf("PostgreSQL deletion request update = %#v, %v", updatedDeletion, err)
	}
	exportedV3, err := service.ExportDataVersion(ctx, profileTrusted, 3)
	if err != nil || exportedV3.SchemaVersion != 3 || exportedV3.DeletionHistory == nil || len(*exportedV3.DeletionHistory) != 1 || (*exportedV3.DeletionHistory)[0].ID != deletion.ID || (*exportedV3.DeletionHistory)[0].Reason != "Updated closure reason" || exportedV3.ContactSnapshots == nil || len(*exportedV3.ContactSnapshots) != 1 {
		t.Fatalf("PostgreSQL safe lifecycle export = %#v, %v", exportedV3, err)
	}
	encodedV3, err := json.Marshal(exportedV3)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key-id", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), "provider-postgres-profile", "provider_subject"} {
		if strings.Contains(string(encodedV3), secret) {
			t.Fatalf("lifecycle export leaked private material: %s", secret)
		}
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
