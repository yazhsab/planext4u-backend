//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/organization"
)

func TestPersistentOccupationAdminToProfileExportAndRetirement(t *testing.T) {
	url := os.Getenv("IDENTITY_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("IDENTITY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS identity CASCADE; DROP SCHEMA IF EXISTS admin CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS identity CASCADE; DROP SCHEMA IF EXISTS admin CASCADE`)
	})
	for _, file := range []string{"platform/000001_service_roles", "platform/000002_commerce_roles", "platform/000003_transaction_roles", "platform/000004_booking_roles", "platform/000005_phase4_roles", "platform/000006_phase5_roles", "platform/000007_transaction_runtime", "platform/000008_admin_roles", "identity/000001_identity", "identity/000002_profile_contacts", "identity/000003_profile_locales", "identity/000004_account_lifecycle", "identity/000005_security_evidence_append_only", "identity/000006_scoped_consent_decisions", "identity/000008_contact_matching", "identity/000009_profile_occupations", "admin/000001_admin_control_plane", "admin/000003_organization", "admin/000004_operator_accounts", "admin/000005_operator_administration", "admin/000011_master_data", "admin/000012_occupation_reference_access"} {
		data, readErr := os.ReadFile("../../migrations/" + file + ".up.sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	rolePool := func(role string) *pgxpool.Pool {
		configuration, parseErr := pgxpool.ParseConfig(url)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		configuration.ConnConfig.RuntimeParams["role"] = role
		value, openErr := pgxpool.NewWithConfig(ctx, configuration)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(value.Close)
		return value
	}
	adminPool, identityPool, configurationPool := rolePool("planext4u_admin_runtime"), rolePool("planext4u_identity_runtime"), rolePool("planext4u_configuration_runtime")
	tenant := uuid.NewString()
	admin := organization.Actor{TenantID: tenant, Country: "IN", Subject: uuid.NewString(), MFA: true, OperatorSessionID: uuid.NewString(), OperatorRevision: 1, RequireLiveOperator: true}
	digest := strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 2)
	if _, err = pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Synthetic occupation admin',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'])`, tenant, admin.Subject, "synthetic-"+admin.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES($1::text,$2,$3,$4,'Synthetic occupation admin',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'],'IN',clock_timestamp(),ARRAY['totp'],$1::text,clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, digest, admin.OperatorSessionID, admin.Subject, tenant); err != nil {
		t.Fatal(err)
	}
	masters, _ := organization.NewService(adminPool, time.Now)
	occupation, err := masters.SaveMaster(ctx, admin, organization.MasterChange{Master: organization.Master{ID: uuid.NewString(), Kind: "OCCUPATION", Code: "TEACHER", NameEN: "Teacher", NameTA: "ஆசிரியர்", Active: true}, Reason: "Approved profile occupation"})
	if err != nil {
		t.Fatal(err)
	}
	directory, _ := configcms.NewPostgresOccupationDirectory(configurationPool)
	page, err := directory.Occupations(ctx, tenant, "IN", "ஆசிரியர்", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != occupation.ID {
		t.Fatalf("published directory %+v: %v", page, err)
	}
	for _, scope := range [][2]string{{tenant, "US"}, {uuid.NewString(), "IN"}} {
		value, readErr := directory.Occupations(ctx, scope[0], scope[1], "", "")
		if readErr != nil || len(value.Items) != 0 {
			t.Fatalf("foreign directory %+v: %v", value, readErr)
		}
	}
	if _, err = configurationPool.Exec(ctx, `SELECT * FROM admin.master_data`); err == nil {
		t.Fatal("configuration read private table directly")
	}
	if _, err = identityPool.Exec(ctx, `SELECT * FROM admin.master_data`); err == nil {
		t.Fatal("identity read private table directly")
	}
	repository, _ := NewPostgresRepository(identityPool)
	hasher, err := NewHMACRefreshHasher([]byte("synthetic-occupation-integration-key-32bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{TenantID: tenant, AllowedCountries: []string{"IN", "US"}, SessionTTL: time.Hour, Repository: repository, ProviderVerifier: &staticProviderVerifier{}, TokenIssuer: &fakeTokenIssuer{}, RefreshFactory: &testRefreshFactory{}, RefreshHasher: hasher, Now: time.Now, IDFactory: (&testIDFactory{}).New})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.Exchange(ctx, ExchangeInput{Provider: "local", ProviderToken: "occupation-customer", DeviceID: "occupation-device", Country: "IN"})
	if err != nil {
		t.Fatal(err)
	}
	trusted := trustedFor(auth)
	handler, err := NewHandler(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := trustedHeaders(auth)
	initialResponse := identityRequest(handler, http.MethodGet, "/v1/me/occupation", "", headers)
	var initial OccupationSelection
	if err = json.Unmarshal(initialResponse.Body.Bytes(), &initial); err != nil || initialResponse.Code != http.StatusOK || initialResponse.Header().Get("Cache-Control") != "no-store" || initial.Revision != 0 || initial.Occupation != nil {
		t.Fatalf("initial HTTP selection %d %s: %v", initialResponse.Code, initialResponse.Body.String(), err)
	}
	saveResponse := identityRequest(handler, http.MethodPut, "/v1/me/occupation", `{"occupation_id":"`+occupation.ID+`","expected_revision":0}`, headers)
	var saved OccupationSelection
	if err = json.Unmarshal(saveResponse.Body.Bytes(), &saved); err != nil || saveResponse.Code != http.StatusOK || saveResponse.Header().Get("Cache-Control") != "no-store" || saved.Revision != 1 || saved.Occupation == nil || saved.Occupation.NameTA != "ஆசிரியர்" {
		t.Fatalf("saved HTTP selection %d %s: %v", saveResponse.Code, saveResponse.Body.String(), err)
	}
	restarted, _ := NewPostgresRepository(identityPool)
	readback, err := restarted.Occupation(ctx, trusted, time.Now())
	if err != nil || readback.Occupation == nil || readback.Occupation.ID != occupation.ID {
		t.Fatalf("restart %+v: %v", readback, err)
	}
	for _, bad := range []TrustedIdentity{func() TrustedIdentity { v := trusted; v.Country = "US"; return v }(), func() TrustedIdentity { v := trusted; v.TenantID = uuid.NewString(); return v }(), func() TrustedIdentity { v := trusted; v.Subject = "another-person"; return v }()} {
		if _, err = restarted.UpdateOccupation(ctx, bad, OccupationUpdate{OccupationID: occupation.ID, ExpectedRevision: 1}, time.Now()); err == nil {
			t.Fatal("foreign principal changed occupation")
		}
	}
	occupation.Active = false
	if _, err = masters.SaveMaster(ctx, admin, organization.MasterChange{Master: occupation, Reason: "Retire occupation safely"}); err != nil {
		t.Fatal(err)
	}
	page, err = directory.Occupations(ctx, tenant, "IN", "", "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("retired directory: %+v %v", page, err)
	}
	retained, err := restarted.Occupation(ctx, trusted, time.Now())
	if err != nil || retained.Occupation == nil || retained.Occupation.MasterRevision != 1 {
		t.Fatalf("retired reference lost: %+v %v", retained, err)
	}
	unchanged, err := service.UpdateOccupation(ctx, trusted, OccupationUpdate{OccupationID: occupation.ID, ExpectedRevision: 1})
	if err != nil || unchanged.Revision != 1 || unchanged.Occupation == nil || unchanged.Occupation.MasterRevision != 1 {
		t.Fatalf("unchanged retired selection must retain its revision and snapshot: %+v %v", unchanged, err)
	}
	cleared, err := service.UpdateOccupation(ctx, trusted, OccupationUpdate{ExpectedRevision: 1})
	if err != nil || cleared.Revision != 2 || cleared.Occupation != nil {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	if _, err = service.UpdateOccupation(ctx, trusted, OccupationUpdate{OccupationID: occupation.ID, ExpectedRevision: 2}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("retired selection allowed: %v", err)
	}
	occupation.Active = true
	occupation.Revision = 2
	if _, err = masters.SaveMaster(ctx, admin, organization.MasterChange{Master: occupation, Reason: "Restore occupation safely"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, writeErr := service.UpdateOccupation(ctx, trusted, OccupationUpdate{OccupationID: occupation.ID, ExpectedRevision: 2})
			results <- writeErr
		}()
	}
	wg.Wait()
	close(results)
	good, conflicts := 0, 0
	for result := range results {
		if result == nil {
			good++
		} else if errors.Is(result, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(result)
		}
	}
	if good != 1 || conflicts != 1 {
		t.Fatalf("duplicate selection race %d/%d", good, conflicts)
	}
	exported, err := service.ExportDataVersion(ctx, trusted, 3)
	if err != nil || exported.OccupationReferences == nil || len(*exported.OccupationReferences) != 1 || (*exported.OccupationReferences)[0].Occupation.ID != occupation.ID {
		t.Fatalf("portable occupation %+v: %v", exported.OccupationReferences, err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE identity.security_events ADD CONSTRAINT synthetic_occupation_audit_failure CHECK(event_type<>'PROFILE_OCCUPATION_UPDATED') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.UpdateOccupation(ctx, trusted, OccupationUpdate{ExpectedRevision: 3}); err == nil {
		t.Fatal("missing audit committed")
	}
	retained, err = restarted.Occupation(ctx, trusted, time.Now())
	if err != nil || retained.Revision != 3 || retained.Occupation == nil {
		t.Fatalf("audit rollback %+v: %v", retained, err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE identity.security_events DROP CONSTRAINT synthetic_occupation_audit_failure`); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM identity.sessions WHERE id=$1 FOR UPDATE`, trusted.Session); err != nil {
		t.Fatal(err)
	}
	queued := make(chan error, 1)
	go func() {
		_, writeErr := restarted.UpdateOccupation(ctx, trusted, OccupationUpdate{ExpectedRevision: 3}, time.Now())
		queued <- writeErr
	}()
	select {
	case result := <-queued:
		t.Fatalf("source lock bypassed: %v", result)
	case <-time.After(80 * time.Millisecond):
	}
	if _, err = blocker.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, trusted.Session); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-queued; !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked source command committed: %v", err)
	}
}
