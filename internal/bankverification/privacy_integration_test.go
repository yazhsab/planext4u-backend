//go:build integration

package bankverification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBankLinkPrivacyScrubRequiresDueCountryPolicyAndHoldRelease(t *testing.T) {
	ctx, pool, runtime := bankIntegrationDatabase(t)
	actor := seedBankActor(t, ctx, pool)
	request := uuid.NewString()
	now := time.Now().UTC()
	_, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
	 (id,identity_id,status,reason,requested_at,effective_at,country)
	 VALUES($1,$2,'SCHEDULED','synthetic bank privacy verification',$3,$4,'IN')`,
		request, actor.OwnerID, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempt := uuid.NewString()
	otherCountry := uuid.NewString()
	otherOwner := uuid.NewString()
	for _, row := range []struct{ id, country, owner, label, hash string }{
		{attempt, "IN", actor.OwnerID, "private-owner", strings.Repeat("a", 64)},
		{otherCountry, "GB", actor.OwnerID, "other-country", strings.Repeat("b", 64)},
		{otherOwner, "IN", uuid.NewString(), "other-owner", strings.Repeat("c", 64)},
	} {
		_, err = pool.Exec(ctx, `INSERT INTO bankverification.link_attempts
		 (id,tenant_id,country,owner_id,application_role,idempotency_hash,status,
		  provider_session_id,account,verification_id,verified_at,created_at,updated_at,expires_at)
		 VALUES($1,$2,$3,$4,'VENDOR',$5,'VERIFIED',$6,$7,$8,$9,$10,$10,$11)`,
			row.id, actor.TenantID, row.country, row.owner, row.hash,
			"session_"+strings.ReplaceAll(row.id, "-", ""),
			`{"reference":"bankref_private","holder_name":"Private Holder","last4":"1234"}`,
			"verification_"+row.label, now.Add(-time.Minute), now.Add(-time.Hour), now.Add(-30*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO bankverification.audit_events
	 (tenant_id,country,owner_id,session_id,attempt_id,action,status,occurred_at)
	 VALUES($1,'IN',$2,$3,$4,'RESOLVED','VERIFIED',$5)`, actor.TenantID,
		actor.OwnerID, actor.SessionID, attempt, now); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err = pool.QueryRow(ctx, `SELECT has_function_privilege('planext4u_bankverification_runtime',
	 'bankverification.privacy_scrub_link_attempts(text)','EXECUTE')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("runtime must not execute privacy scrub: allowed=%v err=%v", allowed, err)
	}
	if _, err = runBankPrivacyScrub(ctx, pool, request); err == nil {
		t.Fatal("bank scrub ran without a country retention policy and READY task")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
	 (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
	 VALUES($1,'IN','bankverification',1,'bank-policy-v1',interval '0',$2)`, actor.TenantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks
	 (request_id,owner,status,updated_at) VALUES($1,'bankverification','READY',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
	 (request_id,owner,reason_reference,recorded_at)
	 VALUES($1,'bankverification','synthetic-bank-hold',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err = runBankPrivacyScrub(ctx, pool, request); err == nil {
		t.Fatal("bank scrub ignored an unresolved legal hold")
	}
	assertBankPrivacyAttempt(t, ctx, pool, attempt, "VERIFIED", true)
	if _, err = pool.Exec(ctx, `UPDATE identity.deletion_legal_holds SET released_at=clock_timestamp(),
	 release_reason_reference='synthetic bank hold released'
	 WHERE request_id=$1`, request); err != nil {
		t.Fatal(err)
	}
	first, err := runBankPrivacyScrub(ctx, pool, request)
	if err != nil || first != 1 {
		t.Fatalf("bank scrub affected=%d err=%v", first, err)
	}
	assertBankPrivacyAttempt(t, ctx, pool, attempt, "EXPIRED", false)
	assertBankPrivacyAttempt(t, ctx, pool, otherCountry, "VERIFIED", true)
	assertBankPrivacyAttempt(t, ctx, pool, otherOwner, "VERIFIED", true)
	second, err := runBankPrivacyScrub(ctx, pool, request)
	if err != nil || second != first {
		t.Fatalf("idempotent retry affected=%d err=%v", second, err)
	}
	service, err := NewPostgresService(runtime, UnavailableLinker(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, actor, "VENDOR", "post-scrub-denied-0001"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("due deletion allowed a new bank link: %v", err)
	}
	foreignCountrySession := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO identity.sessions
	 (id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at)
	 VALUES($1,$2,$3,'GB','synthetic-gb-device','synthetic-gb-device',$4,$5,$5)`,
		foreignCountrySession, actor.OwnerID, actor.TenantID, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	gbActor := actor
	gbActor.Country = "GB"
	gbActor.SessionID = foreignCountrySession
	if _, err = service.Start(ctx, gbActor, "VENDOR", "other-country-denied-0001"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("due account deletion allowed another-country bank link: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE bankverification.link_attempts
	 SET provider_session_id=$2 WHERE id=$1`, attempt,
		"session_"+strings.ReplaceAll(attempt, "-", "")); err == nil {
		t.Fatal("bank proof was reintroduced after scrub")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO bankverification.link_attempts
	 (id,tenant_id,country,owner_id,application_role,idempotency_hash,status,created_at,updated_at,expires_at)
	 VALUES($1,$2,'IN',$3,'RIDER',$4,'CREATING',$5,$5,$6)`, uuid.NewString(), actor.TenantID,
		actor.OwnerID, strings.Repeat("d", 64), now, now.Add(30*time.Minute)); err == nil {
		t.Fatal("new bank attempt was introduced after scrub")
	}
	var outcome, policy, evidence string
	if err = pool.QueryRow(ctx, `SELECT outcome,policy_version,evidence_reference
	 FROM identity.deletion_owner_receipts WHERE request_id=$1 AND owner='bankverification'`, request).
		Scan(&outcome, &policy, &evidence); err != nil || outcome != "RETAINED" || policy != "bank-policy-v1" || evidence != "bankverification-link-scrub:"+request {
		t.Fatalf("bank receipt outcome=%q policy=%q evidence=%q err=%v", outcome, policy, evidence, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM bankverification.audit_events WHERE attempt_id=$1`, attempt).Scan(&count); err != nil || count != 1 {
		t.Fatalf("immutable bank audit count=%d err=%v", count, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE bankverification.privacy_link_scrub_runs SET attempt_rows=0 WHERE request_id=$1`, request); err == nil {
		t.Fatal("bank scrub evidence was mutable")
	}
	if _, err = runtime.Exec(ctx, `SELECT bankverification.privacy_scrub_link_attempts($1)`, request); err == nil {
		t.Fatal("bank runtime invoked owner-only privacy scrub")
	}
}

func runBankPrivacyScrub(ctx context.Context, pool *pgxpool.Pool, request string) (int, error) {
	var outcome, evidence string
	var rows int
	err := pool.QueryRow(ctx, `SELECT outcome,evidence_reference,attempt_rows
	 FROM bankverification.privacy_scrub_link_attempts($1)`, request).Scan(&outcome, &evidence, &rows)
	if err != nil {
		return 0, err
	}
	if outcome != "RETAINED" || evidence != "bankverification-link-scrub:"+request {
		return 0, fmt.Errorf("unexpected bank privacy evidence %q %q", outcome, evidence)
	}
	return rows, nil
}

func assertBankPrivacyAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, wantStatus string, wantProof bool) {
	t.Helper()
	var status, hash string
	var provider, account, verification *string
	if err := pool.QueryRow(ctx, `SELECT status,idempotency_hash,provider_session_id,
	 account::text,verification_id FROM bankverification.link_attempts WHERE id=$1`, id).
		Scan(&status, &hash, &provider, &account, &verification); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || (provider != nil) != wantProof || (account != nil) != wantProof || (verification != nil) != wantProof {
		t.Fatalf("attempt=%s status=%s proof=%v/%v/%v", id, status, provider != nil, account != nil, verification != nil)
	}
	if !wantProof && (len(hash) != 64 || hash == strings.Repeat("a", 64)) {
		t.Fatalf("bank idempotency proof retained: %q", hash)
	}
}
