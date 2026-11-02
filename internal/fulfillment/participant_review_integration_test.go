//go:build integration

package fulfillment

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

func verifyRiderParticipantReviewAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool, databaseURL string, actor Actor) Actor {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(c context.Context, connection *pgx.Conn) error {
		_, e := connection.Exec(c, "SET ROLE planext4u_fulfillment_runtime")
		return e
	}
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	verifyRiderRework(t, ctx, pool, owner, actor)
	verifyRiderRegionalScope(t, ctx, pool, owner, databaseURL, actor)
	service, err := NewPostgresService(owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.RiderReview(ctx, actor, fulfillmentRiderOne)
	if err != nil || review.RiderID != fulfillmentRiderOne || review.Revision < 1 || len(review.Documents) == 0 {
		t.Fatalf("country rider review detail: %+v %v", review, err)
	}
	reviewJSON, _ := json.Marshal(review)
	for _, sensitive := range []string{"asset_id", "bank_reference", fulfillmentDocumentOne, fulfillmentDocumentTwo} {
		if strings.Contains(string(reviewJSON), sensitive) {
			t.Fatalf("private rider review field %q", sensitive)
		}
	}
	noReviewSource := actor
	noReviewSource.OperatorSessionID = ""
	if _, err = service.RiderReview(ctx, noReviewSource, fulfillmentRiderOne); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rider detail without current source: %v", err)
	}
	selfReview := actor
	selfReview.Subject = fulfillmentRiderOne
	if _, err = service.RiderReview(ctx, selfReview, fulfillmentRiderOne); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rider self review: %v", err)
	}
	page, err := service.RiderApplications(ctx, actor, participantreview.Filter{})
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
	if p, e := service.RiderApplications(ctx, actor, participantreview.Filter{Search: "%_"}); e != nil || len(p.Items) != 0 {
		t.Fatalf("literal search: %+v %v", p, e)
	}
	for name, change := range map[string]func(*Actor){"missing-source": func(a *Actor) { a.OperatorSessionID = "" }, "revision": func(a *Actor) { a.OperatorRevision++ }, "country": func(a *Actor) { a.Country = "NG" }, "tenant": func(a *Actor) { a.TenantID = "10000000-0000-4000-8000-000000009999" }, "mfa": func(a *Actor) { a.MFAVerified = false }} {
		a := actor
		change(&a)
		if _, e := service.RiderApplications(ctx, a, participantreview.Filter{}); !errors.Is(e, ErrForbidden) {
			t.Fatalf("%s: %v", name, e)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO fulfillment.rider_profiles(id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at) VALUES('70000000-0000-4000-8000-000000009999',$1,'IN','60000000-0000-4000-8000-000000009999',1,'KYC_REVIEW','Isolated rejection fixture','******1234','BICYCLE','','bankref_syntheticfixture','PENDING_VERIFICATION',ARRAY['600001'],1,'[]',clock_timestamp(),clock_timestamp())`, fulfillmentTenantOne); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fulfillment.reject_review_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected review evidence failure'; END $$; CREATE TRIGGER review_audit_fixture BEFORE INSERT ON fulfillment.audit_events FOR EACH ROW EXECUTE FUNCTION fulfillment.reject_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	key := "participant-rejection-atomic-fixture"
	if _, _, err = service.ReviewRider(actor, key, "60000000-0000-4000-8000-000000009999", 1, false, "Documented application review rejection"); err == nil {
		t.Fatal("acknowledged rejection without audit")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER review_audit_fixture ON fulfillment.audit_events; DROP FUNCTION fulfillment.reject_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err = pool.QueryRow(ctx, `SELECT revision FROM fulfillment.rider_profiles WHERE tenant_id=$1 AND rider_identity_id=$2`, fulfillmentTenantOne, "60000000-0000-4000-8000-000000009999").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("audit failure mutated: %d %v", revision, err)
	}
	value, replay, err := service.ReviewRider(actor, key, "60000000-0000-4000-8000-000000009999", 1, false, "Documented application review rejection")
	if err != nil || replay || string(value.Status) != "SUSPENDED" || value.Revision != 2 || value.AllowedActions == nil {
		t.Fatalf("rejection: %+v %t %v", value, replay, err)
	}
	readback, readErr := service.Rider(Actor{TenantID: actor.TenantID, Country: actor.Country, Subject: "60000000-0000-4000-8000-000000009999", Roles: []string{"RIDER"}})
	if readErr != nil || readback.Status != RiderSuspended || readback.AllowedActions == nil {
		t.Fatalf("normal suspended rider contract: %+v %v", readback, readErr)
	}
	restarted, _ := NewPostgresService(owner, time.Now)
	service = restarted
	if _, replay, err = service.ReviewRider(actor, key, "60000000-0000-4000-8000-000000009999", 1, false, "Documented application review rejection"); err != nil || !replay {
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
	go func() { _, e := service.RiderApplications(ctx, actor, participantreview.Filter{}); result <- e }()
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
	if _, _, err = service.ReviewRider(actor, key, "60000000-0000-4000-8000-000000009999", 1, false, "Documented application review rejection"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended replay: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.operator_accounts SET active=true,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2`, actor.TenantID, actor.Subject); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ReviewRider(actor, key, "60000000-0000-4000-8000-000000009999", 1, false, "Documented application review rejection"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reactivation revived old source: %v", err)
	}
	actor.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, actor.TenantID, actor.Subject)
	actor.OperatorRevision = 3
	if _, _, err = service.ReviewRider(actor, key, "60000000-0000-4000-8000-000000009999", 1, false, "Documented application review rejection"); err != nil {
		t.Fatalf("new source cannot retrieve acknowledged result: %v", err)
	}
	verifyRiderReviewAccess(t, ctx, pool, owner, actor)
	return actor
}

