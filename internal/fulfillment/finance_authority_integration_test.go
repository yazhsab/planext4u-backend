//go:build integration

package fulfillment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The shared fulfillment lifecycle uses a disposable database. Its finance
// fixtures are real admin directory/session rows rather than trusted role
// headers, and this helper never touches the persistent local ledger.
func prepareFinanceOperatorAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (Actor, Actor) {
	t.Helper()
	for _, file := range []string{"000016_operator_finance_role", "000020_fulfillment_finance_authority"} {
		body, err := os.ReadFile("../../migrations/admin/" + file + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply admin %s: %v", file, err)
		}
	}
	finance := func(subject string) Actor {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Synthetic finance operator',ARRAY['COUNTRY_ADMIN','FINANCE'],ARRAY['IN'])`, fulfillmentTenantOne, subject, "finance-fixture-"+subject); err != nil {
			t.Fatal(err)
		}
		return Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: subject, Roles: []string{"FINANCE"}, MFAVerified: true, OperatorSessionID: insertFinanceSession(t, ctx, pool, subject, 1), OperatorRevision: 1}
	}
	return finance(fulfillmentFinanceOne), finance(fulfillmentFinanceTwo)
}

func insertFinanceSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, subject string, revision int64) string {
	t.Helper()
	session := uuid.NewString()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(session)))
	if _, err := pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES($1,$2,$3,$4,'Synthetic finance operator',ARRAY['COUNTRY_ADMIN','FINANCE'],ARRAY['IN'],'IN',clock_timestamp(),ARRAY['pwd','totp'],repeat('f',32),clock_timestamp()+interval '1 hour',clock_timestamp(),$5)`, digest, session, subject, fulfillmentTenantOne, revision); err != nil {
		t.Fatal(err)
	}
	return session
}

func verifyFinanceReadAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, finance, country Actor, accountID, payoutID string, payoutRevision int64) {
	t.Helper()
	if entries, err := service.Ledger(finance, accountID); err != nil || len(entries) != 1 {
		t.Fatalf("current finance ledger read: %+v %v", entries, err)
	}
	if payouts, err := service.Payouts(finance, accountID); err != nil || len(payouts) != 1 {
		t.Fatalf("current finance payout read: %+v %v", payouts, err)
	}
	if _, err := service.Reconcile(finance, accountID); err != nil {
		t.Fatalf("current finance reconciliation: %v", err)
	}
	if entries, err := service.Ledger(country, accountID); err != nil || len(entries) != 1 {
		t.Fatalf("current country admin ledger read: %+v %v", entries, err)
	}
	// This key already belongs to the rider delivery ledger. The authorized
	// idempotent readback tests the seed authority without adding money rows.
	if entry, err := service.SeedSettlement(finance, accountID, fulfillmentTask, "RIDER_DELIVERY_EARNING", Money{AmountMinor: 1000, Currency: "INR"}); err != nil || entry.AccountID != accountID || entry.ReferenceID != fulfillmentTask {
		t.Fatalf("current finance settlement seed readback: %+v %v", entry, err)
	}
	withoutSource := finance
	withoutSource.OperatorSessionID = ""
	if _, err := service.SeedSettlement(withoutSource, accountID, fulfillmentTask, "RIDER_DELIVERY_EARNING", Money{AmountMinor: 1000, Currency: "INR"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked finance seed replay escaped source lock: %v", err)
	}
	if _, _, err := service.ExecutePayout(withoutSource, "finance-execute-denied-001", payoutID, payoutRevision, "synthetic_provider_ref", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manual payout execute without source: %v", err)
	}
	forgedFinance := country
	forgedFinance.Roles = []string{"FINANCE"}
	if _, _, err := service.ApprovePayout(forgedFinance, "finance-forged-approval-001", payoutID, payoutRevision, "Country role must not impersonate finance"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("country admin forged finance approval: %v", err)
	}
	for name, alter := range map[string]func(*Actor){
		"missing-source":    func(a *Actor) { a.OperatorSessionID = "" },
		"stale-revision":    func(a *Actor) { a.OperatorRevision++ },
		"foreign-country":   func(a *Actor) { a.Country = "NG" },
		"foreign-tenant":    func(a *Actor) { a.TenantID = fulfillmentTenantTwo },
		"different-subject": func(a *Actor) { a.Subject = fulfillmentFinanceTwo },
		"forged-super-role": func(a *Actor) { a.Roles = []string{"SUPER_ADMIN"} },
		"regional-role":     func(a *Actor) { a.Roles = []string{"REGIONAL_ADMIN"} },
		"missing-mfa":       func(a *Actor) { a.MFAVerified = false },
	} {
		actor := finance
		alter(&actor)
		for action, read := range map[string]func() error{
			"ledger":         func() error { _, err := service.Ledger(actor, accountID); return err },
			"payouts":        func() error { _, err := service.Payouts(actor, accountID); return err },
			"reconciliation": func() error { _, err := service.Reconcile(actor, accountID); return err },
		} {
			if err := read(); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrMFARequired) {
				t.Fatalf("%s %s escaped finance source scope: %v", name, action, err)
			}
		}
	}
	// Role and MFA headers by themselves are insufficient over the actual HTTP
	// owner route; the missing source session is denied before any money rows.
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/settlements/ledger?account_id="+accountID, nil)
	request.Header.Set("X-Planext4u-Tenant", finance.TenantID)
	request.Header.Set("X-Planext4u-Country", finance.Country)
	request.Header.Set("X-Planext4u-Subject", finance.Subject)
	request.Header.Set("X-Planext4u-Roles", "FINANCE")
	request.Header.Set("X-Planext4u-MFA", "verified")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), accountID) {
		t.Fatalf("HTTP finance role-only read: %d %s", response.Code, response.Body.String())
	}
	var direct, execute bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_fulfillment_runtime','admin.operator_accounts','SELECT'),has_function_privilege('planext4u_fulfillment_runtime','admin.lock_fulfillment_finance_operator(uuid,character,uuid,uuid,bigint,text,text)','EXECUTE')`).Scan(&direct, &execute); err != nil || direct || !execute {
		t.Fatalf("finance authority privilege boundary: direct=%t execute=%t err=%v", direct, execute, err)
	}
}

func verifyFinanceRevokedReplay(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, finance Actor, payoutID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, finance.OperatorSessionID); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := service.ApprovePayout(finance, "payout-approve-postgres-001", payoutID, 1, "First finance approval completed"); !errors.Is(err, ErrForbidden) || replay {
		t.Fatalf("revoked first approver replay: replay=%t err=%v", replay, err)
	}
}

func verifyFinanceQueuedRevocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, finance Actor, payoutID string, revision int64) Actor {
	t.Helper()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT revision FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 FOR UPDATE`, finance.TenantID, finance.Subject); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, e := service.ApprovePayout(finance, "payout-queued-revocation-001", payoutID, revision, "Queued second approval must be denied")
		result <- e
	}()
	waiting := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND position('lock_fulfillment_finance_operator' in query)>0)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("finance approval did not queue behind current directory lock")
	}
	if _, err = blocker.Exec(ctx, `UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, finance.TenantID, finance.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, finance.OperatorSessionID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("queued approval survived directory revocation: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET active=true,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2 AND revision=2`, finance.TenantID, finance.Subject); err != nil {
		t.Fatal(err)
	}
	finance.OperatorRevision = 3
	finance.OperatorSessionID = insertFinanceSession(t, ctx, pool, finance.Subject, finance.OperatorRevision)
	return finance
}
