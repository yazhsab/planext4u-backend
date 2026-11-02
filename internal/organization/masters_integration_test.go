//go:build integration

package organization

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPersistentMasterDataAuthorityHierarchyHistoryAndRollback(t *testing.T) {
	url := os.Getenv("ADMIN_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("ADMIN_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS admin CASCADE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS admin CASCADE`) }()
	for _, file := range []string{"platform/000001_service_roles", "platform/000005_phase4_roles", "platform/000008_admin_roles", "admin/000001_admin_control_plane", "admin/000003_organization", "admin/000004_operator_accounts", "admin/000005_operator_administration", "admin/000011_master_data"} {
		data, readErr := os.ReadFile("../../migrations/" + file + ".up.sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	runtimeConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig.ConnConfig.RuntimeParams["role"] = "planext4u_admin_runtime"
	runtimePool, err := pgxpool.NewWithConfig(ctx, runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	var currentRole string
	if err = runtimePool.QueryRow(ctx, `SELECT current_user`).Scan(&currentRole); err != nil || currentRole != "planext4u_admin_runtime" {
		t.Fatalf("runtime role %q: %v", currentRole, err)
	}
	service, _ := NewService(runtimePool, time.Now)
	operator := func(tenant, country, role string) Actor {
		actor := Actor{TenantID: tenant, Country: country, Subject: uuid.NewString(), MFA: true, OperatorSessionID: uuid.NewString(), OperatorRevision: 1, RequireLiveOperator: true}
		if _, err = pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Synthetic master operator',$4,$5)`, tenant, actor.Subject, "synthetic-"+actor.Subject, []string{role}, []string{country}); err != nil {
			t.Fatal(err)
		}
		digest := strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 2)
		if _, err = pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES($1::text,$2,$3,$4,'Synthetic master operator',$5,$6,$7,clock_timestamp(),ARRAY['pwd','totp'],$1::text,clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, digest, actor.OperatorSessionID, actor.Subject, tenant, []string{role}, []string{country}, country); err != nil {
			t.Fatal(err)
		}
		return actor
	}
	admin := operator(uuid.NewString(), "IN", "SUPER_ADMIN")
	country := operator(admin.TenantID, "US", "COUNTRY_ADMIN")
	foreign := operator(uuid.NewString(), "IN", "COUNTRY_ADMIN")
	regional := operator(admin.TenantID, "IN", "REGIONAL_STAFF")
	auditor := operator(admin.TenantID, "IN", "AUDITOR")
	makeInput := func(kind, code, parent string) MasterChange {
		return MasterChange{Master: Master{ID: uuid.NewString(), Kind: kind, Code: code, ParentID: parent, NameEN: "Teacher", NameTA: "ஆசிரியர்", Active: true}, Reason: "Approved initial master"}
	}
	save := func(a Actor, input MasterChange) Master {
		v, e := service.SaveMaster(ctx, a, input)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	own := save(admin, makeInput("OCCUPATION", "TEACHER", ""))
	_ = save(country, makeInput("OCCUPATION", "TEACHER", ""))
	_ = save(foreign, makeInput("OCCUPATION", "TEACHER", ""))
	page, e := service.Masters(ctx, admin, MasterFilter{Kind: "OCCUPATION", Search: "ஆசிரியர்", Status: "ACTIVE"})
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != own.ID {
		t.Fatalf("scope/search: %+v %v", page, e)
	}
	page, e = service.Masters(ctx, auditor, MasterFilter{Kind: "OCCUPATION", Search: "%"})
	if e != nil || len(page.Items) != 0 {
		t.Fatalf("literal search: %+v %v", page, e)
	}
	for _, a := range []Actor{regional, func() Actor { v := admin; v.Country = "US"; return v }(), func() Actor { v := admin; v.OperatorRevision = 2; return v }(), func() Actor { v := admin; v.TenantID = foreign.TenantID; return v }()} {
		a.PlatformAdmin = true
		if _, e = service.Masters(ctx, a, MasterFilter{Kind: "OCCUPATION"}); !errors.Is(e, ErrForbidden) {
			t.Fatalf("forged read accepted: %v", e)
		}
		if _, e = service.SaveMaster(ctx, a, makeInput("REASON", "FORGED", "")); !errors.Is(e, ErrForbidden) {
			t.Fatalf("forged write accepted: %v", e)
		}
	}
	if _, e = service.SaveMaster(ctx, auditor, makeInput("REASON", "AUDITOR", "")); !errors.Is(e, ErrForbidden) {
		t.Fatalf("auditor write: %v", e)
	}
	if _, e = service.MasterHistory(ctx, foreign, own.ID, 0); !errors.Is(e, ErrNotFound) {
		t.Fatalf("foreign history: %v", e)
	}
	if _, e = service.SaveMaster(ctx, admin, makeInput("OCCUPATION", "TEACHER", "")); !errors.Is(e, ErrConflict) {
		t.Fatalf("duplicate code: %v", e)
	}
	root := save(admin, makeInput("COUNTRY", "IN", ""))
	state := save(admin, makeInput("STATE", "TN", root.ID))
	district := save(admin, makeInput("DISTRICT", "CHENNAI", state.ID))
	city := save(admin, makeInput("CITY", "CHENNAI", district.ID))
	_ = save(admin, makeInput("AREA", "CENTRAL", city.ID))
	for _, input := range []MasterChange{makeInput("CITY", "WRONG_PARENT", state.ID), makeInput("AREA", "FOREIGN_PARENT", uuid.NewString()), makeInput("COUNTRY", "US", "")} {
		if _, e = service.SaveMaster(ctx, admin, input); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid hierarchy: %v", e)
		}
	}
	if _, e = service.SaveMaster(ctx, admin, MasterChange{Master: state, Reason: "Deactivate with active child"}); e != nil {
		t.Fatal(e)
	} // unchanged active value is a valid audited revision
	state.Revision++
	state.Active = false
	if _, e = service.SaveMaster(ctx, admin, MasterChange{Master: state, Reason: "Deactivate with active child"}); !errors.Is(e, ErrConflict) {
		t.Fatalf("active child lost: %v", e)
	}
	changed := own
	changed.NameEN = "Updated teacher"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, saveErr := service.SaveMaster(ctx, admin, MasterChange{Master: changed, Reason: "Concurrent master revision"})
			results <- saveErr
		}()
	}
	wg.Wait()
	close(results)
	good, conflict := 0, 0
	for result := range results {
		if result == nil {
			good++
		} else if errors.Is(result, ErrConflict) {
			conflict++
		} else {
			t.Fatal(result)
		}
	}
	if good != 1 || conflict != 1 {
		t.Fatalf("revision race %d/%d", good, conflict)
	}
	history, e := service.MasterHistory(ctx, admin, own.ID, 0)
	if e != nil || len(history.Items) != 2 || history.Items[0].Definition.Revision != 2 || history.Items[1].Definition.NameEN != "Teacher" {
		t.Fatalf("history: %+v %v", history, e)
	}
	changed.Revision = 2
	if _, err = pool.Exec(ctx, `ALTER TABLE admin.master_data_versions ADD CONSTRAINT synthetic_audit_failure CHECK(reason<>'Reject audit for regression')`); err != nil {
		t.Fatal(err)
	}
	if _, e = service.SaveMaster(ctx, admin, MasterChange{Master: changed, Reason: "Reject audit for regression"}); e == nil {
		t.Fatal("audit failure accepted")
	}
	history, e = service.MasterHistory(ctx, admin, own.ID, 0)
	if e != nil || len(history.Items) != 2 {
		t.Fatalf("audit rollback: %+v %v", history, e)
	}
	// The runtime role cannot rewrite or remove committed evidence.
	if _, e = runtimePool.Exec(ctx, `DELETE FROM admin.master_data_versions`); e == nil {
		t.Fatal("runtime deleted history")
	}
	if _, e = runtimePool.Exec(ctx, `UPDATE admin.master_data_versions SET reason='Rewritten audit record'`); e == nil {
		t.Fatal("runtime rewrote history")
	}
	// Revocation queued ahead of a command wins; no mutation may cross commit.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT session_id FROM admin.sessions WHERE session_id=$1 FOR UPDATE`, admin.OperatorSessionID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, saveErr := service.SaveMaster(ctx, admin, makeInput("REASON", "REVOKED", ""))
		result <- saveErr
	}()
	waiting := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE usename=current_user AND wait_event_type='Lock' AND query LIKE 'SELECT tenant_id::text,subject_id::text,selected_country,operator_revision%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		_ = blocker.Rollback(ctx)
		t.Fatal("command did not queue behind source session")
	}
	if _, err = blocker.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, admin.OperatorSessionID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if e = <-result; !errors.Is(e, ErrForbidden) {
		t.Fatalf("revoked command: %v", e)
	}
}
