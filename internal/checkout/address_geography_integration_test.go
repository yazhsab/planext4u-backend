//go:build integration

package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/organization"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentAddressGeographyAndCurrentSessionAuthority(t *testing.T) {
	url := os.Getenv("CHECKOUT_DATABASE_TEST_URL")
	if url == "" {
		t.Skip("CHECKOUT_DATABASE_TEST_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, schema := range []string{"commerce", "admin", "identity"} {
		if _, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, schema := range []string{"commerce", "admin", "identity"} {
			_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		}
	})
	apply := func(path string) {
		t.Helper()
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, e := pool.Exec(ctx, string(data)); e != nil {
			t.Fatalf("%s: %v", path, e)
		}
	}
	for _, schema := range []string{"platform", "identity", "commerce"} {
		paths, e := filepath.Glob("../../migrations/" + schema + "/*.up.sql")
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range paths {
			apply(p)
		}
	}
	for _, file := range []string{"000001_admin_control_plane", "000003_organization", "000004_operator_accounts", "000005_operator_administration", "000011_master_data", "000012_occupation_reference_access", "000013_address_geography"} {
		apply("../../migrations/admin/" + file + ".up.sql")
	}
	rolePool := func(role string) *pgxpool.Pool {
		c, e := pgxpool.ParseConfig(url)
		if e != nil {
			t.Fatal(e)
		}
		c.ConnConfig.RuntimeParams["role"] = role
		p, e := pgxpool.NewWithConfig(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(p.Close)
		return p
	}
	commercePool, configurationPool, adminPool := rolePool("planext4u_commerce_runtime"), rolePool("planext4u_configuration_runtime"), rolePool("planext4u_admin_runtime")
	tenant, subject, session := uuid.NewString(), uuid.NewString(), uuid.NewString()
	scope := Scope{TenantID: tenant, Country: "IN", CustomerID: subject, SourceSessionID: session}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.identities(id,tenant_id,provider,provider_subject,created_at) VALUES($1,$2,'local',$1,clock_timestamp());`, subject, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.identity_roles(identity_id,role,granted_at) VALUES($1,'CUSTOMER',clock_timestamp())`, subject); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO identity.sessions(id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at) VALUES($1,$2,$3,'IN','synthetic-device','safe-device',clock_timestamp()+interval '1 hour',clock_timestamp(),clock_timestamp())`, session, subject, tenant); err != nil {
		t.Fatal(err)
	}
	actor := organization.Actor{TenantID: tenant, Country: "IN", Subject: uuid.NewString(), OperatorSessionID: uuid.NewString(), OperatorRevision: 1, MFA: true, RequireLiveOperator: true}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES($1,$2,$3,'Synthetic geography administrator',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'])`, tenant, actor.Subject, "synthetic-"+actor.Subject); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 2)
	if _, err = pool.Exec(ctx, `INSERT INTO admin.sessions(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at,operator_revision) VALUES($1::text,$2,$3,$4,'Synthetic geography administrator',ARRAY['COUNTRY_ADMIN'],ARRAY['IN'],'IN',clock_timestamp(),ARRAY['totp'],$1::text,clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, digest, actor.OperatorSessionID, actor.Subject, tenant); err != nil {
		t.Fatal(err)
	}
	masters, _ := organization.NewService(adminPool, time.Now)
	path := []organization.Master{}
	parent := ""
	for _, kind := range []string{"COUNTRY", "STATE", "DISTRICT", "CITY", "AREA"} {
		code := kind
		if kind == "COUNTRY" {
			code = "IN"
		}
		m, e := masters.SaveMaster(ctx, actor, organization.MasterChange{Master: organization.Master{ID: uuid.NewString(), Kind: kind, Code: code, ParentID: parent, NameEN: kind, NameTA: "பகுதி", Active: true}, Reason: "Synthetic governed address path"})
		if e != nil {
			t.Fatal(e)
		}
		path = append(path, m)
		parent = m.ID
	}
	directory, _ := configcms.NewPostgresGeographyDirectory(configurationPool)
	parent = ""
	for _, m := range path {
		page, e := directory.Geography(ctx, tenant, "IN", m.Kind, parent, "", "")
		if e != nil || len(page.Items) != 1 || page.Items[0].ID != m.ID {
			t.Fatalf("directory %s: %+v %v", m.Kind, page, e)
		}
		parent = m.ID
	}
	for _, s := range [][2]string{{uuid.NewString(), "IN"}, {tenant, "US"}} {
		page, e := directory.Geography(ctx, s[0], s[1], "COUNTRY", "", "", "")
		if e != nil || len(page.Items) != 0 {
			t.Fatalf("foreign dictionary %+v %v", page, e)
		}
	}
	for _, p := range []*pgxpool.Pool{commercePool, configurationPool} {
		if _, e := p.Exec(ctx, `SELECT * FROM admin.master_data`); e == nil {
			t.Fatal("private admin table exposed")
		}
		if _, e := p.Exec(ctx, `SELECT * FROM identity.sessions`); e == nil {
			t.Fatal("private source sessions exposed")
		}
	}
	store, e := NewAuthenticatedPostgresStore(commercePool, time.Now)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	f := newCheckoutFixture(t, WalletHybrid)
	f.service.store = store
	handler, _ := NewHandler(f.service)
	call := func(method, target, body, key, revision string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Planext4u-Tenant", tenant)
		r.Header.Set("X-Planext4u-Country", "IN")
		r.Header.Set("X-Planext4u-Subject", subject)
		r.Header.Set("X-Planext4u-Session", session)
		r.Header.Set("X-Planext4u-Roles", "CUSTOMER")
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("If-Match", revision)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	input := AddressInput{Label: "Home", Line1: "123 Local Road", PostalCode: "600001", Locality: "Chennai", Default: true, GeographyAreaID: path[4].ID}
	raw, _ := json.Marshal(input)
	created := call(http.MethodPost, "/v1/addresses", string(raw), "synthetic-address-create-001", "")
	var address Address
	if e = json.Unmarshal(created.Body.Bytes(), &address); e != nil || created.Code != 201 || address.Geography == nil || len(address.Geography.Path) != 5 || address.Serviceable {
		t.Fatalf("create without finance publication: %d %s %v", created.Code, created.Body.String(), e)
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("address response cacheable")
	}
	replay := call(http.MethodPost, "/v1/addresses", string(raw), "synthetic-address-create-001", "")
	if replay.Code != 201 || replay.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	restarted, _ := NewAuthenticatedPostgresStore(commercePool, time.Now)
	readback, e := restarted.Addresses(scope)
	if e != nil || len(readback) != 1 || readback[0].Geography.AreaID != path[4].ID {
		t.Fatalf("restart %+v %v", readback, e)
	}
	changed := input
	changed.GeographyAreaID = path[3].ID
	if _, _, e = f.service.CreateAddress(scope, "synthetic-address-create-001", changed); !errors.Is(e, ErrIdempotencyConflict) {
		t.Fatalf("changed reference replay %v", e)
	}
	if _, _, e = f.service.CreateAddress(scope, "synthetic-address-wrong-kind", changed); !errors.Is(e, ErrInvalidRequest) {
		t.Fatalf("wrong kind %v", e)
	}
	changed.GeographyAreaID = uuid.NewString()
	if _, _, e = f.service.CreateAddress(scope, "synthetic-address-unknown", changed); !errors.Is(e, ErrInvalidRequest) {
		t.Fatalf("unknown reference %v", e)
	}
	area := path[4]
	area.Active = false
	if _, e = masters.SaveMaster(ctx, actor, organization.MasterChange{Master: area, Reason: "Retire synthetic area safely"}); e != nil {
		t.Fatal(e)
	}
	page, e := directory.Geography(ctx, tenant, "IN", "AREA", path[3].ID, "", "")
	if e != nil || len(page.Items) != 0 {
		t.Fatalf("retired option %+v %v", page, e)
	}
	if _, _, e = f.service.CreateAddress(scope, "synthetic-address-retired", input); !errors.Is(e, ErrInvalidRequest) {
		t.Fatalf("retired create %v", e)
	}
	input.Line1 = "456 Updated Road"
	updated, _, e := f.service.UpdateAddress(scope, "synthetic-address-retain", address.ID, 1, input)
	if e != nil || updated.Geography.Path[4].MasterRevision != 1 {
		t.Fatalf("retained retired snapshot %+v %v", updated, e)
	}
	if _, _, e = f.service.UpdateAddress(scope, "synthetic-address-stale", address.ID, 1, input); !errors.Is(e, ErrAddressConflict) {
		t.Fatalf("stale update %v", e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := f.service.UpdateAddress(scope, "synthetic-address-race-"+uuid.NewString(), address.ID, 2, input)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrAddressConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS race %d/%d", success, conflict)
	}
	if _, e = pool.Exec(ctx, `ALTER TABLE commerce.checkout_commands ADD CONSTRAINT synthetic_address_rollback CHECK(command_type<>'ADDRESS_UPDATE') NOT VALID`); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.service.UpdateAddress(scope, "synthetic-address-audit-fail", address.ID, 3, input); e == nil {
		t.Fatal("missing command record acknowledged")
	}
	readback, e = store.Addresses(scope)
	if e != nil || readback[0].Revision != 3 {
		t.Fatalf("failed write changed address %+v %v", readback, e)
	}
	if _, e = pool.Exec(ctx, `ALTER TABLE commerce.checkout_commands DROP CONSTRAINT synthetic_address_rollback`); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Scope{{TenantID: uuid.NewString(), Country: "IN", CustomerID: subject, SourceSessionID: session}, {TenantID: tenant, Country: "US", CustomerID: subject, SourceSessionID: session}, {TenantID: tenant, Country: "IN", CustomerID: uuid.NewString(), SourceSessionID: session}, {TenantID: tenant, Country: "IN", CustomerID: subject}} {
		if _, e = store.Addresses(bad); !errors.Is(e, ErrAddressSession) {
			t.Fatalf("foreign/missing source %+v: %v", bad, e)
		}
	}
	blocker, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, e = blocker.Exec(ctx, `SELECT id FROM identity.sessions WHERE id=$1 FOR UPDATE`, session); e != nil {
		t.Fatal(e)
	}
	queued := make(chan error, 1)
	go func() {
		_, _, e := f.service.UpdateAddress(scope, "synthetic-address-revocation", address.ID, 3, input)
		queued <- e
	}()
	select {
	case e := <-queued:
		t.Fatalf("source lock bypassed %v", e)
	case <-time.After(80 * time.Millisecond):
	}
	if _, e = blocker.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, session); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-queued; !errors.Is(e, ErrAddressSession) {
		t.Fatalf("queued revoked write %v", e)
	}
	if got := call(http.MethodGet, "/v1/addresses", "", "", ""); got.Code != 401 {
		t.Fatalf("revoked direct read %d %s", got.Code, got.Body.String())
	}
	if _, _, e = f.service.CreateAddress(scope, "synthetic-address-create-001", input); !errors.Is(e, ErrAddressSession) {
		t.Fatalf("revoked replay %v", e)
	}
	// A new normal session sees implicit default changes at new revisions. An
	// acknowledged deletion may leave an empty book; it needs no wallet policy.
	fresh := uuid.NewString()
	if _, e = pool.Exec(ctx, `INSERT INTO identity.sessions(id,identity_id,tenant_id,country,device_id,device_reference,refresh_expires_at,authenticated_at,last_seen_at) VALUES($1,$2,$3,'IN','synthetic-device-two','safe-device-two',clock_timestamp()+interval '1 hour',clock_timestamp(),clock_timestamp())`, fresh, subject, tenant); e != nil {
		t.Fatal(e)
	}
	freshScope := scope
	freshScope.SourceSessionID = fresh
	secondArea, e := masters.SaveMaster(ctx, actor, organization.MasterChange{Master: organization.Master{ID: uuid.NewString(), Kind: "AREA", Code: "OTHER_AREA", ParentID: path[3].ID, NameEN: "Other area", NameTA: "மற்ற பகுதி", Active: true}, Reason: "Synthetic second address area"})
	if e != nil {
		t.Fatal(e)
	}
	secondInput := input
	secondInput.GeographyAreaID = secondArea.ID
	secondInput.Default = true
	second, _, e := f.service.CreateAddress(freshScope, "synthetic-address-new-default", secondInput)
	if e != nil {
		t.Fatal(e)
	}
	all, e := store.Addresses(freshScope)
	if e != nil || len(all) != 2 {
		t.Fatalf("default readback %+v %v", all, e)
	}
	var previous Address
	for _, a := range all {
		if a.ID == address.ID {
			previous = a
		}
	}
	if previous.Default || previous.Revision != 4 {
		t.Fatalf("implicit default lost revision: %+v", previous)
	}
	if _, _, e = f.service.UpdateAddress(freshScope, "synthetic-stale-default", address.ID, 3, input); !errors.Is(e, ErrAddressConflict) {
		t.Fatalf("stale implicit default command: %v", e)
	}
	if _, e = f.service.DeleteAddress(freshScope, "synthetic-remove-default", second.ID, second.Revision); e != nil {
		t.Fatal(e)
	}
	all, e = store.Addresses(freshScope)
	if e != nil || len(all) != 1 || !all[0].Default || all[0].Revision != 5 || all[0].Geography.AreaID != path[4].ID {
		t.Fatalf("promoted immutable reference %+v %v", all, e)
	}
	if _, e = f.service.DeleteAddress(freshScope, "synthetic-remove-final", address.ID, 5); e != nil {
		t.Fatal(e)
	}
	all, e = store.Addresses(freshScope)
	if e != nil || len(all) != 0 {
		t.Fatalf("last address removal %+v %v", all, e)
	}
}
