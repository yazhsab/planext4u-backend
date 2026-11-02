//go:build integration

package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPersistentSelfDeactivationRecoveryAndDeletionCancellation(t *testing.T) {
	url := os.Getenv("IDENTITY_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("IDENTITY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS identity CASCADE`) })
	for _, file := range []string{"platform/000001_service_roles", "platform/000002_commerce_roles", "platform/000003_transaction_roles", "platform/000004_booking_roles", "platform/000005_phase4_roles", "platform/000006_phase5_roles", "platform/000007_transaction_runtime", "identity/000001_identity", "identity/000002_profile_contacts", "identity/000003_profile_locales", "identity/000004_account_lifecycle", "identity/000005_security_evidence_append_only"} {
		body, readErr := os.ReadFile("../../migrations/" + file + ".up.sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	repository, _ := NewPostgresRepository(pool)
	hasher, _ := NewHMACRefreshHasher([]byte("synthetic-lifecycle-key-01234567890123456789"))
	service, err := NewService(ServiceConfig{TenantID: "lifecycle-tenant", AllowedCountries: []string{"IN"}, SessionTTL: 30 * 24 * time.Hour, Repository: repository, ProviderVerifier: &staticProviderVerifier{}, TokenIssuer: &fakeTokenIssuer{}, RefreshFactory: &testRefreshFactory{}, RefreshHasher: hasher, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	input := ExchangeInput{Provider: "local", ProviderToken: "lifecycle-owner", DeviceID: "lifecycle-device", Country: "IN"}
	first, err := service.Exchange(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	trusted := TrustedIdentity{Subject: first.IdentityID, Session: first.Session.ID, TenantID: first.TenantID, Country: first.Country}
	secondInput := input
	secondInput.DeviceID = "other-device"
	second, err := service.Exchange(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := service.Lifecycle(ctx, trusted)
	if err != nil || lifecycle.Status != "ACTIVE" {
		t.Fatalf("initial lifecycle=%+v %v", lifecycle, err)
	}
	deletion, err := service.RequestDeletion(ctx, trusted, "Account closure")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err = service.Lifecycle(ctx, trusted)
	if err != nil || lifecycle.Deletion == nil || lifecycle.Deletion.ID != deletion.ID {
		t.Fatalf("scheduled lifecycle=%+v %v", lifecycle, err)
	}
	if err = service.CancelDeletion(ctx, trusted, "someone-elses-request"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = service.CancelDeletion(ctx, trusted, deletion.ID); err != nil {
		t.Fatal(err)
	}
	deletion, err = service.RequestDeletion(ctx, trusted, "Account closure retry")
	if err != nil {
		t.Fatal(err)
	}
	revoked := trusted
	revoked.Session = second.Session.ID
	// A cancellation queued behind source-session revocation must not commit
	// after the revocation is acknowledged.
	revocation, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	defer func() { _ = revocation.Rollback(context.Background()) }()
	var lockedID string
	if err = revocation.QueryRow(ctx, `SELECT id FROM identity.sessions WHERE id=$1 FOR UPDATE`, revoked.Session).Scan(&lockedID); err != nil || lockedID != revoked.Session {
		t.Fatalf("lock source session: %s %v", lockedID, err)
	}
	queuedCancellation := make(chan error, 1)
	go func() { queuedCancellation <- repository.CancelDeletion(ctx, revoked, deletion.ID, now) }()
	select {
	case queuedErr := <-queuedCancellation:
		t.Fatalf("cancellation bypassed source-session lock: %v", queuedErr)
	case <-time.After(80 * time.Millisecond):
	}
	if _, err = revokeSessionTx(ctx, revocation, revoked.Session, now); err != nil {
		t.Fatal(err)
	}
	if err = revocation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case queuedErr := <-queuedCancellation:
		if !errors.Is(queuedErr, ErrSessionInvalid) {
			t.Fatalf("queued cancellation after revocation: %v", queuedErr)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = repository.CreateDeletionRequest(ctx, revoked, deletion); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked source session scheduled deletion: %v", err)
	}
	handler, _ := NewHandler(service, nil)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Planext4u-Subject", trusted.Subject)
		request.Header.Set("X-Planext4u-Tenant", trusted.TenantID)
		request.Header.Set("X-Planext4u-Session", trusted.Session)
		request.Header.Set("X-Planext4u-Country", trusted.Country)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(http.MethodGet, "/v1/me/lifecycle", ""); response.Code != 200 {
		t.Fatalf("lifecycle HTTP=%d", response.Code)
	}
	if response := call(http.MethodPost, "/v1/me/deactivation", `{"confirmation":"not confirmed"}`); response.Code != 422 {
		t.Fatalf("deactivation confirmation bypass=%d", response.Code)
	}
	// Delivery processing holds this shared lock until its owning transaction
	// commits. Deactivation must wait before acknowledging that processing ended.
	processing, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = processing.Rollback(context.Background()) }()
	if _, err = processing.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, "consent:"+trusted.Subject); err != nil {
		t.Fatal(err)
	}
	deactivation := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		deactivation <- call(http.MethodPost, "/v1/me/deactivation", `{"confirmation":"DEACTIVATE MY ACCOUNT"}`)
	}()
	select {
	case response := <-deactivation:
		t.Fatalf("deactivation acknowledged while delivery processing was in flight: %d", response.Code)
	case <-time.After(80 * time.Millisecond):
	}
	var stillActive bool
	if err = pool.QueryRow(ctx, `SELECT disabled_at IS NULL FROM identity.identities WHERE id=$1`, trusted.Subject).Scan(&stillActive); err != nil || !stillActive {
		t.Fatalf("deactivation changed identity before processing finished: active=%v err=%v", stillActive, err)
	}
	if err = processing.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var response *httptest.ResponseRecorder
	select {
	case response = <-deactivation:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if response.Code != 204 {
		t.Fatalf("deactivation=%d %s", response.Code, response.Body.String())
	}
	for _, token := range []string{first.Tokens.RefreshToken, second.Tokens.RefreshToken} {
		if _, err = service.Refresh(ctx, token); !errors.Is(err, ErrRefreshInvalid) {
			t.Fatalf("revoked refresh accepted: %v", err)
		}
	}
	if _, _, err = service.Principal(ctx, trusted); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("deactivated session accepted: %v", err)
	}
	if _, err = service.Exchange(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ordinary sign-in reactivated account: %v", err)
	}
	bad := input
	bad.Country = "NG"
	if _, err = service.Recover(ctx, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if err = repository.RecoverAccount(ctx, "other-tenant", ProviderIdentity{Provider: "local", Subject: input.ProviderToken}, now); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-tenant recovery accepted")
	}
	recovered, err := service.Recover(ctx, input)
	if err != nil || recovered.IdentityID != first.IdentityID {
		t.Fatalf("recovery=%+v %v", recovered, err)
	}
	trusted.Session = recovered.Session.ID
	lifecycle, err = service.Lifecycle(ctx, trusted)
	if err != nil || lifecycle.Status != "ACTIVE" || lifecycle.Deletion != nil {
		t.Fatalf("recovery did not cancel deletion: %+v %v", lifecycle, err)
	}
	if _, err = service.Recover(ctx, input); err != nil {
		t.Fatalf("active recovery retry=%v", err)
	}
	now = now.Add(6 * time.Minute)
	if err = service.Deactivate(ctx, trusted); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale sign-in deactivated account")
	}
	fresh, err := service.Exchange(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	trusted.Session = fresh.Session.ID
	deletion, err = service.RequestDeletion(ctx, trusted, "Deadline test")
	if err != nil {
		t.Fatal(err)
	}
	dueInput := input
	dueInput.DeviceID = "deletion-deadline-device"
	dueSession, err := service.Exchange(ctx, dueInput)
	if err != nil {
		t.Fatal(err)
	}
	beforeDeadline := now
	dueAt := now.Add(2 * time.Minute)
	if _, err = pool.Exec(ctx, `UPDATE identity.account_deletion_requests SET effective_at=$2 WHERE id=$1`, deletion.ID, dueAt); err != nil {
		t.Fatal(err)
	}
	now = dueAt
	if _, _, err = service.Principal(ctx, trusted); !errors.Is(err, ErrForbidden) {
		t.Fatalf("due deletion allowed current access: %v", err)
	}
	if _, err = service.Exchange(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("due deletion issued a fresh source session: %v", err)
	}
	if _, err = service.Refresh(ctx, dueSession.Tokens.RefreshToken); !errors.Is(err, ErrForbidden) {
		t.Fatalf("due deletion rotated refresh credentials: %v", err)
	}
	now = beforeDeadline
	if err = service.Deactivate(ctx, trusted); err != nil {
		t.Fatal(err)
	}
	now = deletion.EffectiveAt
	if _, err = service.Recover(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("overdue deletion was cancelled by recovery")
	}
	if err = repository.CancelDeletion(ctx, trusted, deletion.ID, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled source session cancelled overdue deletion: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.account_restrictions SET source='SECURITY_DISABLED' WHERE identity_id=$1`, first.IdentityID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Recover(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("security restriction cleared by recovery")
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM identity.account_deletion_requests WHERE id=$1`, deletion.ID).Scan(&status); err != nil || status != "SCHEDULED" {
		t.Fatalf("deletion falsely completed: %s %v", status, err)
	}
}
