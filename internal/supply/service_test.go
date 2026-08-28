package supply

import (
	"errors"
	"testing"
	"time"
)

func TestBEP4005VendorOnboardingRequiresScopedKYCVisitZonesAndBank(t *testing.T) {
	service, now := supplyFixture(t)
	vendor := supplyActorFor("vendor-synthetic-001", "VENDOR")
	value, replay, err := service.Register(vendor, "vendor-register-0001", RegisterRequest{BusinessName: "Planext Services", BusinessType: "Home services", ContactName: "Vendor Owner"})
	if err != nil || replay || value.Status != StatusRegistered || value.Revision != 1 {
		t.Fatalf("register=%#v replay=%v err=%v", value, replay, err)
	}
	again, replay, err := service.Register(vendor, "vendor-register-0001", RegisterRequest{BusinessName: "Planext Services", BusinessType: "Home services", ContactName: "Vendor Owner"})
	if err != nil || !replay || again.ID != value.ID {
		t.Fatalf("register replay=%#v replay=%v err=%v", again, replay, err)
	}
	value, _, err = service.SubmitDocuments(vendor, "vendor-documents-0001", value.Revision, DocumentsRequest{Documents: []Document{
		{Kind: "BUSINESS_REGISTRATION", AssetID: "asset-private-business-001"},
		{Kind: "OWNER_IDENTITY", AssetID: "asset-private-owner-001"},
	}})
	if err != nil || value.Status != StatusDocumentsSubmitted || value.Documents[0].OCRStatus != "PENDING" {
		t.Fatalf("documents=%#v err=%v", value, err)
	}
	admin := supplyActorFor("ops-admin-001", "OPS_ADMIN")
	value, _, err = service.TransitionForVendor(admin, "vendor-ocr-review-0001", vendor.Subject, value.Revision, TransitionRequest{Status: StatusOCRReview, Reason: "OCR extraction completed"})
	if err != nil || value.Documents[0].OCRStatus != "REVIEW_REQUIRED" {
		t.Fatalf("ocr=%#v err=%v", value, err)
	}
	value, _, err = service.TransitionForVendor(admin, "vendor-kyc-review-0001", vendor.Subject, value.Revision, TransitionRequest{Status: StatusKYCReview, Reason: "Documents manually verified"})
	if err != nil || value.Documents[0].OCRStatus != "VERIFIED" {
		t.Fatalf("kyc=%#v err=%v", value, err)
	}
	value, _, err = service.TransitionForVendor(admin, "vendor-visit-required-1", vendor.Subject, value.Revision, TransitionRequest{Status: StatusFieldVisitRequired, Reason: "Physical verification required"})
	if err != nil {
		t.Fatal(err)
	}
	value, _, err = service.ScheduleVisit(vendor, "vendor-visit-schedule-1", value.Revision, VisitRequest{ScheduledAt: now.Add(24 * time.Hour), Latitude: 13.0827, Longitude: 80.2707, AllowedRadiusM: 200})
	if err != nil || value.Status != StatusFieldVisitScheduled {
		t.Fatalf("visit=%#v err=%v", value, err)
	}
	officer := supplyActorFor("field-officer-001", "FIELD_OFFICER")
	if _, _, err := service.FieldCheckIn(officer, "vendor-checkin-far-0001", vendor.Subject, value.Revision, CheckInRequest{Latitude: 12.9716, Longitude: 77.5946}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("far check-in error=%v", err)
	}
	value, _, err = service.FieldCheckIn(officer, "vendor-checkin-near-001", vendor.Subject, value.Revision, CheckInRequest{Latitude: 13.08271, Longitude: 80.27071})
	if err != nil || value.Status != StatusFieldVisitPassed || value.Visit.CheckedInAt == nil || value.Visit.OfficerID != officer.Subject {
		t.Fatalf("check-in=%#v err=%v", value, err)
	}
	value, _, err = service.SetZones(vendor, "vendor-zone-command-001", value.Revision, []ServiceZone{{ID: "zone-chennai-core", PostalCodes: []string{"600001", "600002"}, Latitude: 13.0827, Longitude: 80.2707, RadiusKM: 25, PolicyVersion: "zone-policy-v1"}})
	if err != nil || len(value.Zones) != 1 {
		t.Fatalf("zones=%#v err=%v", value, err)
	}
	value, _, err = service.SubmitBank(vendor, "vendor-bank-command-001", value.Revision, BankAccount{Reference: "bankref_synthetic001", HolderName: "Vendor Owner", Last4: "1234", IFSC: "HDFC0001234"})
	if err != nil || value.Status != StatusBankReview || value.Bank.Status != "PENDING_VERIFICATION" {
		t.Fatalf("bank=%#v err=%v", value, err)
	}
	finance := supplyActorFor("finance-reviewer-001", "FINANCE")
	value, _, err = service.VerifyBank(finance, "vendor-bank-verify-001", vendor.Subject, value.Revision, "Penny-drop account match passed")
	if err != nil || value.Bank.Status != "VERIFIED" {
		t.Fatalf("bank verify=%#v err=%v", value, err)
	}
	value, _, err = service.TransitionForVendor(admin, "vendor-approve-command-1", vendor.Subject, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "All onboarding controls passed"})
	if err != nil || !value.Verified || value.Status != StatusApproved || len(value.Timeline) != 9 {
		t.Fatalf("approved=%#v err=%v", value, err)
	}
}

