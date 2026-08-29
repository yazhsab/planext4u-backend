package localverticals

import (
	"errors"
	"testing"
	"time"
)

func TestBEP5007HomesKYCDiscoveryEstimateInquiryVisitAndPlans(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := localTestService(t, func() time.Time { return now })
	owner, buyer := localActor("customer-owner-001"), localActor("customer-buyer-001")
	created, replay, err := service.CreateHome(owner, "home-create-key-001", HomeListingRequest{Title: "Sunny three bedroom villa", PropertyType: "VILLA", Purpose: "SALE", Locality: "Adyar Chennai", Latitude: 13.0012, Longitude: 80.2565, AreaSqFt: 1800, Bedrooms: 3, Price: Money{AmountMinor: 2500000000, Currency: "INR"}, Amenities: []string{"parking", "garden"}, MediaAssetIDs: []string{"asset-home-001"}})
	if err != nil || replay || created.Status != "DRAFT" || !created.KYCVerified || created.Estimate.Version != "homes-avm-2026.08" {
		t.Fatalf("created=%#v replay=%v err=%v", created, replay, err)
	}
	published, _, err := service.PublishHome(owner, "home-publish-key-001", created.ID, created.Revision)
	if err != nil || published.Status != "ACTIVE" {
		t.Fatalf("published=%#v err=%v", published, err)
	}
	items, err := service.SearchHomes(buyer, HomeSearch{Locality: "adyar", PropertyType: "villa"})
	if err != nil || len(items) != 1 {
		t.Fatalf("homes=%#v err=%v", items, err)
	}
	if _, _, err = service.Inquire(buyer, "home-inquiry-key-001", created.ID, "Please share ownership records"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ScheduleVisit(buyer, "home-visit-key-001", created.ID, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	upgraded, _, err := service.UpgradeHome(owner, "home-upgrade-key-001", created.ID, "PREMIUM")
	if err != nil || upgraded.FeaturedUntil == nil {
		t.Fatalf("upgraded=%#v err=%v", upgraded, err)
	}
	unverified := localActor("customer-buyer-001")
	draft, _, err := service.CreateHome(unverified, "home-create-key-002", HomeListingRequest{Title: "Compact city apartment", PropertyType: "APARTMENT", Purpose: "RENT", Locality: "Mylapore", Latitude: 13.03, Longitude: 80.27, AreaSqFt: 700, Bedrooms: 1, Price: Money{AmountMinor: 2500000, Currency: "INR"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.PublishHome(unverified, "home-publish-key-002", draft.ID, draft.Revision); !errors.Is(err, ErrForbidden) {
		t.Fatalf("KYC error=%v", err)
	}
}

func TestBEP5008ClassifiedConsentExpiryRepostReportsAndPlans(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	service := localTestService(t, clock)
	seller, buyer := localActor("customer-owner-001"), localActor("customer-buyer-001")
	created, replay, err := service.CreateClassified(seller, "classified-create-001", ClassifiedRequest{Category: "electronics", Title: "Well kept laptop", Description: "Two years old and fully working", Price: Money{AmountMinor: 4500000, Currency: "INR"}, Locality: "Adyar", Contact: "919876543210", WhatsAppEnabled: true, MediaAssetIDs: []string{"asset-classified-001"}})
	if err != nil || replay || created.ContactMasked == "919876543210" {
		t.Fatalf("created=%#v replay=%v err=%v", created, replay, err)
	}
	if _, err = service.RevealContact(buyer, created.ID, ContactRequest{Channel: "WHATSAPP", Consent: false}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("consent error=%v", err)
	}
	revealed, err := service.RevealContact(buyer, created.ID, ContactRequest{Channel: "WHATSAPP", Consent: true})
	if err != nil || revealed.ContactRevealed != "919876543210" {
		t.Fatalf("revealed=%#v err=%v", revealed, err)
	}
	for index := 0; index < 3; index++ {
		actor := localActor("customer-reporter-00" + string(rune('1'+index)))
		if _, _, err = service.ReportClassified(actor, "classified-report-00"+string(rune('1'+index)), created.ID, ReportRequest{Reason: "SCAM", Details: "Suspicious payment request"}); err != nil {
			t.Fatal(err)
		}
	}
	value, err := service.Classified(seller, created.ID)
	if err != nil || value.Status != "PENDING_REVIEW" {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	expiring, _, err := service.CreateClassified(seller, "classified-create-002", ClassifiedRequest{Category: "furniture", Title: "Solid wood table", Description: "Six seater dining table", Price: Money{AmountMinor: 1500000, Currency: "INR"}, Locality: "Adyar", Contact: "919876543210"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, err = service.BrowseClassifieds(buyer, "", "", ""); err != nil {
		t.Fatal(err)
	}
	reposted, replay, err := service.RepostClassified(seller, "classified-repost-001", expiring.ID)
	if err != nil || replay || reposted.Status != "PUBLISHED" {
		t.Fatalf("reposted=%#v replay=%v err=%v", reposted, replay, err)
	}
	featured, _, err := service.UpgradeClassified(seller, "classified-upgrade-001", expiring.ID, "FEATURED")
	if err != nil || featured.FeaturedUntil == nil {
		t.Fatalf("featured=%#v err=%v", featured, err)
	}
}

func localTestService(t *testing.T, clock func() time.Time) *Service {
	t.Helper()
	service, err := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN", Currency: "INR", KYCVerifiedOwners: []string{"customer-owner-001"}, ReviewTerms: []string{"prohibited"}}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func localActor(subject string) Actor {
	return Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: subject, Roles: []string{"CUSTOMER"}}
}
