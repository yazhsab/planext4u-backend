package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestExchangeRefreshReuseAndRevokeLifecycle(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.exchange(t, "provider-customer", "device-private-001")
	if first.Roles[0] != RoleCustomer || first.Tokens.RefreshToken == "" || first.Session.DeviceReference == "device-private-001" {
		t.Fatalf("exchange result is unsafe or incomplete: %#v", first)
	}

	second, err := fixture.service.Refresh(context.Background(), first.Tokens.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if second.Tokens.RefreshToken == first.Tokens.RefreshToken || second.Session.ID != first.Session.ID {
		t.Fatalf("rotation did not preserve session and replace token: %#v", second)
	}
	if _, err := fixture.service.Refresh(context.Background(), first.Tokens.RefreshToken); !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("reused refresh error = %v, want ErrRefreshReuse", err)
	}
	if _, err := fixture.service.Refresh(context.Background(), second.Tokens.RefreshToken); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("family token after reuse error = %v, want ErrRefreshInvalid", err)
	}

	events := fixture.repository.AuditEvents()
	if !containsAuditType(events, "SESSION_REFRESHED") || !containsAuditType(events, "REFRESH_REUSE_DETECTED") {
		t.Fatalf("audit events missing refresh evidence: %#v", events)
	}
	for _, event := range events {
		if event.DeviceRef == "device-private-001" || event.DeviceRef == first.Tokens.RefreshToken {
			t.Fatalf("audit event contains restricted raw data: %#v", event)
		}
	}
}

func TestConcurrentRefreshDetectsReuseAndRevokesFamily(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	authentication := fixture.exchange(t, "provider-concurrent", "device-concurrent")
	results := make(chan error, 2)
	var tokens sync.Map
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := fixture.service.Refresh(context.Background(), authentication.Tokens.RefreshToken)
			if err == nil {
				tokens.Store(result.Tokens.RefreshToken, struct{}{})
			}
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	var successes, reuse int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRefreshReuse):
			reuse++
		default:
			t.Fatalf("unexpected concurrent refresh error: %v", err)
		}
	}
	if successes != 1 || reuse != 1 {
		t.Fatalf("concurrent outcomes = successes %d reuse %d", successes, reuse)
	}
	tokens.Range(func(value, _ any) bool {
		if _, err := fixture.service.Refresh(context.Background(), value.(string)); !errors.Is(err, ErrRefreshInvalid) {
			t.Errorf("winner token remained active after family reuse: %v", err)
		}
		return true
	})
}

func TestSessionExpiryAndRevokeAreSafe(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	authentication := fixture.exchange(t, "provider-expiry", "device-expiry")
	if err := fixture.service.Revoke(context.Background(), "malformed"); err != nil {
		t.Fatalf("malformed revoke should be an idempotent no-op: %v", err)
	}
	if err := fixture.service.Revoke(context.Background(), authentication.Tokens.RefreshToken); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if err := fixture.service.Revoke(context.Background(), authentication.Tokens.RefreshToken); err != nil {
		t.Fatalf("repeated Revoke() error = %v", err)
	}
	if _, err := fixture.service.Refresh(context.Background(), authentication.Tokens.RefreshToken); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("revoked token error = %v", err)
	}

	expiring := fixture.exchange(t, "provider-expiring", "device-expiring")
	fixture.clock.value = fixture.clock.value.Add(31 * 24 * time.Hour)
	if _, err := fixture.service.Refresh(context.Background(), expiring.Tokens.RefreshToken); !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("expired token error = %v, want ErrRefreshExpired", err)
	}
}