func TestBEP4005VendorOnboardingEnforcesOwnershipRevisionAndApprovalControls(t *testing.T) {
	service, _ := supplyFixture(t)
	vendor := supplyActorFor("vendor-owner-001", "VENDOR")
	value, _, _ := service.Register(vendor, "vendor-owner-register-1", RegisterRequest{BusinessName: "Owner Business", BusinessType: "Retail store", ContactName: "Owner Person"})
	attacker := supplyActorFor("vendor-attacker-001", "VENDOR")
	if _, err := service.Application(attacker); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner application error=%v", err)
	}
	if _, _, err := service.SubmitDocuments(vendor, "vendor-owner-documents-1", 99, DocumentsRequest{Documents: []Document{{Kind: "BUSINESS_REGISTRATION", AssetID: "asset-private-business-002"}, {Kind: "OWNER_IDENTITY", AssetID: "asset-private-owner-002"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error=%v", err)
	}
	admin := supplyActorFor("ops-admin-001", "OPS_ADMIN")
	if _, _, err := service.TransitionForVendor(admin, "vendor-owner-approve-01", vendor.Subject, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "Attempt premature approval"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("premature approval error=%v", err)
	}
}

func TestBEP4006VendorCatalogScheduleInventoryQueuesAndDashboard(t *testing.T) {
	service, now := supplyFixture(t)
	vendor := approvedVendorFixture(t, service, now, "vendor-catalog-owner")
	item, replay, err := service.UpsertCatalog(vendor, "vendor-catalog-create-1", "", 0, CatalogRequest{Kind: CatalogService, Name: "Deep cleaning", Description: "Verified two-person home cleaning", SKU: "CLEAN-DEEP-001", Price: Money{AmountMinor: 20000, Currency: "INR"}})
	if err != nil || replay || item.ApprovalStatus != "PENDING_APPROVAL" || item.Active {
		t.Fatalf("item=%#v replay=%v err=%v", item, replay, err)
	}
	if _, _, err := service.SetSchedule(vendor, "vendor-schedule-invalid-1", item.ID, item.Revision, []ScheduleWindow{
		{Weekday: 1, StartsMinute: 540, EndsMinute: 720, TimeZone: "Asia/Kolkata", Capacity: 2},
		{Weekday: 1, StartsMinute: 600, EndsMinute: 780, TimeZone: "Asia/Kolkata", Capacity: 2},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("overlap error=%v", err)
	}
	item, _, err = service.SetSchedule(vendor, "vendor-schedule-valid-01", item.ID, item.Revision, []ScheduleWindow{{Weekday: 1, StartsMinute: 540, EndsMinute: 720, TimeZone: "Asia/Kolkata", Capacity: 2, BufferMinute: 30}})
	if err != nil {
		t.Fatal(err)
	}
	item, _, err = service.SetInventory(vendor, "vendor-inventory-set-001", item.ID, item.Revision, 4)
	if err != nil || item.Stock != 4 {
		t.Fatalf("inventory=%#v err=%v", item, err)
	}
	admin := supplyActorFor("ops-admin-001", "OPS_ADMIN")
	item, _, err = service.ApproveCatalog(admin, "vendor-catalog-approve1", item.ID, item.Revision, true, "Catalog policy checks passed")
	if err != nil || !item.Active || item.ApprovalStatus != "APPROVED" {
		t.Fatalf("approval=%#v err=%v", item, err)
	}
	attacker := supplyActorFor("vendor-catalog-attacker", "VENDOR")
	if _, _, err := service.SetInventory(attacker, "vendor-attacker-stock-1", item.ID, item.Revision, 100); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner inventory error=%v", err)
	}
	if err := service.SeedWork(vendor, WorkItem{ID: "work-booking-001", VendorID: vendor.Subject, ReferenceType: "SERVICE_BOOKING", ReferenceID: "booking-001", Status: "NEW", Total: Money{AmountMinor: 20000, Currency: "INR"}, CustomerLabel: "Customer P."}); err != nil {
		t.Fatal(err)
	}
	work, replay, err := service.TransitionWork(vendor, "vendor-work-accept-0001", "work-booking-001", "ACCEPTED")
	if err != nil || replay || work.Status != "ACCEPTED" {
		t.Fatalf("work=%#v replay=%v err=%v", work, replay, err)
	}
	if _, _, err := service.TransitionWork(vendor, "vendor-work-invalid-001", work.ID, "COMPLETED"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unsafe transition error=%v", err)
	}
	promotion, _, err := service.UpsertPromotion(vendor, "vendor-promotion-create1", Promotion{Title: "Chennai launch", Kind: "BOOST", Budget: Money{AmountMinor: 50000, Currency: "INR"}, StartsAt: now.Add(time.Hour), EndsAt: now.Add(7 * 24 * time.Hour)})
	if err != nil || promotion.Status != "DRAFT" {
		t.Fatalf("promotion=%#v err=%v", promotion, err)
	}
	dashboard, err := service.Dashboard(vendor)
	if err != nil || dashboard.CatalogItems != 1 || dashboard.LowStockItems != 1 || dashboard.OpenWorkItems != 1 || dashboard.Application.Status != StatusApproved {
		t.Fatalf("dashboard=%#v err=%v", dashboard, err)
	}
}

func supplyFixture(t *testing.T) (*Service, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	service, err := NewService(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service, now
}

func supplyActorFor(subject, role string) Actor {
	return Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: subject, Roles: []string{role}}
}

func approvedVendorFixture(t *testing.T, service *Service, now time.Time, subject string) Actor {
	t.Helper()
	vendor := supplyActorFor(subject, "VENDOR")
	value, _, err := service.Register(vendor, "approved-register-"+subject, RegisterRequest{BusinessName: "Approved Business", BusinessType: "Home services", ContactName: "Approved Owner"})
	if err != nil {
		t.Fatal(err)
	}
	value, _, _ = service.SubmitDocuments(vendor, "approved-documents-"+subject, value.Revision, DocumentsRequest{Documents: []Document{{Kind: "BUSINESS_REGISTRATION", AssetID: "asset-approved-business"}, {Kind: "OWNER_IDENTITY", AssetID: "asset-approved-owner"}}})
	admin := supplyActorFor("ops-admin-001", "OPS_ADMIN")
	value, _, _ = service.TransitionForVendor(admin, "approved-ocr-"+subject, subject, value.Revision, TransitionRequest{Status: StatusOCRReview, Reason: "OCR extraction completed"})
	value, _, _ = service.TransitionForVendor(admin, "approved-kyc-"+subject, subject, value.Revision, TransitionRequest{Status: StatusKYCReview, Reason: "KYC review completed"})
	value, _, _ = service.TransitionForVendor(admin, "approved-visit-"+subject, subject, value.Revision, TransitionRequest{Status: StatusFieldVisitRequired, Reason: "Field visit required"})
	value, _, _ = service.ScheduleVisit(vendor, "approved-schedule-"+subject, value.Revision, VisitRequest{ScheduledAt: now.Add(24 * time.Hour), Latitude: 13.0827, Longitude: 80.2707, AllowedRadiusM: 200})
	value, _, _ = service.FieldCheckIn(supplyActorFor("field-officer-001", "FIELD_OFFICER"), "approved-checkin-"+subject, subject, value.Revision, CheckInRequest{Latitude: 13.08271, Longitude: 80.27071})
	value, _, _ = service.SetZones(vendor, "approved-zones-"+subject, value.Revision, []ServiceZone{{ID: "zone-chennai", PostalCodes: []string{"600001"}, Latitude: 13.0827, Longitude: 80.2707, RadiusKM: 25, PolicyVersion: "zone-policy-v1"}})
	value, _, _ = service.SubmitBank(vendor, "approved-bank-"+subject, value.Revision, BankAccount{Reference: "bankref_synthetic002", HolderName: "Approved Owner", Last4: "1234", IFSC: "HDFC0001234"})
	value, _, _ = service.VerifyBank(supplyActorFor("finance-reviewer-001", "FINANCE"), "approved-verify-"+subject, subject, value.Revision, "Penny-drop verification passed")
	value, _, err = service.TransitionForVendor(admin, "approved-final-"+subject, subject, value.Revision, TransitionRequest{Status: StatusApproved, Reason: "All onboarding checks passed"})
	if err != nil || !value.Verified {
		t.Fatalf("approve fixture=%#v err=%v", value, err)
	}
	return vendor
}
