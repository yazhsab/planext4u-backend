//go:build integration

package customerweb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

func TestPostgresCustomerWebPrivacySessionScrubIsScopedDurableAndHeld(t *testing.T) {
	raw := os.Getenv("CUSTOMER_WEB_DATABASE_TEST_URL")
	if raw == "" {
		t.Skip("CUSTOMER_WEB_DATABASE_TEST_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Path != "/planext4u_phase0_verification" && u.Path != "/planext4u_test" && u.Path != "/planext4u_customer_web_test") ||
		(u.Hostname() != "postgres" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("customer web privacy integration requires an explicitly disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS customer_web CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS customer_web CASCADE; DROP SCHEMA IF EXISTS identity CASCADE`)
	})
	for _, service := range []string{"platform", "identity", "customer_web"} {
		paths, globErr := filepath.Glob("../../migrations/" + service + "/*.up.sql")
		if globErr != nil || len(paths) == 0 {
			t.Fatalf("discover %s migrations: %v", service, globErr)
		}
		sort.Strings(paths)
		for _, path := range paths {
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, applyErr := pool.Exec(ctx, string(body)); applyErr != nil {
				t.Fatalf("apply %s: %v", path, applyErr)
			}
		}
	}
	now := time.Now().UTC()
	tenant, subject, otherSubject := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request := uuid.NewString()
	for _, id := range []string{subject, otherSubject} {
		if _, err = pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
			VALUES($1,$2,'synthetic',$1,$3)`, id, tenant, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
		(id,identity_id,status,reason,requested_at,effective_at,country)
		VALUES($1,$2,'SCHEDULED','synthetic customer web privacy verification',$3,$4,'IN')`,
		request, subject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cipher, err := NewTokenCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresSessionStore(pool, time.Now, cipher)
	if err != nil {
		t.Fatal(err)
	}
	create := func(country, owner string) string {
		t.Helper()
		session := Session{ID: uuid.NewString(), PlatformSession: uuid.NewString(), IdentityID: owner,
			TenantID: tenant, Country: country, DisplayName: "Synthetic Customer", Roles: []identity.Role{identity.RoleCustomer},
			AccessToken: "synthetic-access-token", AccessExpiresAt: now.Add(time.Hour), RefreshToken: "synthetic-refresh-token",
			RefreshExpiresAt: now.Add(24 * time.Hour), CSRFToken: strings.Repeat("c", 32),
			ExpiresAt: now.Add(12 * time.Hour), CreatedAt: now, UpdatedAt: now}
		token, createErr := store.Create(ctx, session)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return token
	}
	targetToken := create("IN", subject)
	foreignCountryToken := create("GB", subject)
	foreignSubjectToken := create("IN", otherSubject)
	var allowed bool
	if err = pool.QueryRow(ctx, `SELECT has_function_privilege('planext4u_customer_web_runtime',
		'customer_web.privacy_scrub_sessions(text)','EXECUTE')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("runtime scrub execute privilege=%v err=%v", allowed, err)
	}
	if _, err = runCustomerWebPrivacyScrub(ctx, pool, request); err == nil {
		t.Fatal("session scrub ran without current country policy and READY task")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
		(tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
		VALUES($1,'IN','customer_web',1,'customer-web-policy-v1',interval '0',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
		(request_id,owner,status,updated_at) VALUES($1,'customer_web','READY',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
		(request_id,owner,reason_reference,recorded_at)
		VALUES($1,'customer_web','synthetic customer web hold',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err = runCustomerWebPrivacyScrub(ctx, pool, request); err == nil {
		t.Fatal("session scrub ignored an active legal hold")
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_legal_holds
		SET released_at=clock_timestamp(),release_reason_reference='synthetic customer web hold release'
		WHERE request_id=$1`, request); err != nil {
		t.Fatal(err)
	}
	// Account session creation is not capped. A subject with more than 10,000
	// sessions must not make every due deletion retry fail forever.
	const bulkSessions = 10001
	if _, err = pool.Exec(ctx, `INSERT INTO customer_web.sessions
		(token_digest,session_id,platform_session_id,identity_id,tenant_id,country,display_name,roles,is_guest,
		 access_token,access_expires_at,refresh_token,refresh_expires_at,csrf_token,expires_at,created_at,updated_at)
		SELECT lpad(to_hex(n),64,'0'),('00000000-0000-4000-8000-'||lpad(to_hex(n),12,'0'))::uuid,
		 'bulk-'||n::text,$1,$2,'IN','Synthetic Customer',ARRAY['CUSTOMER'],false,
		 decode(repeat('01',32),'hex'),$3,decode('0102','hex'),$4,$5,$4,$6,$6
		FROM generate_series(1,$7) AS generated(n)`,
		subject, tenant, now.Add(time.Hour), now.Add(12*time.Hour), strings.Repeat("c", 32), now, bulkSessions); err != nil {
		t.Fatal(err)
	}

	// A registration already holding a write lock must settle before the scrub
	// sees the final set. The table lock also covers the zero-row case.
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `INSERT INTO customer_web.sessions
		(token_digest,session_id,platform_session_id,identity_id,tenant_id,country,display_name,roles,is_guest,
		 access_token,access_expires_at,refresh_token,refresh_expires_at,csrf_token,expires_at,created_at,updated_at)
		VALUES($1,$2,'queued-platform',$3,$4,'IN','Synthetic Customer',ARRAY['CUSTOMER'],false,
		 decode(repeat('01',32),'hex'),$5,decode('0102','hex'),$6,$7,$6,$8,$8)`,
		strings.Repeat("a", 64), uuid.NewString(), subject, tenant, now.Add(time.Hour), now.Add(12*time.Hour), strings.Repeat("c", 32), now); err != nil {
		_ = writer.Rollback(ctx)
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, scrubErr := runCustomerWebPrivacyScrub(ctx, pool, request); result <- scrubErr }()
	select {
	case scrubErr := <-result:
		_ = writer.Rollback(ctx)
		t.Fatalf("scrub bypassed queued session write: %v", scrubErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case scrubErr := <-result:
		if scrubErr != nil {
			t.Fatalf("scrub after queued registration: %v", scrubErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session scrub did not resume after queued registration")
	}
	if rows, scrubErr := runCustomerWebPrivacyScrub(ctx, pool, request); scrubErr != nil || rows != bulkSessions+2 {
		t.Fatalf("idempotent scrub rows=%d err=%v", rows, scrubErr)
	}
	if _, err = store.Resolve(ctx, targetToken); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("scrubbed browser token resolved: %v", err)
	}
	for _, token := range []string{foreignCountryToken, foreignSubjectToken} {
		if _, err = store.Resolve(ctx, token); err != nil {
			t.Fatalf("unrelated browser token lost: %v", err)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE customer_web.sessions SET country='IN'
		WHERE token_digest=$1`, tokenDigest(foreignCountryToken)); err == nil {
		t.Fatal("foreign-country session moved into erased scope")
	}
	if _, err = pool.Exec(ctx, `UPDATE customer_web.sessions SET identity_id=$1
		WHERE token_digest=$2`, subject, tokenDigest(foreignSubjectToken)); err == nil {
		t.Fatal("foreign-subject session moved into erased scope")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM customer_web.privacy_session_scrub_runs WHERE request_id=$1`, request); err == nil {
		t.Fatal("owner privacy evidence was deleted")
	}
	if _, err = pool.Exec(ctx, `TRUNCATE customer_web.privacy_session_scrub_runs`); err == nil {
		t.Fatal("owner privacy evidence was truncated")
	}
	if err = pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_customer_web_runtime',
		'customer_web.privacy_session_scrub_runs','SELECT')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("runtime owner evidence read privilege=%v err=%v", allowed, err)
	}
	var outcome string
	if err = pool.QueryRow(ctx, `SELECT outcome FROM identity.deletion_owner_receipts
		WHERE request_id=$1 AND owner='customer_web'`, request).Scan(&outcome); err != nil || outcome != "RETAINED" {
		t.Fatalf("owner receipt=%q err=%v", outcome, err)
	}
	for _, path := range []string{
		"../../migrations/customer_web/000002_privacy_session_scrub.down.sql",
		"../../migrations/identity/000024_customer_web_erasure_scope.down.sql",
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, rollbackErr := pool.Exec(ctx, string(body)); rollbackErr == nil {
			t.Fatalf("privacy evidence allowed rollback of %s", path)
		}
	}
}

func runCustomerWebPrivacyScrub(ctx context.Context, pool *pgxpool.Pool, request string) (int64, error) {
	var outcome, evidence string
	var rows int64
	err := pool.QueryRow(ctx, `SELECT outcome,evidence_reference,session_rows
		FROM customer_web.privacy_scrub_sessions($1)`, request).Scan(&outcome, &evidence, &rows)
	if err != nil {
		return 0, err
	}
	if outcome != "RETAINED" || evidence != "customer-web-session-scrub:"+request {
		return 0, fmt.Errorf("unexpected customer web privacy evidence %q %q", outcome, evidence)
	}
	return rows, nil
}
