//go:build integration

package identity

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPersistentAccountLinkingContactProofAndAuditRollback(t *testing.T) {
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
	// This environment variable belongs exclusively to the disposable verifier.
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
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["role"] = "planext4u_identity_runtime"
	runtime, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	repository, _ := NewPostgresRepository(runtime)
	f, _, v := linkingFixture(t)
	f.service.repository = repository
	a, err := f.service.Exchange(ctx, ExchangeInput{Provider: "firebase", ProviderToken: v.proof.Subject, DeviceID: "persistent-device", Country: "IN"})
	if err != nil {
		t.Fatal(err)
	}
	trusted := trustedFor(a)
	if _, err = f.service.VerifySignInMethods(ctx, trusted, "fresh-provider-token"); err != nil {
		t.Fatal(err)
	}
	var email, phone string
	var version int
	if err = pool.QueryRow(ctx, `SELECT email,phone,version FROM identity.profiles WHERE identity_id=$1`, a.IdentityID).Scan(&email, &phone, &version); err != nil || email != v.proof.VerifiedEmail || phone != v.proof.VerifiedPhone || version != 2 {
		t.Fatalf("%s %s %d %v", email, phone, version, err)
	}
	// A second process/repository sees committed binding/contact evidence.
	restarted, _ := NewPostgresRepository(runtime)
	if err = restarted.ConfirmProviderAccount(ctx, trusted, v.proof, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	foreign := v.proof
	foreign.Subject = "foreign-uid"
	if err = restarted.ConfirmProviderAccount(ctx, trusted, foreign, f.clock.Now()); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	foreignTrusted := trusted
	foreignTrusted.TenantID = "foreign-tenant"
	if err = restarted.ConfirmProviderAccount(ctx, foreignTrusted, v.proof, f.clock.Now()); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	foreignTrusted = trusted
	foreignTrusted.Country = "GB"
	if err = restarted.ConfirmProviderAccount(ctx, foreignTrusted, v.proof, f.clock.Now()); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal(err)
	}
	// Audit failure must roll back a contact change in the same transaction.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION identity.reject_link_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='SIGN_IN_METHODS_VERIFIED' THEN RAISE EXCEPTION 'synthetic audit outage'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_link_audit BEFORE INSERT ON identity.security_events FOR EACH ROW EXECUTE FUNCTION identity.reject_link_audit()`); err != nil {
		t.Fatal(err)
	}
	v.proof.VerifiedEmail = "changed@example.test"
	if _, err = f.service.VerifySignInMethods(ctx, trusted, "fresh-provider-token"); err == nil {
		t.Fatal("audit failure accepted")
	}
	if err = pool.QueryRow(ctx, `SELECT email,version FROM identity.profiles WHERE identity_id=$1`, a.IdentityID).Scan(&email, &version); err != nil || email != "verified@example.test" || version != 2 {
		t.Fatalf("failed audit changed profile %s %d %v", email, version, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_link_audit ON identity.security_events; DROP FUNCTION identity.reject_link_audit()`); err != nil {
		t.Fatal(err)
	}
	// An unverified replacement cannot retain stale verified contact details.
	v.proof.VerifiedEmail = ""
	if _, err = f.service.VerifySignInMethods(ctx, trusted, "fresh-provider-token"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT email,version FROM identity.profiles WHERE identity_id=$1`, a.IdentityID).Scan(&email, &version); err != nil || email != "" || version != 3 {
		t.Fatal(email, version, err)
	}
	if err = f.service.Revoke(ctx, a.Tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err = restarted.ConfirmProviderAccount(ctx, trusted, v.proof, f.clock.Now()); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE identity_id=$1 AND event_type='SIGN_IN_METHODS_VERIFIED'`, a.IdentityID).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	if _, err = runtime.Exec(ctx, `UPDATE identity.security_events SET outcome='TAMPERED' WHERE identity_id=$1`, a.IdentityID); err == nil {
		t.Fatal("runtime rewrote security evidence")
	}
}
