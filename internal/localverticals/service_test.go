package localverticals

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if err != nil || len(items.Items) != 1 {
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
	if _, err = service.BrowseClassifieds(buyer, ClassifiedSearch{}); err != nil {
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

func TestPublicBrowseIsGuestReadableCursorBoundedAndRedacted(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := localTestService(t, func() time.Time { return now })
	owner := localActor("customer-owner-001")
	for index, title := range []string{"Adyar family villa", "Chennai garden villa"} {
		value, _, err := service.CreateHome(owner, "home-page-create-00"+string(rune('1'+index)), HomeListingRequest{
			Title: title, PropertyType: "VILLA", Purpose: "SALE", Locality: "Chennai",
			Latitude: 13.0012, Longitude: 80.2565, AreaSqFt: 1800, Bedrooms: 3,
			Price: Money{AmountMinor: 2500000000, Currency: "INR"}, MediaAssetIDs: []string{"asset-home-private-001"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.PublishHome(owner, "home-page-publish-0"+string(rune('1'+index)), value.ID, value.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := service.CreateClassified(owner, "classified-page-create-001", ClassifiedRequest{
		Category: "electronics", Title: "Well kept laptop", Description: "Two years old and fully working",
		Price: Money{AmountMinor: 4500000, Currency: "INR"}, Locality: "Adyar", Contact: "919876543210",
		MediaAssetIDs: []string{"asset-classified-private-001"},
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/homes/listings?limit=1&locality=Chennai", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "guest-browser-001")
	request.Header.Set("X-Planext4u-Roles", "GUEST")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("homes status=%d body=%s", response.Code, response.Body.String())
	}
	var homePage map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &homePage); err != nil {
		t.Fatal(err)
	}
	homeItems := homePage["items"].([]any)
	if len(homeItems) != 1 || homePage["next_cursor"] == "" {
		t.Fatalf("home page=%#v", homePage)
	}
	home := homeItems[0].(map[string]any)
	for _, sensitive := range []string{"owner_id", "latitude", "longitude", "media_asset_ids"} {
		if _, exposed := home[sensitive]; exposed {
			t.Errorf("public home exposes %s", sensitive)
		}
	}
	if media := home["media"].([]any); len(media) != 0 {
		t.Fatalf("unresolved public home media=%#v", media)
	}
	if actions := home["allowed_actions"].([]any); len(actions) != 0 {
		t.Fatalf("guest home actions=%#v", actions)
	}
	cursor := homePage["next_cursor"].(string)
	secondRequest := httptest.NewRequest(http.MethodGet, "/v1/homes/listings?limit=1&locality=Chennai&cursor="+cursor, nil)
	secondRequest.Header = request.Header.Clone()
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, secondRequest)
	var secondPage map[string]any
	if secondResponse.Code != http.StatusOK || json.Unmarshal(secondResponse.Body.Bytes(), &secondPage) != nil {
		t.Fatalf("second homes status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	secondItems := secondPage["items"].([]any)
	if len(secondItems) != 1 || secondItems[0].(map[string]any)["id"] == home["id"] {
		t.Fatalf("second home page=%#v", secondPage)
	}

	classifiedRequest := httptest.NewRequest(http.MethodGet, "/v1/classifieds/listings?limit=1", nil)
	classifiedRequest.Header = request.Header.Clone()
	classifiedResponse := httptest.NewRecorder()
	handler.ServeHTTP(classifiedResponse, classifiedRequest)
	if classifiedResponse.Code != http.StatusOK {
		t.Fatalf("classifieds status=%d body=%s", classifiedResponse.Code, classifiedResponse.Body.String())
	}
	var classifiedPage map[string]any
	if err := json.Unmarshal(classifiedResponse.Body.Bytes(), &classifiedPage); err != nil {
		t.Fatal(err)
	}
	classified := classifiedPage["items"].([]any)[0].(map[string]any)
	for _, sensitive := range []string{"owner_id", "media_asset_ids", "contact_revealed", "report_count"} {
		if _, exposed := classified[sensitive]; exposed {
			t.Errorf("public classified exposes %s", sensitive)
		}
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
