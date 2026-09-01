//go:build integration

package localverticals

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	localTenantOne = "12000000-0000-4000-8000-000000000001"
	localTenantTwo = "12000000-0000-4000-8000-000000000002"
	localOwner     = "22000000-0000-4000-8000-000000000001"
	localBuyer     = "22000000-0000-4000-8000-000000000002"
	localVerifier  = "32000000-0000-4000-8000-000000000001"
	localMedia     = "42000000-0000-4000-8000-000000000001"
)

func TestPostgresLocalVerticalsHomesClassifiedEncryptionRestartAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("LOCAL_VERTICALS_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("LOCAL_VERTICALS_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS local_verticals CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS local_verticals CASCADE`)
	})
	for _, path := range []string{"../../migrations/platform/000006_phase5_roles.up.sql", "../../migrations/local_verticals/000001_local_verticals.up.sql", "../../migrations/local_verticals/000002_durable_marketplace_runtime.up.sql"} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `INSERT INTO local_verticals.policies (tenant_id,country,version,currency,estimator_version,review_terms,classified_lifetime_seconds,feature_lifetime_seconds,report_review_threshold,updated_at) VALUES ($1,'IN','market-v1','INR','homes-avm-v1',ARRAY['manual-review'],2592000,604800,1,$2)`, localTenantOne, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO local_verticals.owner_verifications (tenant_id,country,owner_identity_id,status,evidence_reference,verified_by_identity_id,verified_at) VALUES ($1,'IN',$2,'VERIFIED','kyc-evidence-001',$3,$4)`, localTenantOne, localOwner, localVerifier, now)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("local-contact-encryption-key-001")
	service, err := NewPostgresService(pool, func() time.Time { return now }, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	owner := Actor{TenantID: localTenantOne, Country: "IN", Subject: localOwner, Roles: []string{"CUSTOMER"}}
	buyer := Actor{TenantID: localTenantOne, Country: "IN", Subject: localBuyer, Roles: []string{"CUSTOMER"}}
	homeRequest := HomeListingRequest{Title: "Verified Chennai Villa", PropertyType: "VILLA", Purpose: "SALE", Locality: "Chennai", Latitude: 13.0827, Longitude: 80.2707, AreaSqFt: 2000, Bedrooms: 3, Price: Money{AmountMinor: 100000000, Currency: "INR"}, Amenities: []string{"Parking", "Garden"}, MediaAssetIDs: []string{localMedia}}
	home, replay, err := service.CreateHome(owner, "home-create-postgres-000001", homeRequest)
	if err != nil || replay || !home.KYCVerified || home.Status != "DRAFT" || home.Estimate.Amount.AmountMinor != 112000000 {
		t.Fatalf("home=%#v replay=%t err=%v", home, replay, err)
	}
	restarted, err := NewPostgresService(pool, func() time.Time { return now }, key)
	if err != nil {
		t.Fatal(err)
	}
	homeReplay, replay, err := restarted.CreateHome(owner, "home-create-postgres-000001", homeRequest)
	if err != nil || !replay || homeReplay.ID != home.ID {
		t.Fatalf("home replay=%#v replay=%t err=%v", homeReplay, replay, err)
	}
	home, replay, err = restarted.PublishHome(owner, "home-publish-postgres-0001", home.ID, home.Revision)
	if err != nil || replay || home.Status != "ACTIVE" {
		t.Fatalf("publish=%#v replay=%t err=%v", home, replay, err)
	}
	homes, err := restarted.SearchHomes(buyer, HomeSearch{Query: "villa", Locality: "Chennai"})
	if err != nil || len(homes) != 1 {
		t.Fatalf("homes=%#v err=%v", homes, err)
	}
	inquiry, replay, err := restarted.Inquire(buyer, "home-inquiry-postgres-0001", home.ID, "Please share ownership details")
	if err != nil || replay || inquiry.Status != "OPEN" {
		t.Fatalf("inquiry=%#v replay=%t err=%v", inquiry, replay, err)
	}
	visit, replay, err := restarted.ScheduleVisit(buyer, "home-visit-postgres-000001", home.ID, now.Add(2*time.Hour))
	if err != nil || replay || visit.Status != "REQUESTED" {
		t.Fatalf("visit=%#v replay=%t err=%v", visit, replay, err)
	}
	home, replay, err = restarted.UpgradeHome(owner, "home-upgrade-postgres-0001", home.ID, "PREMIUM")
	if err != nil || replay || home.Plan != "PREMIUM" || home.FeaturedUntil == nil {
		t.Fatalf("upgrade=%#v replay=%t err=%v", home, replay, err)
	}

	classifiedRequest := ClassifiedRequest{Category: "FURNITURE", Title: "Solid wood dining table", Description: "Well maintained table for six people", Price: Money{AmountMinor: 500000, Currency: "INR"}, Locality: "Chennai", MediaAssetIDs: []string{localMedia}, Contact: "+919999999999", WhatsAppEnabled: true}
	classified, replay, err := restarted.CreateClassified(owner, "classified-create-postgres-01", classifiedRequest)
	if err != nil || replay || classified.Status != "PUBLISHED" || classified.ContactRevealed != "" {
		t.Fatalf("classified=%#v replay=%t err=%v", classified, replay, err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT encrypted_contact FROM local_verticals.classified_listings WHERE id=$1`, classified.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(classifiedRequest.Contact)) {
		t.Fatal("classified contact stored as plaintext")
	}
	revealed, err := restarted.RevealContact(buyer, classified.ID, ContactRequest{Channel: "WHATSAPP", Consent: true})
	if err != nil || revealed.ContactRevealed != classifiedRequest.Contact {
		t.Fatalf("revealed=%#v err=%v", revealed, err)
	}
	reported, replay, err := restarted.ReportClassified(buyer, "classified-report-postgres-01", classified.ID, ReportRequest{Reason: "SCAM", Details: "The listing ownership could not be verified"})
	if err != nil || replay || reported.ReportCount != 1 || reported.Status != "PENDING_REVIEW" {
		t.Fatalf("reported=%#v replay=%t err=%v", reported, replay, err)
	}
	wrongTenant := Actor{TenantID: localTenantTwo, Country: "IN", Subject: localBuyer, Roles: []string{"CUSTOMER"}}
	if _, err := restarted.Home(wrongTenant, home.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant home err=%v", err)
	}
	var homeRows, classifiedRows, inquiryRows, visitRows, reportRows, replayRows int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM local_verticals.home_listings),(SELECT count(*) FROM local_verticals.classified_listings),(SELECT count(*) FROM local_verticals.home_inquiries),(SELECT count(*) FROM local_verticals.home_visits),(SELECT count(*) FROM local_verticals.reports),(SELECT count(*) FROM local_verticals.idempotency_records)`).Scan(&homeRows, &classifiedRows, &inquiryRows, &visitRows, &reportRows, &replayRows); err != nil {
		t.Fatal(err)
	}
	if homeRows != 1 || classifiedRows != 1 || inquiryRows != 1 || visitRows != 1 || reportRows != 1 || replayRows != 7 {
		t.Fatalf("rows homes=%d classifieds=%d inquiries=%d visits=%d reports=%d replays=%d", homeRows, classifiedRows, inquiryRows, visitRows, reportRows, replayRows)
	}
}
