//go:build integration

package social

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This opt-in operation is exercised only against a disposable test database.
// It is never invoked against the persistent local runtime by verification.
func TestSocialEphemeralPrivacyScrubScopedAndFenced(t *testing.T) {
	url := os.Getenv("SOCIAL_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("SOCIAL_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, schema := range []string{"social", "identity"} {
		if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, schema := range []string{"social", "identity"} {
			_, _ = pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		}
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000006_phase5_roles.up.sql",
		"../../migrations/platform/000008_admin_roles.up.sql",
		"../../migrations/social/000001_social_trust.up.sql",
		"../../migrations/social/000002_engagement_media_messaging.up.sql",
		"../../migrations/social/000003_durable_social_runtime.up.sql",
		"../../migrations/social/000004_shares_reward_leases_admin_grant.up.sql",
		"../../migrations/social/000005_ephemeral_privacy_scrub.up.sql",
		"../../migrations/identity/000001_identity.up.sql",
		"../../migrations/identity/000004_account_lifecycle.up.sql",
		"../../migrations/identity/000013_privacy_export_authority.up.sql",
		"../../migrations/identity/000014_deletion_completion_guard.up.sql",
		"../../migrations/identity/000015_deletion_owner_tasks.up.sql",
		"../../migrations/identity/000016_owner_erasure_capabilities.up.sql",
		"../../migrations/identity/000017_deletion_governance_scope.up.sql",
		"../../migrations/identity/000020_owner_erasure_country_scope.up.sql",
		"../../migrations/identity/000025_social_erasure_scope.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	const (
		tenant  = "11111111-1111-4111-8111-111111111111"
		subject = "22222222-2222-4222-8222-222222222222"
		peer    = "33333333-3333-4333-8333-333333333333"
		request = "44444444-4444-4444-8444-444444444444"
		conv    = "55555555-5555-4555-8555-555555555555"
		callID  = "66666666-6666-4666-8666-666666666666"
	)
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at)
	 VALUES($1,$3,'local','social-privacy-subject',$4),($2,$3,'local','social-privacy-peer',$4)`, subject, peer, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests
	 (id,identity_id,status,reason,requested_at,effective_at,country)
	 VALUES($1,$2,'SCHEDULED','synthetic disposable test',$3,$4,'IN')`,
		request, subject, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks(request_id,owner,status,updated_at)
	 VALUES($1,'social','READY',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social.profiles
	 (identity_id,tenant_id,country,handle,display_name,created_at,updated_at)
	 VALUES($1,$3,'IN','privacy_subject','Private user',$4,$4),
	       ($2,$3,'IN','privacy_peer','Peer',$4,$4)`, subject, peer, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social.presence
	 (tenant_id,country,profile_identity_id,state,expires_at,updated_at)
	 VALUES($1,'IN',$2,'ONLINE',$4,$3),($1,'IN',$5,'ONLINE',$4,$3),
	       ($1,'NG',$2,'AWAY',$4,$3)`, tenant, subject, now, now.Add(time.Hour), peer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social.conversations
	 (id,tenant_id,country,participant_ids,requested_by_identity_id,status,created_at,updated_at)
	 VALUES($1,$2,'IN',ARRAY[$3::uuid,$4::uuid],$3,'ACCEPTED',$5,$5)`, conv, tenant, subject, peer, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social.call_sessions
	 (id,tenant_id,country,conversation_id,initiator_identity_id,kind,status,signal_count,expires_at,created_at)
	 VALUES($1,$2,'IN',$3,$4,'AUDIO','CONNECTED',1,$6,$5)`, callID, tenant, conv, subject, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social.call_signals
	 (tenant_id,country,call_id,sender_identity_id,signal_type,payload_digest,created_at)
	 VALUES($1,'IN',$2,$3,'ANSWER',repeat('a',64),$4)`, tenant, callID, peer, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT * FROM social.privacy_scrub_ephemeral($1)`, request); err == nil {
		t.Fatal("social scrub accepted an unconfigured retention policy")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_country_policies
	 (tenant_id,country,owner,revision,policy_version,retention_interval,published_at)
	 VALUES($1,'IN','social',1,'social-v1',interval '0',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.deletion_legal_holds
	 (request_id,owner,reason_reference,recorded_at)
	 VALUES($1,'social','synthetic hold',$2)`, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT * FROM social.privacy_scrub_ephemeral($1)`, request); err == nil {
		t.Fatal("social scrub crossed an unresolved legal hold")
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.deletion_legal_holds SET released_at=clock_timestamp()
	 WHERE request_id=$1`, request); err != nil {
		t.Fatal(err)
	}
	// A writer that has not committed must finish before the scrub can take
	// its table lock. Its activity is removed before the privacy receipt commits.
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `UPDATE social.presence SET state='AWAY'
	 WHERE tenant_id=$1 AND country='IN' AND profile_identity_id=$2`, tenant, subject); err != nil {
		_ = writer.Rollback(ctx)
		t.Fatal(err)
	}
	type result struct {
		outcome, evidence        string
		presence, calls, signals int
		err                      error
	}
	completed := make(chan result, 1)
	go func() {
		var got result
		got.err = pool.QueryRow(ctx, `SELECT outcome,evidence_reference,presence_rows,call_rows,signal_rows
		 FROM social.privacy_scrub_ephemeral($1)`, request).
			Scan(&got.outcome, &got.evidence, &got.presence, &got.calls, &got.signals)
		completed <- got
	}()
	select {
	case got := <-completed:
		_ = writer.Rollback(ctx)
		t.Fatalf("scrub did not wait for the pending write: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var got result
	select {
	case got = <-completed:
	case <-ctx.Done():
		t.Fatal("social scrub did not finish after queued writer")
	}
	if got.err != nil || got.outcome != "RETAINED" || got.evidence != "social-ephemeral-scrub:"+request || got.presence != 1 || got.calls != 1 || got.signals != 1 {
		t.Fatalf("social scrub result: %+v", got)
	}
	var inPresence, ngPresence, peerPresence, signals, activeCalls int
	var callStatus string
	if err := pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM social.presence WHERE tenant_id=$1 AND country='IN' AND profile_identity_id=$2),
	 (SELECT count(*) FROM social.presence WHERE tenant_id=$1 AND country='NG' AND profile_identity_id=$2),
	 (SELECT count(*) FROM social.presence WHERE tenant_id=$1 AND country='IN' AND profile_identity_id=$3),
	 (SELECT count(*) FROM social.call_signals WHERE call_id=$4),
	 (SELECT count(*) FROM social.call_sessions WHERE id=$4 AND status<>'ENDED'),
	 (SELECT status FROM social.call_sessions WHERE id=$4)`, tenant, subject, peer, callID).
		Scan(&inPresence, &ngPresence, &peerPresence, &signals, &activeCalls, &callStatus); err != nil {
		t.Fatal(err)
	}
	if inPresence != 0 || ngPresence != 1 || peerPresence != 1 || signals != 0 || activeCalls != 0 || callStatus != "ENDED" {
		t.Fatalf("scope leaked: in=%d ng=%d peer=%d signals=%d active=%d status=%s", inPresence, ngPresence, peerPresence, signals, activeCalls, callStatus)
	}
	if err := pool.QueryRow(ctx, `SELECT outcome,evidence_reference,presence_rows,call_rows,signal_rows
	 FROM social.privacy_scrub_ephemeral($1)`, request).
		Scan(&got.outcome, &got.evidence, &got.presence, &got.calls, &got.signals); err != nil || got.presence != 1 || got.calls != 1 || got.signals != 1 {
		t.Fatalf("social scrub retry result: %+v", got)
	}
	for _, sql := range []string{
		`INSERT INTO social.presence(tenant_id,country,profile_identity_id,state,expires_at,updated_at)
		 VALUES($1,'IN',$2,'ONLINE',clock_timestamp()+interval '1 minute',clock_timestamp())`,
		`INSERT INTO social.call_signals(tenant_id,country,call_id,sender_identity_id,signal_type,payload_digest,created_at)
		 VALUES($1,'IN',$3,$4,'ICE',repeat('b',64),clock_timestamp())`,
		`UPDATE social.call_sessions SET status='CONNECTED' WHERE id=$3`,
		`UPDATE social.conversations SET participant_ids=ARRAY[$2::uuid,$4::uuid] WHERE id=$5`,
		`UPDATE social.privacy_ephemeral_erasure_runs SET policy_version='forged' WHERE request_id=$6`,
	} {
		if _, err := pool.Exec(ctx, sql, tenant, subject, callID, peer, conv, request); err == nil {
			t.Fatalf("social privacy fence accepted %q", sql)
		}
	}
	var receiptOutcome string
	if err := pool.QueryRow(ctx, `SELECT outcome FROM identity.deletion_owner_receipts
	 WHERE request_id=$1 AND owner='social'`, request).Scan(&receiptOutcome); err != nil || receiptOutcome != "RETAINED" {
		t.Fatalf("social receipt=%q error=%v", receiptOutcome, err)
	}
	roleTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roleTx.Exec(ctx, `SET LOCAL ROLE planext4u_social_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleTx.Exec(ctx, `SELECT * FROM social.privacy_ephemeral_erasure_runs`); err == nil {
		t.Fatal("social runtime read private scrub evidence")
	}
	_ = roleTx.Rollback(ctx)
	roleTx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roleTx.Exec(ctx, `SET LOCAL ROLE planext4u_social_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleTx.Exec(ctx, `SELECT * FROM social.privacy_scrub_ephemeral($1)`, request); err == nil {
		t.Fatal("social runtime invoked opt-in owner scrub")
	}
	_ = roleTx.Rollback(ctx)
	for _, check := range []struct{ path, message string }{
		{"../../migrations/social/000005_ephemeral_privacy_scrub.down.sql", "blocks rollback"},
		{"../../migrations/identity/000025_social_erasure_scope.down.sql", "still installed"},
	} {
		migration, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err == nil || !strings.Contains(err.Error(), check.message) {
			t.Fatalf("used privacy rollback accepted or failed for wrong reason %s: %v", check.path, err)
		}
	}
}
