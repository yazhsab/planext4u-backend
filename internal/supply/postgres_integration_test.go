//go:build integration

package supply

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/bankverification"
	"github.com/yazhsab/planext4u-backend/internal/kycattestation"
	"github.com/yazhsab/planext4u-backend/internal/ocrattestation"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
)

func TestPostgresVendorOnboardingCatalogReplayRestartAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("SUPPLY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("SUPPLY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS supply CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS supply CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/platform/000008_admin_roles.up.sql",
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000002_durable_vendor_runtime.up.sql",
		"../../migrations/supply/000004_application_submissions.up.sql",
		"../../migrations/supply/000007_rework_profile_history.up.sql",
		"../../migrations/supply/000009_reviewer_evidence_access.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	testsupport.PrivateIdentityMedia(t, ctx, pool, supplyTenantOne, supplyVendorOne, []string{supplyAssetOne, supplyAssetTwo})
	denyEvidence := false
	evidence := privateevidence.VerifyFunc(func(_ context.Context, scope privateevidence.Scope, ids []string) error {
		if denyEvidence {
			return privateevidence.ErrRejected
		}
		if scope.TenantID != supplyTenantOne || scope.Country != "IN" || scope.OwnerID != supplyVendorOne {
			return privateevidence.ErrRejected
		}
		for _, id := range ids {
			if id != supplyAssetOne && id != supplyAssetTwo {
				return privateevidence.ErrRejected
			}
		}
		return nil
	})
	denyBank := false
	bank := bankverification.VerifyFunc(func(_ context.Context, scope bankverification.Scope, account bankverification.Account) (string, error) {
		if denyBank || scope.TenantID != supplyTenantOne || scope.Country != "IN" || scope.OwnerID != supplyVendorOne || account.Reference != "bankref_supplyvendor0001" || account.Last4 != "4567" || account.IFSC != "HDFC0001234" {
			return "", bankverification.ErrRejected
		}
		return "synthetic_bank_verification_01", nil
	})
	denyKYC := false
	kyc := kycattestation.VerifyFunc(func(_ context.Context, scope kycattestation.Scope, documents []kycattestation.Document) (string, error) {
		if denyKYC || scope.TenantID != supplyTenantOne || scope.Country != "IN" || scope.OwnerID != supplyVendorOne || len(documents) != 2 ||
			documents[0] != (kycattestation.Document{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}) || documents[1] != (kycattestation.Document{Kind: "BUSINESS_LICENSE", AssetID: supplyAssetTwo}) {
			return "", kycattestation.ErrRejected
		}
		return "synthetic_kyc_attestation_01", nil
	})
	denyOCR := false
	ocr := ocrattestation.VerifyFunc(func(_ context.Context, scope ocrattestation.Scope, documents []ocrattestation.Document) (string, error) {
		if denyOCR || scope.TenantID != supplyTenantOne || scope.Country != "IN" || scope.OwnerID != supplyVendorOne || scope.DocumentRevision != 3 || len(documents) != 2 ||
			documents[0] != (ocrattestation.Document{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}) || documents[1] != (ocrattestation.Document{Kind: "BUSINESS_LICENSE", AssetID: supplyAssetTwo}) {
			return "", ocrattestation.ErrRejected
		}
		return "synthetic_ocr_attestation_01", nil
	})
	service, err := NewPostgresServiceWithOCR(pool, func() time.Time { return now }, evidence, bank, kyc, ocr)
	if err != nil || service.Ready(ctx) != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
	vendor := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyVendorOne, Roles: []string{"VENDOR"}}
	vendor.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, vendor.TenantID, vendor.Subject, "VENDOR")
	withoutSource := vendor
	withoutSource.SourceSessionID = ""
	if _, _, err := service.Register(withoutSource, "no-source-vendor-register-01", RegisterRequest{BusinessName: "P4U Local Services", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("registration without current source session: %v", err)
	}
	registered, replay, err := service.Register(vendor, "supply-register-postgres-001", RegisterRequest{BusinessName: "P4U Local Services", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"})
	if err != nil || replay || registered.Revision != 1 || registered.Status != StatusRegistered {
		t.Fatalf("registered=%#v replay=%t err=%v", registered, replay, err)
	}
	if registered.Documents == nil || registered.Zones == nil || registered.Timeline == nil || registered.AllowedActions == nil {
		t.Fatal("new application violates required array contract")
	}
	restarted, _ := NewPostgresServiceWithOCR(pool, func() time.Time { return now }, evidence, bank, kyc, ocr)
	for name, read := range map[string]func(Actor) error{
		"application": func(actor Actor) error { _, err := restarted.Application(actor); return err },
		"catalog":     func(actor Actor) error { _, err := restarted.Catalog(actor); return err },
		"work":        func(actor Actor) error { _, err := restarted.Work(actor); return err },
		"dashboard":   func(actor Actor) error { _, err := restarted.Dashboard(actor); return err },
	} {
		if err := read(withoutSource); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s read without current source session: %v", name, err)
		}
	}
	replayed, replay, err := restarted.Register(vendor, "supply-register-postgres-001", RegisterRequest{BusinessName: "P4U Local Services", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"})
	if err != nil || !replay || replayed.ID != registered.ID {
		t.Fatalf("restart replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	if replayed.Documents == nil || replayed.Zones == nil {
		t.Fatal("idempotent replay violates required array contract")
	}
	if _, _, err := restarted.Register(vendor, "supply-register-postgres-001", RegisterRequest{BusinessName: "Changed Business", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("register conflict=%v", err)
	}
	if _, _, err := restarted.SubmitDocuments(withoutSource, "no-source-documents-01", 1, DocumentsRequest{Documents: []Document{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "BUSINESS_LICENSE", AssetID: supplyAssetTwo}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("document mutation without current source session: %v", err)
	}
	for _, documents := range [][]Document{
		{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "OWNER_IDENTITY", AssetID: supplyAssetTwo}},
		{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "BUSINESS_LICENSE", AssetID: supplyAssetOne}},
		{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "UNREVIEWED_KIND", AssetID: supplyAssetTwo}},
	} {
		if _, _, err := restarted.SubmitDocuments(vendor, "invalid-document-set-postgres-01", 1, DocumentsRequest{Documents: documents}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("accepted incomplete or duplicate initial evidence: %v", err)
		}
	}
	unconfigured, _ := NewPostgresService(pool, func() time.Time { return now })
	if _, _, err := unconfigured.SubmitDocuments(vendor, "unconfigured-evidence-pg-01", 1, DocumentsRequest{Documents: []Document{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "BUSINESS_LICENSE", AssetID: supplyAssetTwo}}}); !errors.Is(err, privateevidence.ErrUnavailable) {
		t.Fatalf("missing evidence verifier accepted: %v", err)
	}
	if _, _, err := restarted.SubmitDocuments(vendor, "forged-evidence-pg-01", 1, DocumentsRequest{Documents: []Document{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "BUSINESS_LICENSE", AssetID: "90000000-0000-4000-8000-000000000999"}}}); !errors.Is(err, privateevidence.ErrRejected) {
		t.Fatalf("forged private evidence accepted: %v", err)
	}
	if before, err := restarted.Application(vendor); err != nil || before.Status != StatusRegistered || before.Revision != 1 {
		t.Fatalf("failed verification mutated application: %+v %v", before, err)
	}
	value, _, err := restarted.SubmitDocuments(vendor, "supply-documents-postgres-01", 1, DocumentsRequest{Documents: []Document{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne, OCRStatus: "VERIFIED", ReviewReason: "caller supplied review"}, {Kind: "BUSINESS_LICENSE", AssetID: supplyAssetTwo}}})
	if err != nil || value.Status != StatusDocumentsSubmitted {
		t.Fatalf("documents=%#v err=%v", value, err)
	}
	if value.Documents[0].OCRStatus != "PENDING" || value.Documents[0].ReviewReason != "" {
		t.Fatalf("caller controlled document review state: %+v", value.Documents[0])
	}
	ops := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyOpsOne, Roles: []string{"OPS_ADMIN"}, MFAVerified: true}
	ops.MFAVerified = true
	ops.OperatorRevision = 1
	ops.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, supplyTenantOne, supplyOpsOne)
	withoutMFA := ops
	withoutMFA.MFAVerified = false
	if _, _, err := restarted.TransitionForVendor(withoutMFA, "supply-no-mfa-kyc-pg01", supplyVendorOne, value.Revision, TransitionRequest{Status: StatusOCRReview, Reason: "MFA is required before KYC review"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("KYC review skipped MFA: %v", err)
	}
	for _, transition := range []TransitionRequest{{Status: StatusOCRReview, Reason: "OCR review completed"}, {Status: StatusKYCReview, Reason: "KYC review completed"}, {Status: StatusFieldVisitRequired, Reason: "Field visit is required"}} {
		if transition.Status == StatusKYCReview {
			withoutOCR, _ := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bank, kyc)
			if _, _, err := withoutOCR.TransitionForVendor(ops, "supply-no-ocr-provider-pg01", supplyVendorOne, value.Revision, transition); !errors.Is(err, ocrattestation.ErrUnavailable) {
				t.Fatalf("READY documents bypassed missing OCR attestation: %v", err)
			}
			denyOCR = true
			if _, _, err := restarted.TransitionForVendor(ops, "supply-denied-ocr-provider-pg01", supplyVendorOne, value.Revision, transition); !errors.Is(err, ocrattestation.ErrRejected) {
				t.Fatalf("rejected OCR attestation accepted: %v", err)
			}
			if unchanged, err := restarted.Application(vendor); err != nil || unchanged.Revision != value.Revision || unchanged.Status != StatusOCRReview || unchanged.Documents[0].OCRStatus == "VERIFIED" {
				t.Fatalf("failed OCR attestation changed application: %+v %v", unchanged, err)
			}
			denyOCR = false
			withoutKYC, _ := NewPostgresServiceWithOCR(pool, func() time.Time { return now }, evidence, bank, kycattestation.Unavailable(), ocr)
			if _, _, err := withoutKYC.TransitionForVendor(ops, "supply-no-kyc-provider-pg01", supplyVendorOne, value.Revision, transition); !errors.Is(err, kycattestation.ErrUnavailable) {
				t.Fatalf("READY documents bypassed missing KYC attestation: %v", err)
			}
			denyKYC = true
			if _, _, err := restarted.TransitionForVendor(ops, "supply-denied-kyc-provider-pg01", supplyVendorOne, value.Revision, transition); !errors.Is(err, kycattestation.ErrRejected) {
				t.Fatalf("rejected KYC attestation accepted: %v", err)
			}
			if unchanged, err := restarted.Application(vendor); err != nil || unchanged.Revision != value.Revision || unchanged.Status != StatusOCRReview || unchanged.Documents[0].OCRStatus == "VERIFIED" {
				t.Fatalf("failed KYC attestation changed application: %+v %v", unchanged, err)
			}
			denyKYC = false
		}
		value, _, err = restarted.TransitionForVendor(ops, "supply-transition-"+string(transition.Status)+"-001", supplyVendorOne, value.Revision, transition)
		if err != nil {
			t.Fatalf("transition %s: %v", transition.Status, err)
		}
	}
	value, _, err = restarted.ScheduleVisit(vendor, "supply-visit-postgres-0001", value.Revision, VisitRequest{ScheduledAt: now.Add(24 * time.Hour), Latitude: 13.0827, Longitude: 80.2707, AllowedRadiusM: 200})
	if err != nil || value.Visit == nil || value.Status != StatusFieldVisitScheduled {
		t.Fatalf("visit=%#v err=%v", value, err)
	}
	officer := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyOfficerOne, Roles: []string{"FIELD_OFFICER"}}
	officer.MFAVerified = true
	officer.OperatorRevision = 1
	officer.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, supplyTenantOne, supplyOfficerOne)
	checkInKey := "supply-checkin-postgres-001"
	checkInPoint := CheckInRequest{Latitude: 13.0827, Longitude: 80.2707}
	if _, _, err = restarted.FieldCheckIn(officer, checkInKey, supplyVendorOne, value.Revision, checkInPoint); !errors.Is(err, ErrForbidden) {
		t.Fatalf("country reviewer without field assignment attested visit: %v", err)
	}
	queueActor := officer
	queueActor.Roles = []string{"OPS_ADMIN"}
	queue, err := restarted.Applications(ctx, queueActor, participantreview.Filter{Status: string(StatusFieldVisitScheduled)})
	if err != nil || len(queue.Items) != 1 || len(queue.Items[0].Actions) != 0 {
		t.Fatalf("unassigned country field action: %+v %v", queue, err)
	}
	const fieldNode = "90000000-0000-4000-8000-000000000921"
	const fieldRole = "90000000-0000-4000-8000-000000000922"
	if _, err = pool.Exec(ctx, `INSERT INTO admin.organization_nodes(tenant_id,country,id,kind,name) VALUES($1,'IN',$2,'TERRITORY','Synthetic field territory')`, supplyTenantOne, fieldNode); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.job_roles(tenant_id,country,id,name,capabilities) VALUES($1,'IN',$2,'Synthetic field officer',ARRAY['resource.read','resource.command'])`, supplyTenantOne, fieldRole); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.staff_assignments(tenant_id,country,subject_id,node_id,role_id,status) VALUES($1,'IN',$2,$3,$4,'ACTIVE')`, supplyTenantOne, supplyOfficerOne, fieldNode, fieldRole); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin.participant_territory_bindings(tenant_id,country,kind,participant_id,node_id,active,revision,reason) VALUES($1,'IN','VENDOR',$2,$3,true,1,'Synthetic visit assignment')`, supplyTenantOne, supplyVendorOne, fieldNode); err != nil {
		t.Fatal(err)
	}
	queue, err = restarted.Applications(ctx, queueActor, participantreview.Filter{Status: string(StatusFieldVisitScheduled)})
	if err != nil || len(queue.Items) != 1 || len(queue.Items[0].Actions) != 1 || queue.Items[0].Actions[0] != "FIELD_CHECK_IN" {
		t.Fatalf("assigned country field action: %+v %v", queue, err)
	}
	value, _, err = restarted.FieldCheckIn(officer, checkInKey, supplyVendorOne, value.Revision, checkInPoint)
	if err != nil || value.Status != StatusFieldVisitPassed || value.Visit.CheckedInAt == nil {
		t.Fatalf("checkin=%#v err=%v", value, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin.staff_assignments SET status='SUSPENDED',revision=revision+1 WHERE tenant_id=$1 AND country='IN' AND subject_id=$2 AND node_id=$3`, supplyTenantOne, supplyOfficerOne, fieldNode); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restarted.FieldCheckIn(officer, checkInKey, supplyVendorOne, value.Revision-1, checkInPoint); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked field assignment replayed attestation: %v", err)
	}
	value, _, err = restarted.SetZones(vendor, "supply-zones-postgres-00001", value.Revision, []ServiceZone{{ID: supplyZoneOne, PostalCodes: []string{"600001"}, Latitude: 13.0827, Longitude: 80.2707, RadiusKM: 8, PolicyVersion: "zone-policy-v1"}})
	if err != nil || len(value.Zones) != 1 {
		t.Fatalf("zones=%#v err=%v", value, err)
	}
	value, _, err = restarted.SubmitBank(vendor, "supply-bank-postgres-000001", value.Revision, BankAccount{Reference: "bankref_supplyvendor0001", HolderName: "P4U Local Services", Last4: "4567", IFSC: "HDFC0001234"})
	if err != nil || value.Status != StatusBankReview {
		t.Fatalf("bank=%#v err=%v", value, err)
	}
	finance := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyFinanceOne, Roles: []string{"FINANCE"}, MFAVerified: true}
	finance.MFAVerified = true
	finance.OperatorRevision = 1
	finance.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, supplyTenantOne, supplyFinanceOne)
	withoutMFA = finance
	withoutMFA.MFAVerified = false
	if _, _, err := restarted.VerifyBank(withoutMFA, "supply-no-mfa-bank-pg01", supplyVendorOne, value.Revision, "MFA is required before bank verification"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bank review skipped MFA: %v", err)
	}
	noBank, _ := NewPostgresService(pool, func() time.Time { return now }, evidence)
	if _, _, err := noBank.VerifyBank(finance, "missing-bank-provider-pg01", supplyVendorOne, value.Revision, "Bank provider is not available"); !errors.Is(err, bankverification.ErrUnavailable) {
		t.Fatalf("missing bank provider approved: %v", err)
	}
	denyBank = true
	if _, _, err := restarted.VerifyBank(finance, "rejected-bank-provider-pg01", supplyVendorOne, value.Revision, "Provider denied this bank proof"); !errors.Is(err, bankverification.ErrRejected) {
		t.Fatalf("rejected bank approved: %v", err)
	}
	if unchanged, err := restarted.Application(vendor); err != nil || unchanged.Revision != value.Revision || unchanged.Bank.Status != "PENDING_VERIFICATION" {
		t.Fatalf("failed bank verification mutated state: %+v %v", unchanged, err)
	}
	denyBank = false
	value, _, err = restarted.VerifyBank(finance, "supply-bank-verify-postgres1", supplyVendorOne, value.Revision, "Bank penny verification passed")
	if err != nil || value.Bank.Status != "VERIFIED" {
		t.Fatalf("bank verified=%#v err=%v", value, err)
	}
	denyEvidence = true
	if _, _, err := restarted.TransitionForVendor(ops, "supply-recheck-rejected-pg1", supplyVendorOne, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "Document was deleted during review"}); !errors.Is(err, privateevidence.ErrRejected) {
		t.Fatalf("approval skipped current evidence: %v", err)
	}
	if unchanged, err := restarted.Application(vendor); err != nil || unchanged.Revision != value.Revision || unchanged.Verified {
		t.Fatalf("failed approval changed application: %+v %v", unchanged, err)
	}
	denyEvidence = false
	denyKYC = true
	if _, _, err := restarted.TransitionForVendor(ops, "revoked-kyc-approval-pg01", supplyVendorOne, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "KYC proof was revoked before approval"}); !errors.Is(err, kycattestation.ErrRejected) {
		t.Fatalf("approval skipped current KYC attestation: %v", err)
	}
	if unchanged, err := restarted.Application(vendor); err != nil || unchanged.Revision != value.Revision || unchanged.Verified {
		t.Fatalf("failed KYC reattestation changed application: %+v %v", unchanged, err)
	}
	denyKYC = false
	denyBank = true
	if _, _, err := restarted.TransitionForVendor(ops, "revoked-bank-approval-pg01", supplyVendorOne, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "Bank proof was revoked before approval"}); !errors.Is(err, bankverification.ErrRejected) {
		t.Fatalf("approval skipped current bank: %v", err)
	}
	if unchanged, err := restarted.Application(vendor); err != nil || unchanged.Revision != value.Revision || unchanged.Verified {
		t.Fatalf("failed bank revalidation changed application: %+v %v", unchanged, err)
	}
	denyBank = false
	value, _, err = restarted.TransitionForVendor(ops, "supply-approve-postgres-0001", supplyVendorOne, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "All onboarding controls passed"})
	if err != nil || !value.Verified || value.Status != StatusApproved {
		t.Fatalf("approved=%#v err=%v", value, err)
	}
	item, replay, err := restarted.UpsertCatalog(vendor, "supply-catalog-postgres-0001", "", 0, CatalogRequest{Kind: CatalogService, Name: "Electrical repair", Description: "Verified home electrical repair", SKU: "ELEC-SERVICE-01", Price: Money{AmountMinor: 50000, Currency: "INR"}})
	if err != nil || replay || item.Revision != 1 {
		t.Fatalf("catalog=%#v replay=%t err=%v", item, replay, err)
	}
	item, _, err = restarted.SetInventory(vendor, "supply-inventory-postgres-01", item.ID, item.Revision, 10)
	if err != nil || item.Stock != 10 {
		t.Fatalf("inventory=%#v err=%v", item, err)
	}
	item, _, err = restarted.SetSchedule(vendor, "supply-schedule-postgres-001", item.ID, item.Revision, []ScheduleWindow{{Weekday: 1, StartsMinute: 540, EndsMinute: 1020, TimeZone: "Asia/Kolkata", Capacity: 8, BufferMinute: 15}})
	if err != nil || len(item.Schedules) != 1 {
		t.Fatalf("schedule=%#v err=%v", item, err)
	}
	item, _, err = restarted.ApproveCatalog(ops, "supply-catalog-approve-00001", item.ID, item.Revision, true, "Catalog policy checks passed")
	if err != nil || !item.Active || item.ApprovalStatus != "APPROVED" {
		t.Fatalf("catalog approval=%#v err=%v", item, err)
	}
	dashboard, err := restarted.Dashboard(vendor)
	if err != nil || dashboard.CatalogItems != 1 || dashboard.Application.Status != StatusApproved {
		t.Fatalf("dashboard=%#v err=%v", dashboard, err)
	}
	if catalog, err := restarted.Catalog(vendor); err != nil || len(catalog) != 1 || catalog[0].ID != item.ID {
		t.Fatalf("current vendor catalog: %+v %v", catalog, err)
	}
	if catalog, err := restarted.Catalog(ops); err != nil || len(catalog) != 1 || catalog[0].ID != item.ID {
		t.Fatalf("current reviewer catalog: %+v %v", catalog, err)
	}
	if work, err := restarted.Work(vendor); err != nil || work == nil {
		t.Fatalf("current vendor work: %+v %v", work, err)
	}
	staleReviewer := ops
	staleReviewer.OperatorSessionID = ""
	if _, err := restarted.Catalog(staleReviewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("catalog accepted reviewer without current source session: %v", err)
	}
	queuedActor := vendor
	queuedActor.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, vendor.TenantID, vendor.Subject, "VENDOR")
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT 1 FROM identity.sessions WHERE id=$1 FOR UPDATE`, queuedActor.SourceSessionID); err != nil {
		t.Fatal(err)
	}
	queuedRead := make(chan error, 1)
	go func() { _, readErr := restarted.Application(queuedActor); queuedRead <- readErr }()
	waiting := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%identity.lock_participant_session%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
	}
	if !waiting {
		t.Fatal("application read did not wait on the current source session")
	}
	if _, err = blocker.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, queuedActor.SourceSessionID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-queuedRead; !errors.Is(err, ErrForbidden) {
		t.Fatalf("queued application read survived source-session revocation: %v", err)
	}
	wrongTenant := Actor{TenantID: supplyTenantTwo, Country: "IN", Subject: supplyVendorOne, Roles: []string{"VENDOR"}}
	if _, err := restarted.Application(wrongTenant); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant application=%v", err)
	}
	var applicationCount, timelineCount, itemCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supply.vendor_applications`).Scan(&applicationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supply.application_timeline`).Scan(&timelineCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supply.catalog_items`).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if applicationCount != 1 || timelineCount != 14 || itemCount != 1 {
		t.Fatalf("applications=%d timeline=%d items=%d", applicationCount, timelineCount, itemCount)
	}
	var ocrEvidenceEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supply.application_timeline WHERE application_id=$1 AND actor_subject_id=$2 AND position($3 in reason)>0 AND position($4 in reason)>0`, registered.ID, supplyOpsOne, "synthetic_ocr_attestation_01", "document revision 3").Scan(&ocrEvidenceEvents); err != nil || ocrEvidenceEvents != 1 {
		t.Fatalf("expected one revision-bound OCR evidence event: count=%d err=%v", ocrEvidenceEvents, err)
	}
	var kycEvidenceEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supply.application_timeline WHERE application_id=$1 AND actor_subject_id=$2 AND position($3 in reason)>0`, registered.ID, supplyOpsOne, "synthetic_kyc_attestation_01").Scan(&kycEvidenceEvents); err != nil || kycEvidenceEvents != 2 {
		t.Fatalf("expected KYC and approval reattestation events: count=%d err=%v", kycEvidenceEvents, err)
	}
	var bankEvidenceEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supply.application_timeline WHERE application_id=$1 AND actor_subject_id IN ($2,$3) AND position($4 in reason)>0`, registered.ID, supplyFinanceOne, supplyOpsOne, "provider bank verification: synthetic_bank_verification_01").Scan(&bankEvidenceEvents); err != nil || bankEvidenceEvents != 2 {
		t.Fatalf("expected exactly verification and approval bank evidence events: count=%d err=%v", bankEvidenceEvents, err)
	}
	verifyVendorParticipantReviewAuthority(t, ctx, pool, databaseURL, ops)
}

const (
	supplyTenantOne  = "3433b29a-d67d-4af6-b95e-6dc1e4193a56"
	supplyTenantTwo  = "1a49b944-32e6-4503-bca6-392a48f814d3"
	supplyVendorOne  = "419a488c-9d3a-4925-9ce7-7c8d226777b0"
	supplyOpsOne     = "5f73d091-2c84-4283-b24b-cb7b92b44d80"
	supplyOfficerOne = "dd09d2a4-d7a0-45f9-a5ae-8cd686493aa0"
	supplyFinanceOne = "b39f7471-a2aa-4852-b764-cfde774041a9"
	supplyAssetOne   = "c652b76c-7c3c-4157-8672-e38018ec2e86"
	supplyAssetTwo   = "2a231c62-6fe6-4e87-bad7-4fd07b1cdce1"
	supplyZoneOne    = "24867800-80ea-4de3-b331-194413dc5c6d"
)
