//go:build integration

package supply

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000002_durable_vendor_runtime.up.sql",
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
	service, err := NewPostgresService(pool, func() time.Time { return now })
	if err != nil || service.Ready(ctx) != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
	vendor := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyVendorOne, Roles: []string{"VENDOR"}}
	registered, replay, err := service.Register(vendor, "supply-register-postgres-001", RegisterRequest{BusinessName: "P4U Local Services", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"})
	if err != nil || replay || registered.Revision != 1 || registered.Status != StatusRegistered {
		t.Fatalf("registered=%#v replay=%t err=%v", registered, replay, err)
	}
	restarted, _ := NewPostgresService(pool, func() time.Time { return now })
	replayed, replay, err := restarted.Register(vendor, "supply-register-postgres-001", RegisterRequest{BusinessName: "P4U Local Services", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"})
	if err != nil || !replay || replayed.ID != registered.ID {
		t.Fatalf("restart replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	if _, _, err := restarted.Register(vendor, "supply-register-postgres-001", RegisterRequest{BusinessName: "Changed Business", BusinessType: "SOLE_PROPRIETOR", ContactName: "Local Vendor"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("register conflict=%v", err)
	}
	value, _, err := restarted.SubmitDocuments(vendor, "supply-documents-postgres-01", 1, DocumentsRequest{Documents: []Document{{Kind: "IDENTITY_PROOF", AssetID: supplyAssetOne}, {Kind: "BUSINESS_LICENSE", AssetID: supplyAssetTwo}}})
	if err != nil || value.Status != StatusDocumentsSubmitted {
		t.Fatalf("documents=%#v err=%v", value, err)
	}
	ops := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyOpsOne, Roles: []string{"OPS_ADMIN"}}
	for _, transition := range []TransitionRequest{{Status: StatusOCRReview, Reason: "OCR review completed"}, {Status: StatusKYCReview, Reason: "KYC review completed"}, {Status: StatusFieldVisitRequired, Reason: "Field visit is required"}} {
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
	value, _, err = restarted.FieldCheckIn(officer, "supply-checkin-postgres-001", supplyVendorOne, value.Revision, CheckInRequest{Latitude: 13.0827, Longitude: 80.2707})
	if err != nil || value.Status != StatusFieldVisitPassed || value.Visit.CheckedInAt == nil {
		t.Fatalf("checkin=%#v err=%v", value, err)
	}
	value, _, err = restarted.SetZones(vendor, "supply-zones-postgres-00001", value.Revision, []ServiceZone{{ID: supplyZoneOne, PostalCodes: []string{"600001"}, Latitude: 13.0827, Longitude: 80.2707, RadiusKM: 8, PolicyVersion: "zone-policy-v1"}})
	if err != nil || len(value.Zones) != 1 {
		t.Fatalf("zones=%#v err=%v", value, err)
	}
	value, _, err = restarted.SubmitBank(vendor, "supply-bank-postgres-000001", value.Revision, BankAccount{Reference: "bankref_supplyvendor0001", HolderName: "P4U Local Services", Last4: "4567", IFSC: "HDFC0001234"})
	if err != nil || value.Status != StatusBankReview {
		t.Fatalf("bank=%#v err=%v", value, err)
	}
	finance := Actor{TenantID: supplyTenantOne, Country: "IN", Subject: supplyFinanceOne, Roles: []string{"FINANCE"}}
	value, _, err = restarted.VerifyBank(finance, "supply-bank-verify-postgres1", supplyVendorOne, value.Revision, "Bank penny verification passed")
	if err != nil || value.Bank.Status != "VERIFIED" {
		t.Fatalf("bank verified=%#v err=%v", value, err)
	}
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
	wrongTenant := Actor{TenantID: supplyTenantTwo, Country: "IN", Subject: supplyVendorOne, Roles: []string{"VENDOR"}}
	if _, err := restarted.Application(wrongTenant); !errors.Is(err, ErrNotFound) {
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
	if applicationCount != 1 || timelineCount != 9 || itemCount != 1 {
		t.Fatalf("applications=%d timeline=%d items=%d", applicationCount, timelineCount, itemCount)
	}
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