func TestAuthorizationUsesCurrentServerRolesAndIdentityContext(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	authentication := fixture.exchange(t, "provider-roles", "device-roles")
	trusted := trustedFor(authentication)
	if _, err := fixture.service.Authorize(context.Background(), trusted, RoleCustomer); err != nil {
		t.Fatalf("customer authorization error = %v", err)
	}
	if _, err := fixture.service.Authorize(context.Background(), trusted, RoleVendor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("vendor denial error = %v", err)
	}
	if err := fixture.repository.ProvisionRole(authentication.IdentityID, RoleVendor, fixture.clock.value); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Authorize(context.Background(), trusted, RoleVendor); err != nil {
		t.Fatalf("provisioned vendor authorization error = %v", err)
	}

	tampered := []TrustedIdentity{
		{Subject: "identity_other", Session: trusted.Session, TenantID: trusted.TenantID, Country: trusted.Country},
		{Subject: trusted.Subject, Session: trusted.Session, TenantID: "tenant_other", Country: trusted.Country},
		{Subject: trusted.Subject, Session: trusted.Session, TenantID: trusted.TenantID, Country: "GB"},
	}
	for _, attempt := range tampered {
		if _, err := fixture.service.Authorize(context.Background(), attempt, RoleCustomer); !errors.Is(err, ErrForbidden) {
			t.Errorf("tampered context %#v error = %v, want forbidden", attempt, err)
		}
	}
}

func TestProfileConsentAndSessionOwnership(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.exchange(t, "provider-profile", "device-profile-1")
	second := fixture.exchange(t, "provider-profile", "device-profile-2")
	if first.IdentityID != second.IdentityID || first.Session.ID == second.Session.ID {
		t.Fatalf("provider identity/session mapping is incorrect: first=%#v second=%#v", first, second)
	}
	trusted := trustedFor(first)
	profile, err := fixture.service.UpdateProfile(context.Background(), trusted, ProfileUpdate{
		DisplayName: "தமிழ் வாடிக்கையாளர்",
		Locale:      "ta",
		TimeZone:    "Asia/Kolkata",
		Version:     1,
	})
	if err != nil || profile.Version != 2 || profile.Locale != "ta" {
		t.Fatalf("UpdateProfile() = %#v, %v", profile, err)
	}
	if _, err := fixture.service.UpdateProfile(context.Background(), trusted, ProfileUpdate{
		DisplayName: "Stale update",
		Locale:      "en",
		TimeZone:    "Asia/Kolkata",
		Version:     1,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale profile update error = %v", err)
	}

	granted, err := fixture.service.RecordConsent(context.Background(), trusted, ConsentUpdate{
		Purpose:       ConsentAnalytics,
		Granted:       true,
		PolicyVersion: "privacy-2026-04",
	})
	if err != nil || !granted.Granted || granted.Version != 1 {
		t.Fatalf("grant consent = %#v, %v", granted, err)
	}
	withdrawn, err := fixture.service.RecordConsent(context.Background(), trusted, ConsentUpdate{
		Purpose:       ConsentAnalytics,
		Granted:       false,
		PolicyVersion: "privacy-2026-04",
	})
	if err != nil || withdrawn.Granted || withdrawn.Version != 2 || withdrawn.EvidenceID == granted.EvidenceID {
		t.Fatalf("withdraw consent = %#v, %v", withdrawn, err)
	}
	consents, err := fixture.service.Consents(context.Background(), trusted)
	if err != nil || len(consents) != 1 || consents[0].Granted {
		t.Fatalf("current consents = %#v, %v", consents, err)
	}
	sessions, err := fixture.service.Sessions(context.Background(), trusted)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("Sessions() = %#v, %v", sessions, err)
	}
	if err := fixture.service.RevokeSession(context.Background(), trusted, second.Session.ID); err != nil {
		t.Fatalf("revoke owned session error = %v", err)
	}

	other := fixture.exchange(t, "provider-other", "device-other")
	if err := fixture.service.RevokeSession(context.Background(), trusted, other.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner revoke error = %v", err)
	}
}

