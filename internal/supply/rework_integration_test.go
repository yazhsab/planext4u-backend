//go:build integration

package supply

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
	"testing"
	"time"
)

func verifyVendorRework(t *testing.T, ctx context.Context, pool, owner *pgxpool.Pool, reviewer Actor) {
	t.Helper()
	const subject = "60000000-0000-4000-8000-000000008901"
	ids := []string{"90000000-0000-4000-8000-000000008901", "90000000-0000-4000-8000-000000008902", "90000000-0000-4000-8000-000000008903", "90000000-0000-4000-8000-000000008904"}
	testsupport.AddPrivateIdentityMedia(t, ctx, pool, reviewer.TenantID, subject, ids)
	a := Actor{TenantID: reviewer.TenantID, Country: "IN", Subject: subject, Roles: []string{"VENDOR"}}
	a.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, a.TenantID, a.Subject, "VENDOR")
	verifier := privateevidence.VerifyFunc(func(_ context.Context, scope privateevidence.Scope, assets []string) error {
		if scope.OwnerID != subject || scope.TenantID != a.TenantID || scope.Country != "IN" {
			return privateevidence.ErrRejected
		}
		for _, asset := range assets {
			found := false
			for _, id := range ids {
				found = found || asset == id
			}
			if !found {
				return privateevidence.ErrRejected
			}
		}
		return nil
	})
	service, err := NewPostgresService(owner, time.Now, verifier)
	if err != nil {
		t.Fatal(err)
	}
	value, _, err := service.Register(a, "rework-registration-01", RegisterRequest{BusinessName: "Rework local fixture", BusinessType: "SOLE_PROPRIETOR", ContactName: "Synthetic vendor"})
	if err != nil {
		t.Fatal(err)
	}
	old := DocumentsRequest{Documents: []Document{{Kind: "OWNER_IDENTITY", AssetID: ids[0]}, {Kind: "BUSINESS_REGISTRATION", AssetID: ids[1]}}}
	value, _, err = service.SubmitDocuments(a, "rework-documents-old-01", value.Revision, old)
	if err != nil {
		t.Fatal(err)
	}
	value, _, err = service.TransitionForVendor(reviewer, "vendor-rework-ocr-transition-01", subject, value.Revision, TransitionRequest{Status: StatusOCRReview, Reason: "Begin independent review"})
	if err != nil {
		t.Fatal(err)
	}
	value, _, err = service.TransitionForVendor(reviewer, "rework-rejection-01", subject, value.Revision, TransitionRequest{Status: StatusRejected, Reason: "Replace unreadable registration document"})
	if err != nil {
		t.Fatal(err)
	}
	revision := value.Revision
	corrected := ResubmissionRequest{BusinessName: "Corrected local fixture", BusinessType: "PRIVATE_LIMITED", ContactName: "Corrected vendor", Documents: []Document{{Kind: "OWNER_IDENTITY", AssetID: ids[2]}, {Kind: "BUSINESS_REGISTRATION", AssetID: ids[3]}}}
	key := "rework-documents-corrected-01"
	incomplete := corrected
	incomplete.ContactName = " "
	if _, _, err = service.ResubmitDocuments(a, "rework-profile-incomplete-01", revision, incomplete); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("accepted incomplete rework profile: %v", err)
	}
	for name, change := range map[string]func(*Actor){"no-session": func(v *Actor) { v.SourceSessionID = "" }, "country": func(v *Actor) { v.Country = "NG" }, "role": func(v *Actor) { v.Roles = []string{"CUSTOMER"} }} {
		bad := a
		change(&bad)
		if _, _, err = service.ResubmitDocuments(bad, key, revision, corrected); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, _, err = service.ResubmitDocuments(a, key, revision-1, corrected); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION supply.rework_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER rework_failure BEFORE INSERT ON supply.application_timeline FOR EACH ROW EXECUTE FUNCTION supply.rework_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ResubmitDocuments(a, key, revision, corrected); err == nil {
		t.Fatal("rework acknowledged without audit")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER rework_failure ON supply.application_timeline;DROP FUNCTION supply.rework_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM media.identity_evidence_claims WHERE asset_id=ANY($1::uuid[])`, ids[2:]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback claimed replacement: %d %v", count, err)
	}
	value, replay, err := service.ResubmitDocuments(a, key, revision, corrected)
	if err != nil || replay || value.Status != StatusDocumentsSubmitted || value.Verified || value.Bank != nil || value.Visit != nil || len(value.Zones) != 0 || value.BusinessName != corrected.BusinessName || value.BusinessType != corrected.BusinessType || value.ContactName != corrected.ContactName {
		t.Fatalf("resubmit %+v %t %v", value, replay, err)
	}
	restarted, _ := NewPostgresService(owner, time.Now, verifier)
	if _, replay, err = restarted.ResubmitDocuments(a, key, revision, corrected); err != nil || !replay {
		t.Fatalf("restart replay %t %v", replay, err)
	}
	if _, _, err = restarted.ResubmitDocuments(a, key, revision+1, corrected); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed revision replay %v", err)
	}
	changedProfile := corrected
	changedProfile.BusinessName = "Different corrected business"
	if _, _, err = restarted.ResubmitDocuments(a, key, revision, changedProfile); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed profile replay %v", err)
	}
	if current, e := restarted.Application(a); e != nil || current.BusinessName != corrected.BusinessName || current.ContactName != corrected.ContactName {
		t.Fatalf("resubmission profile readback %+v %v", current, e)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM media.identity_evidence_claims WHERE asset_id=ANY($1::uuid[])`, ids).Scan(&count); err != nil || count != 4 {
		t.Fatalf("old/new claims %d %v", count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM supply.application_submissions WHERE application_id=$1`, value.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("submission history %d %v", count, err)
	}
	var historicalName, currentName string
	if err = pool.QueryRow(ctx, `SELECT business_name FROM supply.application_submissions WHERE application_id=$1 ORDER BY revision LIMIT 1`, value.ID).Scan(&historicalName); err != nil || historicalName != "Rework local fixture" {
		t.Fatalf("old profile history %q %v", historicalName, err)
	}
	if err = pool.QueryRow(ctx, `SELECT business_name FROM supply.application_submissions WHERE application_id=$1 ORDER BY revision DESC LIMIT 1`, value.ID).Scan(&currentName); err != nil || currentName != corrected.BusinessName {
		t.Fatalf("current profile history %q %v", currentName, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, a.SourceSessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restarted.ResubmitDocuments(a, key, revision, corrected); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked session replay %v", err)
	}
	a.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, a.TenantID, a.Subject, "VENDOR")
	if _, err = pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests(id,identity_id,status,reason,requested_at,effective_at) VALUES($1,$2,'SCHEDULED','synthetic overdue deletion',clock_timestamp()-interval '2 days',clock_timestamp()-interval '1 day')`, "90000000-0000-4000-8000-000000008999", a.Subject); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restarted.ResubmitDocuments(a, key, revision, corrected); !errors.Is(err, ErrForbidden) {
		t.Fatalf("effective deletion allowed participant replay: %v", err)
	}
	for name, read := range map[string]func() error{
		"application": func() error { _, err := restarted.Application(a); return err },
		"catalog":     func() error { _, err := restarted.Catalog(a); return err },
		"work":        func() error { _, err := restarted.Work(a); return err },
		"dashboard":   func() error { _, err := restarted.Dashboard(a); return err },
	} {
		if err := read(); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s read after effective deletion: %v", name, err)
		}
	}
}
