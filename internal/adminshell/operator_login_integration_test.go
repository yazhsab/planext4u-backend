//go:build integration

package adminshell

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

type operatorVerifierStub struct {
	result identity.ProviderAssurance
	err    error
}

func (stub *operatorVerifierStub) VerifyOperator(context.Context, string) (identity.ProviderAssurance, error) {
	return stub.result, stub.err
}

func TestOperatorLoginRequiresVerifiedMFAAndCurrentServerOwnedAccess(t *testing.T) {
	url := os.Getenv("ADMIN_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("ADMIN_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS admin CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS admin CASCADE`) })
	for _, file := range []string{"platform/000001_service_roles", "platform/000008_admin_roles", "admin/000001_admin_control_plane", "admin/000004_operator_accounts", "admin/000005_operator_administration"} {
		data, readErr := os.ReadFile("../../migrations/" + file + ".up.sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	tenant, subject := uuid.NewString(), uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,'firebase-operator','Operator',ARRAY['AUDITOR'],ARRAY['IN'])`, tenant, subject)
	if err != nil {
		t.Fatal(err)
	}
	accounts, _ := NewOperatorAccounts(pool)
	store, _ := NewPostgresSessionStore(pool, func() time.Time { return now })
	verifier := &operatorVerifierStub{result: identity.ProviderAssurance{Subject: "firebase-operator", AuthenticatedAt: now, MFA: true}}
	handler, err := NewOperatorLoginHandler(OperatorLoginConfig{Verifier: verifier, Accounts: accounts, Sessions: store, TenantID: tenant, Origins: []string{"https://localhost:4173"}, Clock: func() time.Time { return now }, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	requestLogin := func(body, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/admin/api/v1/login", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	body := `{"id_token":"verified-provider-token","country":"IN"}`
	if response := requestLogin(body, "https://attacker.test"); response.Code != 403 {
		t.Fatalf("origin accepted: %d", response.Code)
	}
	if response := requestLogin(`{"id_token":"verified-provider-token","country":"IN","roles":["SUPER_ADMIN"]}`, "https://localhost:4173"); response.Code != 422 {
		t.Fatalf("client roles accepted: %d", response.Code)
	}
	verifier.result.MFA = false
	if response := requestLogin(body, "https://localhost:4173"); response.Code != 403 {
		t.Fatalf("MFA bypass: %d", response.Code)
	}
	verifier.result.MFA = true
	verifier.result.AuthenticatedAt = now.Add(-6 * time.Minute)
	if response := requestLogin(body, "https://localhost:4173"); response.Code != 403 {
		t.Fatalf("stale MFA accepted: %d", response.Code)
	}
	verifier.result.AuthenticatedAt = now
	if response := requestLogin(strings.Replace(body, `"IN"`, `"NG"`, 1), "https://localhost:4173"); response.Code != 403 {
		t.Fatalf("country expansion: %d", response.Code)
	}
	response := requestLogin(body, "https://localhost:4173")
	if response.Code != 201 || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login failed: %d %s", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/api/v1/session", nil)
	request.AddCookie(cookie)
	resolver := OperatorSessionResolver{SessionResolver: store, Accounts: accounts}
	session, err := resolver.Resolve(request)
	if err != nil || len(session.Principal.Roles) != 1 || session.Principal.Roles[0] != RoleAuditor || session.Principal.SubjectID != subject {
		t.Fatalf("role injection or identity mismatch: %+v %v", session, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET roles=ARRAY['REGIONAL_STAFF'],revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, tenant, subject); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(request); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("changed operator session retained: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, tenant, subject); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(request); !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("disabled operator cookie accepted: %v", err)
	}
	if response := requestLogin(body, "https://localhost:4173"); response.Code != 403 {
		t.Fatalf("disabled login accepted: %d", response.Code)
	}
	verifier.err = identity.ErrProviderUnavailable
	if response := requestLogin(body, "https://localhost:4173"); response.Code != 503 {
		t.Fatalf("provider failure accepted: %d", response.Code)
	}
}
