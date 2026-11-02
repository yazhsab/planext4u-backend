//go:build integration

package supply

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
	"strings"
	"testing"
	"time"
)

func verifyVendorParticipantReviewAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool, databaseURL string, actor Actor) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(c context.Context, connection *pgx.Conn) error {
		_, e := connection.Exec(c, "SET ROLE planext4u_supply_runtime")
		return e
	}
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	verifyVendorRework(t, ctx, pool, owner, actor)
	verifyVendorRegionalScope(t, ctx, pool, owner, databaseURL, actor)
	service, err := NewPostgresService(owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.VendorReview(ctx, actor, supplyVendorOne)
	if err != nil || review.VendorID != supplyVendorOne || review.Revision < 1 || len(review.Documents) == 0 {
		t.Fatalf("country vendor review detail: %+v %v", review, err)
	}
	reviewJSON, _ := json.Marshal(review)
	for _, sensitive := range []string{"asset_id", "bank_reference", "extracted_fields", supplyAssetOne, supplyAssetTwo} {
		if strings.Contains(string(reviewJSON), sensitive) {
			t.Fatalf("private vendor review field %q", sensitive)
		}
	}
	noReviewSource := actor
	noReviewSource.OperatorSessionID = ""
	if _, err = service.VendorReview(ctx, noReviewSource, supplyVendorOne); !errors.Is(err, ErrForbidden) {
		t.Fatalf("vendor detail without current source: %v", err)
	}
	selfReview := actor
	selfReview.Subject = supplyVendorOne
	if _, err = service.VendorReview(ctx, selfReview, supplyVendorOne); !errors.Is(err, ErrForbidden) {
		t.Fatalf("vendor self review: %v", err)
	}
	page, err := service.Applications(ctx, actor, participantreview.Filter{})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("current review queue: %+v %v", page, err)
	}
	data, _ := json.Marshal(page)
	if strings.Contains(string(data), "bank_reference") || strings.Contains(string(data), "asset_id") || strings.Contains(string(data), "extracted_fields") {
		t.Fatal("private fields in queue")
	}
	var direct, execute bool
	if err = owner.QueryRow(ctx, `SELECT has_table_privilege(current_user,'admin.operator_accounts','SELECT'),has_function_privilege(current_user,'admin.lock_participant_reviewer(uuid,character,uuid,uuid,bigint)','EXECUTE')`).Scan(&direct, &execute); err != nil || direct || !execute {
		t.Fatalf("private authority grants %t %t %v", direct, execute, err)
	}
	if p, e := service.Applications(ctx, actor, participantreview.Filter{Search: "%_"}); e != nil || len(p.Items) != 0 {
		t.Fatalf("literal search: %+v %v", p, e)
	}
	for name, change := range map[string]func(*Actor){"missing-source": func(a *Actor) { a.OperatorSessionID = "" }, "revision": func(a *Actor) { a.OperatorRevision++ }, "country": func(a *Actor) { a.Country = "NG" }, "tenant": func(a *Actor) { a.TenantID = "10000000-0000-4000-8000-000000009999" }, "mfa": func(a *Actor) { a.MFAVerified = false }} {
		a := actor
		change(&a)
		if _, e := service.Applications(ctx, a, participantreview.Filter{}); !errors.Is(e, ErrForbidden) {
			t.Fatalf("%s: %v", name, e)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO supply.vendor_applications(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at) VALUES('70000000-0000-4000-8000-000000009999',$1,'IN','60000000-0000-4000-8000-000000009999',1,'OCR_REVIEW','Isolated rejection fixture','SOLE_PROPRIETOR','Synthetic participant',clock_timestamp(),clock_timestamp())`, supplyTenantOne); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION supply.reject_review_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected review evidence failure'; END $$; CREATE TRIGGER review_audit_fixture BEFORE INSERT ON supply.application_timeline FOR EACH ROW EXECUTE FUNCTION supply.reject_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	key := "participant-rejection-atomic-fixture"
	if _, _, err = service.TransitionForVendor(actor, key, "60000000-0000-4000-8000-000000009999", 1, TransitionRequest{Status: StatusRejected, Reason: "Documented application review rejection"}); err == nil {
		t.Fatal("acknowledged rejection without audit")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER review_audit_fixture ON supply.application_timeline; DROP FUNCTION supply.reject_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err = pool.QueryRow(ctx, `SELECT revision FROM supply.vendor_applications WHERE tenant_id=$1 AND vendor_identity_id=$2`, supplyTenantOne, "60000000-0000-4000-8000-000000009999").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("audit failure mutated: %d %v", revision, err)
	}
	value, replay, err := service.TransitionForVendor(actor, key, "60000000-0000-4000-8000-000000009999", 1, TransitionRequest{Status: StatusRejected, Reason: "Documented application review rejection"})
	if err != nil || replay || string(value.Status) != "REJECTED" || value.Revision != 2 {
		t.Fatalf("rejection: %+v %t %v", value, replay, err)
	}
	restarted, _ := NewPostgresService(owner, time.Now)
	service = restarted
	if _, replay, err = service.TransitionForVendor(actor, key, "60000000-0000-4000-8000-000000009999", 1, TransitionRequest{Status: StatusRejected, Reason: "Documented application review rejection"}); err != nil || !replay {
		t.Fatalf("restart replay %t %v", replay, err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT revision FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 FOR UPDATE`, actor.TenantID, actor.Subject); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, e := service.Applications(ctx, actor, participantreview.Filter{}); result <- e }()
	waiting := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND position('lock_participant_access' in query)>0)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("did not observe queued source validation")
	}
	if _, err = blocker.Exec(ctx, `UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, actor.TenantID, actor.Subject); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("queued suspension allowed read: %v", err)
	}
	if _, _, err = service.TransitionForVendor(actor, key, "60000000-0000-4000-8000-000000009999", 1, TransitionRequest{Status: StatusRejected, Reason: "Documented application review rejection"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended replay: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET active=true,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, actor.TenantID, actor.Subject); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.TransitionForVendor(actor, key, "60000000-0000-4000-8000-000000009999", 1, TransitionRequest{Status: StatusRejected, Reason: "Documented application review rejection"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reactivation revived old source: %v", err)
	}
	actor.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, actor.TenantID, actor.Subject)
	actor.OperatorRevision = 3
	if _, _, err = service.TransitionForVendor(actor, key, "60000000-0000-4000-8000-000000009999", 1, TransitionRequest{Status: StatusRejected, Reason: "Documented application review rejection"}); err != nil {
		t.Fatalf("new source cannot retrieve acknowledged result: %v", err)
	}
	verifyVendorReviewAccess(t, ctx, pool, owner, actor)
}

func verifyVendorRegionalScope(t *testing.T, ctx context.Context, pool, owner *pgxpool.Pool, url string, country Actor) {
	t.Helper()
	const subject = "60000000-0000-4000-8000-000000008903"
	const foreign = "60000000-0000-4000-8000-000000008905"
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_applications(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at) VALUES('70000000-0000-4000-8000-000000008903',$1,'IN',$2,1,'OCR_REVIEW','Regional review fixture','SOLE_PROPRIETOR','Synthetic participant',clock_timestamp(),clock_timestamp())`, country.TenantID, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE supply.vendor_applications SET bank_reference='private-regional-bank',bank_last4='1234',bank_status='VERIFIED' WHERE tenant_id=$1 AND vendor_identity_id=$2`, country.TenantID, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_documents(id,application_id,kind,private_asset_id,ocr_status,extracted_fields,review_reason,created_at)
VALUES('80000000-0000-4000-8000-000000008903','70000000-0000-4000-8000-000000008903','OWNER_IDENTITY','90000000-0000-4000-8000-000000008903','VERIFIED','{"private":"regional-evidence"}'::jsonb,'private-regional-reason',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_applications(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at)
VALUES('70000000-0000-4000-8000-000000008905',$1,'IN',$2,1,'OCR_REVIEW','Foreign regional fixture','SOLE_PROPRIETOR','Synthetic participant',clock_timestamp(),clock_timestamp())`, country.TenantID, foreign); err != nil {
		t.Fatal(err)
	}
	service, err := NewPostgresService(owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	actor := func(s participantreview.Scope) Actor {
		return Actor{TenantID: s.TenantID, Country: s.Country, Subject: s.Subject, OperatorSessionID: s.SourceSession, OperatorRevision: s.Revision, Roles: []string{"REGIONAL_ADMIN"}, MFAVerified: s.MFA}
	}
	testsupport.RegionalParticipantScope(t, ctx, pool, url, "VENDOR", subject, reviewerScope(country), testsupport.ParticipantScopeCommands{
		List: func(s participantreview.Scope) (participantreview.Page, error) {
			page, err := service.Applications(ctx, actor(s), participantreview.Filter{})
			if err == nil {
				for _, item := range page.Items {
					if item.ParticipantID == subject && (item.DocumentCount != 0 || item.BankStatus != "") {
						t.Fatalf("regional vendor queue read private document or bank metadata: %+v", item)
					}
				}
			}
			return page, err
		},
		Detail: func(s participantreview.Scope) error {
			detail, e := service.VendorReview(ctx, actor(s), subject)
			if e != nil {
				return e
			}
			if _, e = service.VendorReview(ctx, actor(s), foreign); !errors.Is(e, ErrForbidden) {
				t.Fatalf("regional reviewer read unassigned foreign vendor: %v", e)
			}
			if detail.VendorID != subject || detail.BusinessName != "Regional review fixture" || detail.ContactName != "" || len(detail.Documents) != 0 || detail.BankStatus != "" || detail.BankLast4 != "" || detail.LastReviewReason != "" || len(detail.ReviewActions) != 0 {
				t.Fatalf("regional detail leaked private or country data: %+v", detail)
			}
			encoded, _ := json.Marshal(detail)
			for _, private := range []string{"Synthetic participant", "private-regional", "OWNER_IDENTITY", "1234", "asset_id", "extracted_fields", "bank_reference"} {
				if strings.Contains(string(encoded), private) {
					t.Fatalf("regional detail leaked %s: %s", private, encoded)
				}
			}
			return nil
		},
		Reject: func(s participantreview.Scope, key string) (bool, error) {
			_, replay, e := service.TransitionForVendor(actor(s), key, subject, 1, TransitionRequest{Status: StatusRejected, Reason: "Regional documented evidence rejection"})
			return replay, e
		},
		CountryOnly: func(s participantreview.Scope) error {
			_, _, e := service.TransitionForVendor(actor(s), "regional-final-review-denial", subject, 1, TransitionRequest{Status: StatusKYCReview, Reason: "Regional must not acquire verification authority"})
			if errors.Is(e, ErrForbidden) {
				return e
			}
			return nil
		},
	})
	const scheduledVendor = "60000000-0000-4000-8000-000000008904"
	const scheduledApplication = "70000000-0000-4000-8000-000000008904"
	const scheduledVisit = "80000000-0000-4000-8000-000000008904"
	if _, err = pool.Exec(ctx, `INSERT INTO supply.vendor_applications(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at) VALUES($1,$2,'IN',$3,1,'FIELD_VISIT_SCHEDULED','Regional visit fixture','SOLE_PROPRIETOR','Synthetic participant',clock_timestamp(),clock_timestamp())`, scheduledApplication, country.TenantID, scheduledVendor); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO supply.field_visits(id,application_id,scheduled_at,latitude,longitude,allowed_radius_m) VALUES($1,$2,clock_timestamp(),13.0827,80.2707,200)`, scheduledVisit, scheduledApplication); err != nil {
		t.Fatal(err)
	}
	fieldActor := func(s participantreview.Scope) Actor {
		return Actor{TenantID: s.TenantID, Country: s.Country, Subject: s.Subject, OperatorSessionID: s.SourceSession, OperatorRevision: s.Revision, Roles: []string{"FIELD_OFFICER"}, MFAVerified: s.MFA}
	}
	testsupport.RegionalParticipantScope(t, ctx, pool, url, "VENDOR", scheduledVendor, reviewerScope(country), testsupport.ParticipantScopeCommands{
		List: func(s participantreview.Scope) (participantreview.Page, error) {
			return service.Applications(ctx, actor(s), participantreview.Filter{})
		},
		Reject: func(s participantreview.Scope, key string) (bool, error) {
			_, replay, e := service.FieldCheckIn(fieldActor(s), key, scheduledVendor, 1, CheckInRequest{Latitude: 13.0827, Longitude: 80.2707})
			return replay, e
		},
		CountryOnly: func(s participantreview.Scope) error {
			_, _, e := service.TransitionForVendor(fieldActor(s), "regional-field-final-review-denial", scheduledVendor, 1, TransitionRequest{Status: StatusApproved, Reason: "Field officer cannot make final approval"})
			return e
		},
	})
}