func TestIssuerFailureRevokesCreatedSession(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.issuer.err = errors.New("synthetic signing failure")
	if _, err := fixture.service.Exchange(context.Background(), ExchangeInput{
		Provider:      "local",
		ProviderToken: "provider-signing-failure",
		DeviceID:      "device-signing-failure",
		Country:       "IN",
	}); err == nil {
		t.Fatal("Exchange() error = nil")
	}
	fixture.repository.mu.RLock()
	defer fixture.repository.mu.RUnlock()
	if len(fixture.repository.sessions) != 1 {
		t.Fatalf("session count = %d", len(fixture.repository.sessions))
	}
	for _, session := range fixture.repository.sessions {
		if session.RevokedAt == nil {
			t.Fatalf("session remained active after signing failure: %#v", session)
		}
	}
}

type serviceFixture struct {
	service    *Service
	repository *MemoryRepository
	provider   *staticProviderVerifier
	issuer     *fakeTokenIssuer
	clock      *testClock
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	repository := NewMemoryRepository()
	provider := &staticProviderVerifier{}
	issuer := &fakeTokenIssuer{}
	clock := &testClock{value: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}
	hasher, err := NewHMACRefreshHasher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	refreshFactory := &testRefreshFactory{}
	ids := &testIDFactory{}
	service, err := NewService(ServiceConfig{
		TenantID:         "tenant_synthetic_001",
		AllowedCountries: []string{"IN", "GB"},
		SessionTTL:       30 * 24 * time.Hour,
		Repository:       repository,
		ProviderVerifier: provider,
		TokenIssuer:      issuer,
		RefreshFactory:   refreshFactory,
		RefreshHasher:    hasher,
		Now:              clock.Now,
		IDFactory:        ids.New,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &serviceFixture{service: service, repository: repository, provider: provider, issuer: issuer, clock: clock}
}

func (fixture *serviceFixture) exchange(t *testing.T, providerToken, deviceID string) Authentication {
	t.Helper()
	result, err := fixture.service.Exchange(context.Background(), ExchangeInput{
		Provider:      "local",
		ProviderToken: providerToken,
		DeviceID:      deviceID,
		Country:       "IN",
	})
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	return result
}

func trustedFor(authentication Authentication) TrustedIdentity {
	return TrustedIdentity{
		Subject:  authentication.IdentityID,
		Session:  authentication.Session.ID,
		TenantID: authentication.TenantID,
		Country:  authentication.Country,
	}
}

type staticProviderVerifier struct {
	err error
}

func (verifier *staticProviderVerifier) Verify(_ context.Context, provider, token string) (ProviderIdentity, error) {
	if verifier.err != nil {
		return ProviderIdentity{}, verifier.err
	}
	return ProviderIdentity{Provider: provider, Subject: token}, nil
}

type fakeTokenIssuer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (issuer *fakeTokenIssuer) Issue(Principal) (string, time.Time, error) {
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if issuer.err != nil {
		return "", time.Time{}, issuer.err
	}
	issuer.calls++
	return fmt.Sprintf("access-synthetic-%d", issuer.calls), time.Date(2026, 8, 27, 10, 10, 0, 0, time.UTC), nil
}

type testRefreshFactory struct {
	mu      sync.Mutex
	counter byte
}

func (factory *testRefreshFactory) Generate() (string, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.counter++
	value := make([]byte, 32)
	for index := range value {
		value[index] = factory.counter
	}
	return "p4ur_v1_" + base64.RawURLEncoding.EncodeToString(value), nil
}

type testIDFactory struct {
	mu       sync.Mutex
	counters map[string]int
}

func (factory *testIDFactory) New(prefix string) (string, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if factory.counters == nil {
		factory.counters = make(map[string]int)
	}
	factory.counters[prefix]++
	return fmt.Sprintf("%s_synthetic_%03d", prefix, factory.counters[prefix]), nil
}

type testClock struct {
	value time.Time
}

func (clock *testClock) Now() time.Time { return clock.value }

func containsAuditType(events []AuditEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
