//go:build integration

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contactDirectoryStub struct {
	phones map[string]string
	err    error
	before func()
}

func (d *contactDirectoryStub) ContactPhones(context.Context, []string) (map[string]string, error) {
	if d.before != nil {
		d.before()
	}
	return d.phones, d.err
}

func TestPersistentContactMatchingConsentProofRecoveryAndErasure(t *testing.T) {
	databaseURL := os.Getenv("IDENTITY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("IDENTITY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, schema := range []string{"identity", "configuration"} {
		if _, err = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS identity CASCADE; DROP SCHEMA IF EXISTS configuration CASCADE`)
	}()
	for _, file := range []string{"platform/000001_service_roles", "platform/000002_commerce_roles", "platform/000003_transaction_roles", "platform/000004_booking_roles", "platform/000005_phase4_roles", "platform/000006_phase5_roles", "platform/000007_transaction_runtime", "configuration/000001_configuration", "configuration/000006_contact_matching_policy", "identity/000001_identity", "identity/000002_profile_contacts", "identity/000003_profile_locales", "identity/000004_account_lifecycle", "identity/000005_security_evidence_append_only", "identity/000006_scoped_consent_decisions", "identity/000008_contact_matching"} {
		body, e := os.ReadFile("../../migrations/" + file + ".up.sql")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(body)); e != nil {
			t.Fatalf("%s: %v", file, e)
		}
	}
	config, err := pgxpool.ParseConfig(databaseURL)
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
	if err = repository.ContactReady(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 5, 0, 0, 0, time.UTC)
	tenant := "a5e5b430-7adc-4219-9e55-f8f29370915f"
	if _, err = pool.Exec(ctx, `INSERT INTO configuration.snapshots VALUES($1,'IN',1,'{"consent_policies":[{"purpose":"CONTACT_MATCHING","policy_version":"contacts-v1"}]}',$2)`, tenant, now); err != nil {
		t.Fatal(err)
	}
	settings, err := validatedContactSettings(ContactMatchingSettings{HMACKey: bytes.Repeat([]byte{91}, 32), KeyID: "key1", SnapshotTTL: time.Hour, MaxContacts: 100})
	if err != nil {
		t.Fatal(err)
	}
	phones := map[string]string{"alice": "+919876543210", "bob": "+919876543211", "charlie": "+919876543212"}
	directory := &contactDirectoryStub{phones: phones}
	actors := map[string]TrustedIdentity{}
	for _, id := range []string{"alice", "bob", "charlie"} {
		account, session, e := repository.StartSession(ctx, StartSessionParams{Provider: ProviderIdentity{Provider: "firebase", Subject: id}, IdentityID: id, SessionID: id + "-session", TenantID: tenant, Country: "IN", DeviceID: id + "-device", DeviceReference: id + "-reference", RefreshDigest: strings.Repeat(id[:1], 64), RefreshExpires: now.Add(24 * time.Hour), Now: now})
		if e != nil {
			t.Fatal(e)
		}
		actors[id] = TrustedIdentity{Subject: account.ID, Session: session.ID, TenantID: tenant, Country: "IN"}
	}
	grant := func(id string, granted bool) {
		t.Helper()
		var current int64
		if e := pool.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM identity.consent_evidence WHERE identity_id=$1 AND country='IN' AND purpose='CONTACT_MATCHING'`, id).Scan(&current); e != nil {
			t.Fatal(e)
		}
		_, e := repository.RecordConsent(ctx, id, Consent{EvidenceID: id + "-consent-" + time.Now().Format("150405.000000000"), Country: "IN", Purpose: ConsentContactMatching, Granted: granted, PolicyVersion: "contacts-v1", ExpectedVersion: &current, RecordedAt: now}, now)
		if e != nil {
			t.Fatal(e)
		}
	}
	snapshot := func(id string, revision int64, contacts ...string) (contactSnapshot, ProviderAccount) {
		t.Helper()
		proof := ProviderAccount{Subject: id, AuthenticatedAt: now, VerifiedPhone: phones[id], Methods: []string{"phone"}}
		input, e := settings.prepare(tenant, proof, ContactSnapshotRequest{PolicyVersion: "contacts-v1", ExpectedRevision: revision, Phones: contacts}, now)
		if e != nil {
			t.Fatal(e)
		}
		return input, proof
	}
	sync := func(id string, revision int64, contacts ...string) ContactMatchingState {
		t.Helper()
		input, proof := snapshot(id, revision, contacts...)
		state, e := repository.PutContactSnapshot(ctx, actors[id], proof, input, settings, now)
		if e != nil {
			t.Fatal(e)
		}
		return state
	}
	input, proof := snapshot("alice", 0, phones["bob"])
	if _, err = repository.PutContactSnapshot(ctx, actors["alice"], proof, input, settings, now); !errors.Is(err, ErrContactConsent) {
		t.Fatalf("missing consent: %v", err)
	}
	grant("alice", true)
	state := sync("alice", 0, phones["bob"], phones["charlie"])
	if state.Status != "READY" || state.Revision != 1 || state.ContactCount != 2 {
		t.Fatalf("%+v", state)
	}
	matches, err := repository.ContactMatches(ctx, actors["alice"], settings, 1, "", 1, directory, now)
	if err != nil || len(matches.Items) != 0 {
		t.Fatalf("one-sided address book disclosed: %+v %v", matches, err)
	}
	grant("bob", true)
	sync("bob", 0, phones["alice"])
	grant("charlie", true)
	sync("charlie", 0, phones["alice"])
	matches, err = repository.ContactMatches(ctx, actors["alice"], settings, 1, "", 1, directory, now)
	if err != nil || len(matches.Items) != 1 || matches.Items[0].IdentityID != "bob" || matches.NextCursor != "bob" {
		t.Fatalf("page1 %+v %v", matches, err)
	}
	second, err := repository.ContactMatches(ctx, actors["alice"], settings, 1, matches.NextCursor, 1, directory, now)
	if err != nil || len(second.Items) != 1 || second.Items[0].IdentityID != "charlie" || second.NextCursor != "" {
		t.Fatalf("page2 %+v %v", second, err)
	}
	encoded, _ := json.Marshal(matches)
	for _, private := range []string{phones["alice"], phones["bob"], "phone_hash", "provider_subject", "key_id"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("private claim leaked")
		}
	}
	// A restart sees committed graph/revision, and mutable profile phone cannot
	// impersonate a matching provider identity.
	restarted, _ := NewPostgresRepository(runtime)
	if _, err = pool.Exec(ctx, `UPDATE identity.profiles SET phone=$1 WHERE identity_id='bob'`, phones["charlie"]); err != nil {
		t.Fatal(err)
	}
	matches, err = restarted.ContactMatches(ctx, actors["alice"], settings, 1, "", 50, directory, now)
	if err != nil || len(matches.Items) != 2 {
		t.Fatalf("profile phone affected proof: %+v %v", matches, err)
	}
	directory.phones = map[string]string{"alice": phones["alice"], "bob": "+919876543299", "charlie": phones["charlie"]}
	matches, err = restarted.ContactMatches(ctx, actors["alice"], settings, 1, "", 50, directory, now)
	if err != nil || len(matches.Items) != 1 || matches.Items[0].IdentityID != "charlie" {
		t.Fatalf("stale provider phone disclosed: %+v %v", matches, err)
	}
	directory.err = errors.New("provider outage")
	if _, err = restarted.ContactMatches(ctx, actors["alice"], settings, 1, "", 50, directory, now); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	directory.err = nil
	directory.phones = phones
	// Withdrawal must wait for an already-authorized provider read to commit.
	// The test observes PostgreSQL's actual advisory-lock wait, then proves no
	// later request can disclose the withdrawn participant.
	entered, release := make(chan struct{}), make(chan struct{})
	directory.before = func() { close(entered); <-release }
	readDone := make(chan error, 1)
	go func() {
		_, e := restarted.ContactMatches(ctx, actors["alice"], settings, 1, "", 50, directory, now)
		readDone <- e
	}()
	<-entered
	withdrawDone := make(chan error, 1)
	go func() {
		_, e := restarted.RecordConsent(ctx, "bob", Consent{EvidenceID: "bob-concurrent-withdrawal", Country: "IN", Purpose: ConsentContactMatching, Granted: false, PolicyVersion: "contacts-v1", RecordedAt: now}, now)
		withdrawDone <- e
	}()
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if e := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted)`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		close(release)
		t.Fatal("withdrawal did not wait for in-flight matching")
	}
	close(release)
	if e := <-readDone; e != nil {
		t.Fatal(e)
	}
	if e := <-withdrawDone; e != nil {
		t.Fatal(e)
	}
	directory.before = nil
	matches, err = restarted.ContactMatches(ctx, actors["alice"], settings, 1, "", 50, directory, now)
	if err != nil || len(matches.Items) != 1 || matches.Items[0].IdentityID != "charlie" {
		t.Fatalf("post-withdrawal disclosure: %+v %v", matches, err)
	}
	grant("bob", true)
	sync("bob", 2, phones["alice"])
	// A response-lost replay is safe; a distinct stale payload conflicts.
	replay, proof := snapshot("alice", 0, phones["bob"], phones["charlie"])
	if value, e := restarted.PutContactSnapshot(ctx, actors["alice"], proof, replay, settings, now); e != nil || value.Revision != 1 {
		t.Fatalf("replay %+v %v", value, e)
	}
	stale, proof := snapshot("alice", 0, phones["bob"])
	if _, err = restarted.PutContactSnapshot(ctx, actors["alice"], proof, stale, settings, now); !errors.Is(err, ErrContactConflict) {
		t.Fatal(err)
	}
	foreign := actors["alice"]
	foreign.Country = "GB"
	if _, err = restarted.PutContactSnapshot(ctx, foreign, proof, stale, settings, now); err == nil {
		t.Fatal("country spoof accepted")
	}
	grant("alice", false)
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.contact_hashes WHERE identity_id='alice'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("withdrawal left graph: %d %v", count, err)
	}
	grant("alice", true)
	if _, err = restarted.PutContactSnapshot(ctx, actors["alice"], proof, replay, settings, now); !errors.Is(err, ErrContactConflict) {
		t.Fatalf("delayed pre-withdrawal upload: %v", err)
	}
	state, err = restarted.ContactState(ctx, actors["alice"], settings, now)
	if err != nil || state.Revision != 2 || state.ContactCount != 0 || state.Status != "EMPTY" {
		t.Fatalf("tombstone %+v %v", state, err)
	}
	sync("alice", 2, phones["bob"])
	// Audit failure rolls the entire refresh back.
	if _, err = pool.Exec(ctx, `REVOKE INSERT ON identity.security_events FROM planext4u_identity_runtime`); err != nil {
		t.Fatal(err)
	}
	replacement, proof := snapshot("alice", 3, phones["charlie"])
	if _, err = restarted.PutContactSnapshot(ctx, actors["alice"], proof, replacement, settings, now); err == nil {
		t.Fatal("unaudited mutation committed")
	}
	if _, err = pool.Exec(ctx, `GRANT INSERT ON identity.security_events TO planext4u_identity_runtime`); err != nil {
		t.Fatal(err)
	}
	state, err = restarted.ContactState(ctx, actors["alice"], settings, now)
	if err != nil || state.Revision != 3 {
		t.Fatalf("rollback %+v %v", state, err)
	}
	// Current session authority is checked again after external proof acquisition.
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1 WHERE identity_id='alice'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.PutContactSnapshot(ctx, actors["alice"], proof, replacement, settings, now); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked owner: %v", err)
	}
	// Bounded retention erases actual stored HMACs with durable revision/audit.
	if n, e := restarted.ExpireContacts(ctx, now.Add(2*time.Hour), 2); e != nil || n != 2 {
		t.Fatalf("expiry batch %d %v", n, e)
	}
	if n, e := restarted.ExpireContacts(ctx, now.Add(2*time.Hour), 2); e != nil || n != 1 {
		t.Fatalf("expiry remainder %d %v", n, e)
	}
	if n, e := restarted.ExpireContacts(ctx, now.Add(2*time.Hour), 2); e != nil || n != 0 {
		t.Fatalf("expiry replay %d %v", n, e)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.contact_hashes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expiry left graph: %d %v", count, err)
	}
	if _, err = runtime.Exec(ctx, `SELECT document FROM configuration.snapshots`); err == nil {
		t.Fatal("identity runtime read configuration owner table")
	}
}
