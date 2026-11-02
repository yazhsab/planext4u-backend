//go:build integration

package bankverification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type bankFixture struct {
	mu                      sync.Mutex
	results                 map[string]LinkResult
	createEffects, requests int
	loseNext                bool
	verify                  bool
}

func (f *bankFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	var input LinkRequest
	if json.NewDecoder(r.Body).Decode(&input) != nil || r.Header.Get("Idempotency-Key") != input.AttemptID || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
		w.WriteHeader(400)
		return
	}
	result, exists := f.results[input.AttemptID]
	switch r.URL.Path {
	case "/v1/bank-accounts/link-sessions":
		if !exists {
			input.ProviderSession = "session_" + strings.ReplaceAll(input.AttemptID, "-", "")
			result = LinkResult{LinkRequest: input, Status: "AWAITING_USER", ExpiresAt: time.Now().UTC().Add(10 * time.Minute), LaunchURL: "https://bank.example.test/hosted?one_time=private-synthetic-secret"}
			f.results[input.AttemptID] = result
			f.createEffects++
		}
		if f.loseNext {
			f.loseNext = false
			w.WriteHeader(503)
			return
		}
	case "/v1/bank-accounts/link-sessions:resolve", "/v1/bank-accounts/link-sessions:cancel":
		if !exists || input.ProviderSession != result.ProviderSession && !(input.ProviderSession == "" && strings.HasSuffix(r.URL.Path, ":cancel")) {
			w.WriteHeader(404)
			return
		}
		if f.verify {
			result.Status, result.LaunchURL = "VERIFIED", ""
			result.Account = &Account{Reference: "bankref_" + strings.ReplaceAll(input.AttemptID, "-", ""), HolderName: "Synthetic Owner", Last4: "1234", IFSC: "TEST0123456"}
			result.VerificationID, result.VerifiedAt = "verification_"+input.AttemptID, time.Now().UTC()
		} else if strings.HasSuffix(r.URL.Path, ":cancel") {
			result.Status, result.LaunchURL = "CANCELLED", ""
		}
		f.results[input.AttemptID] = result
	default:
		w.WriteHeader(404)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func bankIntegrationDatabase(t *testing.T) (context.Context, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("BANK_VERIFICATION_DATABASE_TEST_URL")
	if raw == "" {
		t.Skip("BANK_VERIFICATION_DATABASE_TEST_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Path != "/planext4u_banklink_test" && u.Path != "/planext4u_phase0_verification") || (u.Hostname() != "postgres" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("bank integration requires an explicitly disposable verification database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS bankverification CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS bankverification CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`)
	})
	paths, err := filepath.Glob("../../migrations/platform/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	identityPaths, err := filepath.Glob("../../migrations/identity/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	bankPaths, err := filepath.Glob("../../migrations/bankverification/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	sort.Strings(identityPaths)
	sort.Strings(bankPaths)
	paths = append(paths, identityPaths...)
	paths = append(paths, bankPaths...)
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, e := c.Exec(ctx, `SET ROLE planext4u_bankverification_runtime`)
		return e
	}
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return ctx, pool, runtime
}

func seedBankActor(t *testing.T, ctx context.Context, pool *pgxpool.Pool) Actor {
	t.Helper()
	a := Actor{Scope: Scope{TenantID: uuid.NewString(), Country: "IN", OwnerID: uuid.NewString()}, SessionID: uuid.NewString()}
	now := time.Now().UTC()
	_, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at) VALUES($1,$2,'synthetic',$1,$3)`, a.OwnerID, a.TenantID, now)
	if err == nil {
		_, err = pool.Exec(ctx, `INSERT INTO identity.sessions(id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at) VALUES($3,$1,$2,'IN','synthetic-device','synthetic-device',$5,$4,$4)`, a.OwnerID, a.TenantID, a.SessionID, now, now.Add(time.Hour))
	}
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPostgresHostedBankLinkRecoveryAuthorityAuditAndIsolation(t *testing.T) {
	ctx, pool, runtime := bankIntegrationDatabase(t)
	fixture := &bankFixture{results: map[string]LinkResult{}, loseNext: true}
	provider := httptest.NewServer(fixture)
	defer provider.Close()
	endpoint, _ := url.Parse(provider.URL)
	linker, err := NewHTTPLinker(LinkerConfig{Endpoint: endpoint, Client: &http.Client{Timeout: time.Second}, Token: strings.Repeat("s", 32), Clock: time.Now, LaunchOrigins: []string{"https://bank.example.test"}, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewPostgresService(runtime, linker, time.Now)
	if err = service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	a := seedBankActor(t, ctx, pool)
	if _, err = service.Start(ctx, a, "VENDOR", "lost-create-0001"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lost response: %v", err)
	}
	current, err := service.Current(ctx, a, "VENDOR")
	if err != nil || current.Status != "CREATING" || current.LaunchURL != "" {
		t.Fatalf("attempt was not persisted before side effect: %+v %v", current, err)
	}
	service, _ = NewPostgresService(runtime, linker, time.Now)
	resumed, err := service.Start(ctx, a, "VENDOR", "lost-create-0001")
	if err != nil || resumed.AttemptID != current.AttemptID || resumed.Status != "AWAITING_USER" || resumed.LaunchURL == "" {
		t.Fatalf("restart/retry failed: %+v %v", resumed, err)
	}
	fixture.mu.Lock()
	effects := fixture.createEffects
	fixture.mu.Unlock()
	if effects != 1 {
		t.Fatalf("retry created %d provider sessions", effects)
	}
	interrupted := seedBankActor(t, ctx, pool)
	fixture.mu.Lock()
	fixture.loseNext = true
	fixture.mu.Unlock()
	if _, err = service.Start(ctx, interrupted, "RIDER", "interrupted-cancel-01"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	unknown, err := service.Current(ctx, interrupted, "RIDER")
	if err != nil || unknown.Status != "CREATING" {
		t.Fatal("unknown provider session was not durable")
	}
	fixture.mu.Lock()
	beforeCancelEffects := fixture.createEffects
	fixture.mu.Unlock()
	if cancelled, cancelErr := service.Cancel(ctx, interrupted, "RIDER", unknown.AttemptID); cancelErr != nil || cancelled.Status != "CANCELLED" {
		t.Fatalf("interrupted cancel=%+v %v", cancelled, cancelErr)
	}
	fixture.mu.Lock()
	afterCancelEffects := fixture.createEffects
	fixture.mu.Unlock()
	if beforeCancelEffects != afterCancelEffects {
		t.Fatal("cancelling an unknown session initiated verification")
	}
	var persisted string
	if err = pool.QueryRow(ctx, `SELECT row_to_json(a)::text FROM bankverification.link_attempts a WHERE id=$1`, resumed.AttemptID).Scan(&persisted); err != nil || strings.Contains(persisted, "private-synthetic-secret") || strings.Contains(persisted, "launch_url") {
		t.Fatal("private hosted capability persisted")
	}
	other := seedBankActor(t, ctx, pool)
	for _, actor := range []Actor{other, {Scope: Scope{TenantID: a.TenantID, Country: "GB", OwnerID: a.OwnerID}, SessionID: a.SessionID}, {Scope: a.Scope, SessionID: other.SessionID}} {
		if _, err = service.Resume(ctx, actor, "VENDOR", resumed.AttemptID); err == nil {
			t.Fatal("cross-owner/country/session attempt was disclosed")
		}
	}
	if _, err = service.Resume(ctx, a, "RIDER", resumed.AttemptID); !errors.Is(err, ErrNotFound) {
		t.Fatal("role boundary was not enforced")
	}
	fixture.mu.Lock()
	fixture.verify = true
	fixture.mu.Unlock()
	verified, err := service.Resume(ctx, a, "VENDOR", resumed.AttemptID)
	if err != nil || verified.Status != "VERIFIED" || verified.Account == nil || verified.Account.Last4 != "1234" || verified.LaunchURL != "" {
		t.Fatalf("masked verification failed: %+v %v", verified, err)
	}
	readback, err := service.Current(ctx, a, "VENDOR")
	if err != nil || readback.Account == nil || readback.Account.Reference != verified.Account.Reference {
		t.Fatalf("verified restart readback: %+v %v", readback, err)
	}
	if _, err = runtime.Exec(ctx, `SELECT * FROM identity.identities`); err == nil {
		t.Fatal("bank runtime can read the identity directory")
	}
	if _, err = runtime.Exec(ctx, `UPDATE bankverification.audit_events SET action='RESUMED'`); err == nil {
		t.Fatal("bank runtime can rewrite security audit")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM bankverification.audit_events`); err == nil {
		t.Fatal("append-only audit trigger missing")
	}
	// Freshness comes from the live session, not a caller MFA/role claim.
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET authenticated_at=clock_timestamp()-interval '16 minutes' WHERE id=$1`, a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Current(ctx, a, "VENDOR"); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale login disclosed a masked bank result")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET authenticated_at=clock_timestamp() WHERE id=$1`, a.SessionID); err != nil {
		t.Fatal(err)
	}
	// New idempotency keys cannot fan out active concurrent provider sessions.
	fixture.mu.Lock()
	fixture.verify = false
	fixture.mu.Unlock()
	var wg sync.WaitGroup
	views := make(chan LinkView, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := service.Start(ctx, other, "RIDER", uuid.NewString())
			views <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(views)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var activeID string
	for v := range views {
		if activeID == "" {
			activeID = v.AttemptID
		}
		if v.AttemptID != activeID {
			t.Fatal("concurrent starts did not converge")
		}
	}
	if v, e := service.Cancel(ctx, other, "RIDER", activeID); e != nil || v.Status != "CANCELLED" || v.LaunchURL != "" {
		t.Fatalf("cancel=%+v %v", v, e)
	}
	if v, e := service.Cancel(ctx, other, "RIDER", activeID); e != nil || v.Status != "CANCELLED" {
		t.Fatalf("idempotent cancel=%+v %v", v, e)
	}
	// An audit write failure prevents both preparing an attempt and its side effect.
	_, err = pool.Exec(ctx, `CREATE FUNCTION bankverification.fail_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit outage'; END $$; CREATE TRIGGER fail_audit_test BEFORE INSERT ON bankverification.audit_events FOR EACH ROW EXECUTE FUNCTION bankverification.fail_audit_test()`)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.requests
	fixture.mu.Unlock()
	if _, err = service.Start(ctx, other, "VENDOR", "audit-failure-01"); err == nil {
		t.Fatal("mandatory audit failure acknowledged")
	}
	fixture.mu.Lock()
	after := fixture.requests
	fixture.mu.Unlock()
	if before != after {
		t.Fatal("provider was called before mandatory attempt/audit persistence")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_audit_test ON bankverification.audit_events; DROP FUNCTION bankverification.fail_audit_test()`); err != nil {
		t.Fatal(err)
	}
	// Observe a real wait behind revocation; the queued operation must see the
	// committed revocation before invoking the provider or returning a capability.
	locking, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locking.Rollback(ctx) }()
	if _, err = locking.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, other.SessionID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, e := service.Start(ctx, other, "VENDOR", "revocation-wait-01"); result <- e }()
	observed := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%identity.lock_bank_link_session%' AND pid<>pg_backend_pid())`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("authorization lock wait was not observed")
	}
	if err = locking.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("queued revocation result=%v", err)
	}
	fixture.mu.Lock()
	after = fixture.requests
	fixture.mu.Unlock()
	if before != after {
		t.Fatal("revoked source session reached provider")
	}
	// Disabled accounts fail even with another non-revoked fresh source session.
	if _, err = pool.Exec(ctx, `UPDATE identity.identities SET disabled_at=clock_timestamp() WHERE id=$1`, a.OwnerID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Current(ctx, a, "VENDOR"); !errors.Is(err, ErrForbidden) {
		t.Fatal("disabled owner disclosed bank result")
	}
}

func TestPostgresExpiredBankDataIsRedactedWithAuditRollbackLeasesAndRetry(t *testing.T) {
	ctx, pool, runtime := bankIntegrationDatabase(t)
	service, _ := NewPostgresService(runtime, UnavailableLinker(), time.Now)
	if _, err := service.Expire(ctx, 0); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("unbounded expiry sweep accepted")
	}
	owner := seedBankActor(t, ctx, pool)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for index, id := range ids {
		state := []string{"VERIFIED", "AWAITING_USER", "CANCELLED"}[index]
		var masked, verificationID, verifiedAt any
		if state == "VERIFIED" {
			masked = `{"reference":"bankref_synthetic1234","holder_name":"Private Synthetic Owner","last4":"1234","ifsc":"TEST0123456"}`
			verificationID, verifiedAt = "verification_synthetic", time.Now().UTC().Add(-time.Hour)
		}
		// Separate roles/owners avoid the active-attempt uniqueness constraint.
		actor := owner
		if index > 0 {
			actor = seedBankActor(t, ctx, pool)
		}
		_, err := pool.Exec(ctx, `INSERT INTO bankverification.link_attempts(id,tenant_id,country,owner_id,application_role,idempotency_hash,status,provider_session_id,account,verification_id,verified_at,created_at,updated_at,expires_at) VALUES($1,$2,'IN',$3,'VENDOR',$4,$5,'session_synthetic_expiry',$6,$7,$8,clock_timestamp()-interval '2 hours',clock_timestamp()-interval '2 hours',clock_timestamp()-interval '110 minutes')`, id, actor.TenantID, actor.OwnerID, strings.Repeat(string(rune('a'+index)), 64), state, masked, verificationID, verifiedAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.Current(ctx, owner, "VENDOR")
	if err != nil || view.Status != "EXPIRED" || view.Account != nil {
		t.Fatal("expired proof disclosed before cleanup")
	}
	locked, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(ctx) }()
	if _, err = locked.Exec(ctx, `SELECT id FROM bankverification.link_attempts WHERE id=$1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION bankverification.fail_expiry_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic expiry audit outage'; END $$; CREATE TRIGGER fail_expiry_audit_test BEFORE INSERT ON bankverification.audit_events FOR EACH ROW EXECUTE FUNCTION bankverification.fail_expiry_audit_test()`)
	if err != nil {
		t.Fatal(err)
	}
	if count, sweepErr := service.Expire(ctx, 10); sweepErr == nil || count != 0 {
		t.Fatalf("audit failure acknowledged redaction: %d %v", count, sweepErr)
	}
	var retained int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM bankverification.link_attempts WHERE provider_session_id IS NOT NULL`).Scan(&retained); err != nil || retained != 3 {
		t.Fatal("failed audit did not roll back redaction")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_expiry_audit_test ON bankverification.audit_events; DROP FUNCTION bankverification.fail_expiry_audit_test()`); err != nil {
		t.Fatal(err)
	}
	if count, sweepErr := service.Expire(ctx, 10); sweepErr != nil || count != 2 {
		t.Fatalf("leased expiry should skip locked attempt: %d %v", count, sweepErr)
	}
	if err = locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if count, sweepErr := service.Expire(ctx, 10); sweepErr != nil || count != 1 {
		t.Fatalf("retry missed released attempt: %d %v", count, sweepErr)
	}
	if count, sweepErr := service.Expire(ctx, 10); sweepErr != nil || count != 0 {
		t.Fatalf("expiry replay rewrote completed work: %d %v", count, sweepErr)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM bankverification.link_attempts WHERE account IS NOT NULL OR provider_session_id IS NOT NULL OR verification_id IS NOT NULL OR verified_at IS NOT NULL OR status<>'EXPIRED'`).Scan(&retained); err != nil || retained != 0 {
		t.Fatal("expired private bank metadata survived cleanup")
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM bankverification.audit_events WHERE session_id='SYSTEM_EXPIRY' AND action='EXPIRED'`).Scan(&retained); err != nil || retained != 3 {
		t.Fatal("expiry did not create one durable audit per redacted attempt")
	}
}
