//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const fieldOperatorInitialSession = "b862aa4b-31e1-4897-b4dc-e301445cc5b6"
const fieldOperatorLogoutSession = "f5e0603e-b63c-4297-af96-b7bf84188bcf"
const fieldOperatorCurrentSession = "ed723d36-9991-4c65-a568-84203f7d4140"
const fieldOperatorStaleSession = "485ca37a-3f01-4a55-a19f-99ef5b411f68"

func seedFieldOperatorSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, revision int64, age time.Duration) {
	t.Helper()
	digest := strings.Repeat(strings.ReplaceAll(id, "-", ""), 2)
	_, err := pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES($1,$2,$3,$4,'Synthetic field operator',ARRAY['REGIONAL_STAFF'],ARRAY['IN'],'IN',clock_timestamp()-($6::double precision*interval '1 second'),ARRAY['pwd','totp'],repeat('s',32),clock_timestamp()+interval '1 hour',clock_timestamp(),$5)`, digest, id, fulfillmentField, fulfillmentTenantOne, revision, age.Seconds())
	if err != nil {
		t.Fatal(err)
	}
}

func verifyFieldOperatorRevocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, field Actor) {
	t.Helper()
	for _, proof := range []struct {
		query string
		args  []any
	}{
		{`SELECT revision FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 FOR UPDATE`, []any{field.TenantID, field.Subject}},
		{`SELECT session_id FROM admin.sessions WHERE session_id=$1 FOR UPDATE`, []any{field.OperatorSessionID}},
	} {
		ownerTx, lockErr := service.pool.Begin(ctx)
		if lockErr != nil {
			t.Fatal(lockErr)
		}
		var allowed bool
		if lockErr = ownerTx.QueryRow(ctx, `SELECT admin.lock_fulfillment_operator($1,$2,$3,$4,$5)`, field.TenantID, field.Country, field.Subject, field.OperatorSessionID, field.OperatorRevision).Scan(&allowed); lockErr != nil || !allowed {
			_ = ownerTx.Rollback(ctx)
			t.Fatalf("actual runtime operator gate=%t %v", allowed, lockErr)
		}
		locked := make(chan error, 1)
		go func() {
			tag, e := pool.Exec(ctx, proof.query, proof.args...)
			if e == nil && tag.RowsAffected() != 1 {
				e = errors.New("operator/source row missing")
			}
			locked <- e
		}()
		select {
		case e := <-locked:
			_ = ownerTx.Rollback(ctx)
			t.Fatalf("revocation acquired a row before owning completion: %v", e)
		case <-time.After(50 * time.Millisecond):
		}
		if lockErr = ownerTx.Commit(ctx); lockErr != nil {
			t.Fatal(lockErr)
		}
		select {
		case e := <-locked:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("operator/source row remained locked after owning completion")
		}
	}
	point := Point{Latitude: 13.0827, Longitude: 80.2707}
	// An owner command queued behind an uncommitted directory suspension must
	// observe the committed new revision rather than its earlier BFF snapshot.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE admin.operator_accounts SET active=false,revision=2 WHERE tenant_id=$1 AND subject_id=$2`, field.TenantID, field.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM admin.sessions WHERE tenant_id=$1 AND subject_id=$2`, field.TenantID, field.Subject); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, e := service.FieldCheckIn(field, "field-operator-suspension-race", fulfillmentTerritory, point)
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("directory lock bypassed: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ErrForbidden) {
			t.Fatalf("queued directory suspension=%v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("directory suspension not observed")
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET active=true,revision=3 WHERE tenant_id=$1 AND subject_id=$2`, field.TenantID, field.Subject); err != nil {
		t.Fatal(err)
	}
	seedFieldOperatorSession(t, ctx, pool, fieldOperatorLogoutSession, 3, 0)
	if _, _, err = service.FieldCheckIn(field, "scoped-field-proof-command", fulfillmentTerritory, point); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reactivation revived old session: %v", err)
	}
	current := field
	current.OperatorSessionID = fieldOperatorLogoutSession
	current.OperatorRevision = 3
	if _, replay, e := service.FieldCheckIn(current, "scoped-field-proof-command", fulfillmentTerritory, point); e != nil || !replay {
		t.Fatalf("fresh explicit session cannot recover prior result: replay=%t err=%v", replay, e)
	}
	// Logout/country changes lock the actual source session; the owning command
	// cannot outlive a completed logout using a previously resolved cookie.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, fieldOperatorLogoutSession); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _, e := service.FieldCheckIn(current, "field-operator-logout-race", fulfillmentTerritory, point)
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("session lock bypassed: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ErrForbidden) {
			t.Fatalf("queued logout=%v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("logout not observed")
	}
	seedFieldOperatorSession(t, ctx, pool, fieldOperatorCurrentSession, 3, 0)
	validated := field
	validated.OperatorSessionID = fieldOperatorCurrentSession
	validated.OperatorRevision = 3
	if _, replay, e := service.FieldCheckIn(validated, "scoped-field-proof-command", fulfillmentTerritory, point); e != nil || !replay {
		t.Fatalf("current operator baseline replay=%t err=%v", replay, e)
	}
	for name, change := range map[string]func(*Actor){
		"tenant":         func(a *Actor) { a.TenantID = fulfillmentTenantTwo },
		"country":        func(a *Actor) { a.Country = "GB" },
		"subject":        func(a *Actor) { a.Subject = fulfillmentRiderOne },
		"revision":       func(a *Actor) { a.OperatorRevision = 2 },
		"source-session": func(a *Actor) { a.OperatorSessionID = fieldOperatorInitialSession },
	} {
		candidate := validated
		change(&candidate)
		if _, _, e := service.FieldCheckIn(candidate, "field-scope-negative-"+name, fulfillmentTerritory, point); !errors.Is(e, ErrForbidden) {
			t.Fatalf("operator %s scope=%v", name, e)
		}
	}
	for _, args := range [][]any{
		{validated.TenantID, validated.Country, validated.Subject, validated.OperatorSessionID, nil},
		{validated.TenantID, nil, validated.Subject, validated.OperatorSessionID, validated.OperatorRevision},
		{validated.TenantID, validated.Country, validated.Subject, nil, validated.OperatorRevision},
	} {
		var allowed bool
		if e := service.pool.QueryRow(ctx, `SELECT admin.lock_fulfillment_operator($1,$2,$3,$4,$5)`, args...).Scan(&allowed); e != nil || allowed {
			t.Fatalf("nullable operator proof accepted=%t err=%v", allowed, e)
		}
	}
	seedFieldOperatorSession(t, ctx, pool, fieldOperatorStaleSession, 3, 6*time.Minute)
	stale := current
	stale.OperatorSessionID = fieldOperatorStaleSession
	if _, _, err = service.FieldCheckIn(stale, "field-operator-stale-mfa", fulfillmentTerritory, point); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale operator MFA=%v", err)
	}
	missing := current
	missing.OperatorSessionID = ""
	if _, _, err = service.FieldCheckIn(missing, "field-operator-missing-session", fulfillmentTerritory, point); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing operator session=%v", err)
	}
	var accounts, sessions, execute bool
	if err = pool.QueryRow(ctx, `SELECT has_table_privilege('planext4u_fulfillment_runtime','admin.operator_accounts','SELECT'),has_table_privilege('planext4u_fulfillment_runtime','admin.sessions','SELECT'),has_function_privilege('planext4u_fulfillment_runtime','admin.lock_fulfillment_operator(uuid,character,uuid,uuid,bigint)','EXECUTE')`).Scan(&accounts, &sessions, &execute); err != nil || accounts || sessions || !execute {
		t.Fatalf("operator privileges tables=%t/%t function=%t err=%v", accounts, sessions, execute, err)
	}
}