func verifyRiderRegionalScope(t *testing.T, ctx context.Context, pool, owner *pgxpool.Pool, url string, country Actor) {
	t.Helper()
	const subject = "60000000-0000-4000-8000-000000008904"
	if _, err := pool.Exec(ctx, `INSERT INTO fulfillment.rider_profiles(id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at) VALUES('70000000-0000-4000-8000-000000008904',$1,'IN',$2,1,'KYC_REVIEW','Regional rider fixture','******1234','BICYCLE','CYC123','bankref_syntheticfixture','PENDING_VERIFICATION',ARRAY['600001'],1,'[]',clock_timestamp(),clock_timestamp())`, country.TenantID, subject); err != nil {
		t.Fatal(err)
	}
	service, err := NewPostgresService(owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	actor := func(s participantreview.Scope) Actor {
		return Actor{TenantID: s.TenantID, Country: s.Country, Subject: s.Subject, OperatorSessionID: s.SourceSession, OperatorRevision: s.Revision, Roles: []string{"REGIONAL_ADMIN"}, MFAVerified: s.MFA}
	}
	testsupport.RegionalParticipantScope(t, ctx, pool, url, "RIDER", subject, reviewerScope(country), testsupport.ParticipantScopeCommands{
		List: func(s participantreview.Scope) (participantreview.Page, error) {
			page, err := service.RiderApplications(ctx, actor(s), participantreview.Filter{})
			if err == nil {
				for _, item := range page.Items {
					if item.ParticipantID == subject && (item.DocumentCount != 0 || item.BankStatus != "") {
						t.Fatalf("regional rider queue read private document or bank metadata: %+v", item)
					}
				}
			}
			return page, err
		},
		Reject: func(s participantreview.Scope, key string) (bool, error) {
			_, replay, e := service.ReviewRider(actor(s), key, subject, 1, false, "Regional documented evidence rejection")
			return replay, e
		},
		CountryOnly: func(s participantreview.Scope) error {
			_, _, e := service.ReviewRider(actor(s), "regional-final-review-denial", subject, 1, true, "Regional must not acquire approval authority")
			if errors.Is(e, ErrForbidden) {
				return e
			}
			return nil
		},
	})
}
