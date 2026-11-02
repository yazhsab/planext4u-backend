//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
)

type riderEvidenceAccessFixture struct {
	calls  int
	reject bool
	asset  string
	owner  string
}

func (*riderEvidenceAccessFixture) VerifyIdentityDocuments(context.Context, privateevidence.Scope, []string) error {
	return nil
}
func (fixture *riderEvidenceAccessFixture) AccessIdentityDocument(_ context.Context, scope privateevidence.Scope, id string) (privateevidence.AccessGrant, error) {
	fixture.calls++
	if scope.OwnerID != fixture.owner || id != fixture.asset || scope.Country != "IN" || scope.TenantID != fulfillmentTenantOne {
		return privateevidence.AccessGrant{}, privateevidence.ErrRejected
	}
	if fixture.reject {
		return privateevidence.AccessGrant{}, privateevidence.ErrRejected
	}
	return privateevidence.AccessGrant{URL: "https://private.example/rider?signature=synthetic-secret", ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}

func verifyRiderReviewAccess(t *testing.T, ctx context.Context, pool, owner *pgxpool.Pool, actor Actor) {
	t.Helper()
	const riderID = "60000000-0000-4000-8000-000000008921"
	const assetID = "90000000-0000-4000-8000-000000008921"
	if _, err := pool.Exec(ctx, `INSERT INTO fulfillment.rider_profiles(id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at)
	 VALUES('70000000-0000-4000-8000-000000008921',$1,'IN',$2,1,'KYC_REVIEW','Private rider review','******4321','BICYCLE','CYC1','bankref_reviewfixture','PENDING_VERIFICATION',ARRAY['600001'],1,
	 '[{"kind":"IDENTITY","asset_id":"90000000-0000-4000-8000-000000008921","ocr_status":"REVIEW_REQUIRED"}]'::jsonb,clock_timestamp(),clock_timestamp())`, actor.TenantID, riderID); err != nil {
		t.Fatal(err)
	}
	fixture := &riderEvidenceAccessFixture{asset: assetID, owner: riderID}
	service, err := NewPostgresService(owner, time.Now, fixture)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.RiderReviewDocumentAccess(ctx, actor, riderID, "IDENTITY")
	if err != nil || grant.Kind != "IDENTITY" || !strings.Contains(grant.URL, "signature=") || fixture.calls != 1 {
		t.Fatalf("rider review grant=%+v error=%v calls=%d", grant, err, fixture.calls)
	}
	if _, err = service.RiderReviewDocumentAccess(ctx, actor, riderID, "DRIVER_LICENSE"); !errors.Is(err, ErrNotFound) || fixture.calls != 1 {
		t.Fatalf("unstored rider document access=%v calls=%d", err, fixture.calls)
	}
	fixture.reject = true
	if _, err = service.RiderReviewDocumentAccess(ctx, actor, riderID, "IDENTITY"); !errors.Is(err, privateevidence.ErrRejected) {
		t.Fatalf("denied rider media=%v", err)
	}
	fixture.reject = false
	var granted, denied int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='RIDER_EVIDENCE_ACCESS_GRANTED'),count(*) FILTER(WHERE action='RIDER_EVIDENCE_ACCESS_DENIED') FROM fulfillment.audit_events WHERE resource_id=$1`, riderID).Scan(&granted, &denied); err != nil || granted != 1 || denied != 2 {
		t.Fatalf("rider audit granted=%d denied=%d error=%v", granted, denied, err)
	}
	if _, err = service.RiderReviewDocumentAccess(ctx, actor, "60000000-0000-4000-8000-000000008999", "IDENTITY"); !errors.Is(err, ErrNotFound) || fixture.calls != 2 {
		t.Fatalf("other rider leaked grant: %v calls=%d", err, fixture.calls)
	}
	stale := actor
	stale.OperatorRevision++
	if _, err = service.RiderReviewDocumentAccess(ctx, stale, riderID, "IDENTITY"); !errors.Is(err, ErrForbidden) || fixture.calls != 2 {
		t.Fatalf("stale operator leaked rider grant: %v calls=%d", err, fixture.calls)
	}
	revoked := actor
	revoked.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, actor.TenantID, actor.Subject)
	if _, err = pool.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, revoked.OperatorSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RiderReviewDocumentAccess(ctx, revoked, riderID, "IDENTITY"); !errors.Is(err, ErrForbidden) || fixture.calls != 2 {
		t.Fatalf("revoked operator leaked rider grant: %v calls=%d", err, fixture.calls)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fulfillment.fail_private_review_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$;
	 CREATE TRIGGER private_review_audit_fixture BEFORE INSERT ON fulfillment.audit_events FOR EACH ROW EXECUTE FUNCTION fulfillment.fail_private_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	if grant, err = service.RiderReviewDocumentAccess(ctx, actor, riderID, "IDENTITY"); err == nil || grant.URL != "" {
		t.Fatalf("rider audit failure returned signed grant: %+v %v", grant, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER private_review_audit_fixture ON fulfillment.audit_events; DROP FUNCTION fulfillment.fail_private_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.rider_profiles SET documents=documents||'[{"kind":"IDENTITY","asset_id":"90000000-0000-4000-8000-000000008922","ocr_status":"REVIEW_REQUIRED"}]'::jsonb WHERE rider_identity_id=$1`, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RiderReviewDocumentAccess(ctx, actor, riderID, "IDENTITY"); !errors.Is(err, ErrConflict) || fixture.calls != 3 {
		t.Fatalf("ambiguous rider document leaked grant: %v calls=%d", err, fixture.calls)
	}
}
