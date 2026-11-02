//go:build integration

package supply

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

type vendorEvidenceAccessFixture struct {
	calls  int
	reject bool
	asset  string
	owner  string
}

func (*vendorEvidenceAccessFixture) VerifyIdentityDocuments(context.Context, privateevidence.Scope, []string) error {
	return nil
}
func (fixture *vendorEvidenceAccessFixture) AccessIdentityDocument(_ context.Context, scope privateevidence.Scope, id string) (privateevidence.AccessGrant, error) {
	fixture.calls++
	if scope.OwnerID != fixture.owner || id != fixture.asset || scope.Country != "IN" || scope.TenantID != supplyTenantOne {
		return privateevidence.AccessGrant{}, privateevidence.ErrRejected
	}
	if fixture.reject {
		return privateevidence.AccessGrant{}, privateevidence.ErrRejected
	}
	return privateevidence.AccessGrant{URL: "https://private.example/document?signature=synthetic-secret", ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}

func verifyVendorReviewAccess(t *testing.T, ctx context.Context, pool, owner *pgxpool.Pool, actor Actor) {
	t.Helper()
	const vendorID = "60000000-0000-4000-8000-000000008919"
	const applicationID = "70000000-0000-4000-8000-000000008919"
	const assetID = "90000000-0000-4000-8000-000000008919"
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_applications(id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,created_at,updated_at)
	 VALUES($1,$2,'IN',$3,1,'OCR_REVIEW','Private review fixture','SOLE_PROPRIETOR','Synthetic contact',clock_timestamp(),clock_timestamp())`, applicationID, actor.TenantID, vendorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO supply.vendor_documents(id,application_id,kind,private_asset_id,ocr_status,created_at)
	 VALUES('80000000-0000-4000-8000-000000008919',$1,'OWNER_IDENTITY',$2,'REVIEW_REQUIRED',clock_timestamp())`, applicationID, assetID); err != nil {
		t.Fatal(err)
	}
	fixture := &vendorEvidenceAccessFixture{asset: assetID, owner: vendorID}
	service, err := NewPostgresService(owner, time.Now, fixture)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.VendorReviewDocumentAccess(ctx, actor, vendorID, "OWNER_IDENTITY")
	if err != nil || grant.Kind != "OWNER_IDENTITY" || !strings.Contains(grant.URL, "signature=") || fixture.calls != 1 {
		t.Fatalf("scoped grant=%+v error=%v calls=%d", grant, err, fixture.calls)
	}
	if _, err = service.VendorReviewDocumentAccess(ctx, actor, vendorID, "BUSINESS_LICENSE"); !errors.Is(err, ErrNotFound) || fixture.calls != 1 {
		t.Fatalf("missing stored document access=%v calls=%d", err, fixture.calls)
	}
	fixture.reject = true
	if _, err = service.VendorReviewDocumentAccess(ctx, actor, vendorID, "OWNER_IDENTITY"); !errors.Is(err, privateevidence.ErrRejected) {
		t.Fatalf("denied private media=%v", err)
	}
	fixture.reject = false
	var granted, denied int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE outcome='GRANTED'),count(*) FILTER(WHERE outcome='DENIED') FROM supply.reviewer_evidence_access WHERE vendor_identity_id=$1`, vendorID).Scan(&granted, &denied); err != nil || granted != 1 || denied != 2 {
		t.Fatalf("audited outcomes granted=%d denied=%d error=%v", granted, denied, err)
	}
	if _, err = service.VendorReviewDocumentAccess(ctx, actor, "60000000-0000-4000-8000-000000008999", "OWNER_IDENTITY"); !errors.Is(err, ErrNotFound) || fixture.calls != 2 {
		t.Fatalf("other applicant leaked grant: %v calls=%d", err, fixture.calls)
	}
	stale := actor
	stale.OperatorRevision++
	if _, err = service.VendorReviewDocumentAccess(ctx, stale, vendorID, "OWNER_IDENTITY"); !errors.Is(err, ErrForbidden) || fixture.calls != 2 {
		t.Fatalf("stale operator source leaked grant: %v calls=%d", err, fixture.calls)
	}
	revoked := actor
	revoked.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, actor.TenantID, actor.Subject)
	if _, err = pool.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, revoked.OperatorSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.VendorReviewDocumentAccess(ctx, revoked, vendorID, "OWNER_IDENTITY"); !errors.Is(err, ErrForbidden) || fixture.calls != 2 {
		t.Fatalf("revoked source leaked grant: %v calls=%d", err, fixture.calls)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION supply.fail_private_review_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$;
	 CREATE TRIGGER private_review_audit_fixture BEFORE INSERT ON supply.reviewer_evidence_access FOR EACH ROW EXECUTE FUNCTION supply.fail_private_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	if grant, err = service.VendorReviewDocumentAccess(ctx, actor, vendorID, "OWNER_IDENTITY"); err == nil || grant.URL != "" {
		t.Fatalf("audit failure returned signed grant: %+v %v", grant, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER private_review_audit_fixture ON supply.reviewer_evidence_access; DROP FUNCTION supply.fail_private_review_audit_fixture()`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO supply.vendor_documents(id,application_id,kind,private_asset_id,ocr_status,created_at)
	 VALUES('80000000-0000-4000-8000-000000008920',$1,'OWNER_IDENTITY','90000000-0000-4000-8000-000000008920','REVIEW_REQUIRED',clock_timestamp())`, applicationID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.VendorReviewDocumentAccess(ctx, actor, vendorID, "OWNER_IDENTITY"); !errors.Is(err, ErrConflict) || fixture.calls != 3 {
		t.Fatalf("ambiguous stored kind leaked grant: %v calls=%d", err, fixture.calls)
	}
}
