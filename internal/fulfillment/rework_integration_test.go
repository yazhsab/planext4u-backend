//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
	"testing"
	"time"
)

func verifyRiderRework(t *testing.T, ctx context.Context, pool, owner *pgxpool.Pool, reviewer Actor) {
	t.Helper()
	const subject = "60000000-0000-4000-8000-000000008902"
	ids := []string{"90000000-0000-4000-8000-000000008905", "90000000-0000-4000-8000-000000008906"}
	testsupport.AddPrivateIdentityMedia(t, ctx, pool, reviewer.TenantID, subject, ids)
	a := Actor{TenantID: reviewer.TenantID, Country: "IN", Subject: subject, Roles: []string{"RIDER"}}
	a.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, a.TenantID, a.Subject, "RIDER")
	verifier := privateevidence.VerifyFunc(func(_ context.Context, scope privateevidence.Scope, assets []string) error {
		if scope.OwnerID != subject || scope.TenantID != a.TenantID || scope.Country != "IN" {
			return privateevidence.ErrRejected
		}
		for _, asset := range assets {
			if asset != ids[0] && asset != ids[1] {
				return privateevidence.ErrRejected
			}
		}
		return nil
	})
	service, err := NewPostgresService(owner, time.Now, verifier)
	if err != nil {
		t.Fatal(err)
	}
	input := RiderRegistrationRequest{FullName: "Synthetic correction rider", VehicleType: "BICYCLE", VehicleNumber: "CYC123", Documents: []RiderDocument{{Kind: "IDENTITY", AssetID: ids[0]}}, BankReference: "bankref_synthetic_rework", Zones: []string{"600001"}}
	value, _, err := service.RegisterRider(a, "rider-rework-register-01", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ResubmitRider(a, "rider-not-rejected-01", value.Revision, input); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending application bypassed review: %v", err)
	}
	value, _, err = service.ReviewRider(reviewer, "rider-rework-reject-01", subject, value.Revision, false, "Replace unreadable identity document")
	if err != nil || !value.ReworkAllowed || value.ReviewReason == "" {
		t.Fatalf("rejection feedback %+v %v", value, err)
	}
	readback, err := service.Rider(a)
	if err != nil || !readback.ReworkAllowed || len(readback.AllowedActions) != 1 || readback.AllowedActions[0] != "RESUBMIT_APPLICATION" {
		t.Fatalf("normal rework readback %+v %v", readback, err)
	}
	revision := value.Revision
	input.Documents = []RiderDocument{{Kind: "IDENTITY", AssetID: ids[1]}}
	key := "rider-rework-corrected-01"
	for name, change := range map[string]func(*Actor){"source": func(v *Actor) { v.SourceSessionID = "" }, "country": func(v *Actor) { v.Country = "NG" }, "role": func(v *Actor) { v.Roles = []string{"CUSTOMER"} }} {
		bad := a
		change(&bad)
		if _, _, err = service.ResubmitRider(bad, key, revision, input); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s %v", name, err)
		}
	}
	if _, _, err = service.ResubmitRider(a, key, revision-1, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision %v", err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fulfillment.rework_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$;CREATE TRIGGER rework_failure BEFORE INSERT ON fulfillment.audit_events FOR EACH ROW EXECUTE FUNCTION fulfillment.rework_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ResubmitRider(a, key, revision, input); err == nil {
		t.Fatal("rework acknowledged without audit")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER rework_failure ON fulfillment.audit_events;DROP FUNCTION fulfillment.rework_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM media.identity_evidence_claims WHERE asset_id=$1`, ids[1]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback claimed correction %d %v", count, err)
	}
	value, replay, err := service.ResubmitRider(a, key, revision, input)
	if err != nil || replay || value.Status != RiderKYCReview || value.ReworkAllowed || value.BankStatus != "PENDING_VERIFICATION" || value.ReviewReason != "" {
		t.Fatalf("resubmit %+v %t %v", value, replay, err)
	}
	restarted, _ := NewPostgresService(owner, time.Now, verifier)
	if _, replay, err = restarted.ResubmitRider(a, key, revision, input); err != nil || !replay {
		t.Fatalf("restart replay %t %v", replay, err)
	}
	if _, _, err = restarted.ResubmitRider(a, key, revision+1, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed revision replay %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment.rider_submissions WHERE profile_id=$1`, value.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("history %d %v", count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM media.identity_evidence_claims WHERE asset_id=ANY($1::uuid[])`, ids).Scan(&count); err != nil || count != 2 {
		t.Fatalf("old/new claims %d %v", count, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, a.SourceSessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restarted.ResubmitRider(a, key, revision, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked session replay %v", err)
	}
	// Operational suspension must never become a self-service approval reset.
	a.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, a.TenantID, a.Subject, "RIDER")
	if _, err = pool.Exec(ctx, `UPDATE fulfillment.rider_profiles SET status='SUSPENDED',rework_allowed=false WHERE id=$1`, value.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restarted.ResubmitRider(a, "operational-suspension-01", value.Revision, input); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("operational suspension eligible: %v", err)
	}
}
