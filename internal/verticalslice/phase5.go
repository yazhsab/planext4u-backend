package verticalslice

import (
	"net/http"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/emergency"
	"github.com/yazhsab/planext4u-backend/internal/governance"
	"github.com/yazhsab/planext4u-backend/internal/localverticals"
	"github.com/yazhsab/planext4u-backend/internal/social"
)

func phase5Handlers(clock func() time.Time) (http.Handler, http.Handler, http.Handler, http.Handler, *emergency.Service, error) {
	profiles := []social.Profile{
		{ID: syntheticSubject, Handle: "synthetic_customer", DisplayName: "Synthetic Customer"},
		{ID: "customer-public-synthetic-001", Handle: "local_guide", DisplayName: "Local Guide", Bio: "Trusted neighbourhood updates", Verified: true, FollowerCount: 128, FollowingCount: 24},
		{ID: "customer-private-synthetic-001", Handle: "private_neighbour", DisplayName: "Private Neighbour", Private: true},
	}
	service, err := social.NewService(social.Configuration{
		TenantID: syntheticTenant, Country: "IN", Profiles: profiles, RankingModel: "socio-feed-v1", ReviewTerms: []string{"manual-review", "unsafe-review"},
		Posts: []social.Post{{
			ID: "social-post-synthetic-001", Revision: 1, Author: profiles[1], Body: "Weekend community market in Chennai #local #chennai",
			MediaAssetIDs: []string{"asset-social-synthetic-001"}, Hashtags: []string{"chennai", "local"}, Status: social.PostPublished,
			RankingVersion: "socio-feed-v1", CreatedAt: clock().UTC().Add(-time.Hour), UpdatedAt: clock().UTC().Add(-time.Hour),
		}},
	}, clock)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	socialHandler, err := social.NewHandler(service)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	now := clock().UTC()
	localService, err := localverticals.NewService(localverticals.Configuration{
		TenantID: syntheticTenant, Country: "IN", Currency: "INR", KYCVerifiedOwners: []string{syntheticSubject}, ReviewTerms: []string{"prohibited", "unsafe-review"},
		Homes: []localverticals.HomeListing{{
			ID: "home-listing-synthetic-001", Revision: 1, OwnerID: "customer-public-synthetic-001", Title: "Sea-view family apartment", PropertyType: "APARTMENT", Purpose: "SALE", Locality: "Adyar, Chennai", Latitude: 13.0012, Longitude: 80.2565, AreaSqFt: 1450, Bedrooms: 3, Price: localverticals.Money{AmountMinor: 1850000000, Currency: "INR"}, Amenities: []string{"parking", "security", "lift"}, MediaAssetIDs: []string{"asset-home-synthetic-001"}, Status: "ACTIVE", KYCVerified: true, Plan: "FEATURED", CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-time.Hour),
		}},
		SeedContacts: map[string]string{"classified-listing-synthetic-001": "919876543210"}, Classifieds: []localverticals.ClassifiedListing{{
			ID: "classified-listing-synthetic-001", Revision: 1, OwnerID: "customer-public-synthetic-001", Category: "ELECTRONICS", Title: "Well-kept laptop", Description: "Two years old, serviced and fully working.", Price: localverticals.Money{AmountMinor: 4500000, Currency: "INR"}, Locality: "Adyar, Chennai", MediaAssetIDs: []string{"asset-classified-synthetic-001"}, Status: "PUBLISHED", Plan: "STANDARD", ContactMasked: "********3210", WhatsAppEnabled: true, ExpiresAt: now.Add(30 * 24 * time.Hour), CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-time.Hour),
		}},
	}, clock)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	localHandler, err := localverticals.NewHandler(localService)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	emergencyService, err := emergency.NewService(emergency.Configuration{TenantID: syntheticTenant, Country: "IN", AssignmentSLA: 5 * time.Minute, LocationMaxAge: 2 * time.Minute}, clock)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	emergencyHandler, err := emergency.NewHandler(emergencyService)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	governanceService, err := governance.NewService(phase5GovernanceConfiguration(now), clock)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	governanceHandler, err := governance.NewHandler(governanceService)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return socialHandler, localHandler, emergencyHandler, governanceHandler, emergencyService, nil
}

func phase5Route(path string) string {
	exact := map[string]bool{
		"/v1/social/feed": true, "/v1/social/posts": true, "/v1/social/media": true, "/v1/social/ephemeral": true, "/v1/social/collections": true, "/v1/social/conversations": true, "/v1/social/presence": true,
		"/v1/moderation/reports": true, "/v1/moderation/retention/purge": true,
		"/v1/homes/listings": true, "/v1/classifieds/listings": true, "/v1/classifieds/retention/expire": true,
		"/v1/emergency/requests": true, "/v1/emergency/escalations/run": true, "/v1/emergency/reports/sla": true,
		"/v1/governance/dashboard": true, "/v1/governance/reports": true, "/v1/governance/maps": true, "/v1/governance/leaderboards": true, "/v1/governance/intelligence": true, "/v1/governance/countries": true,
	}
	if exact[path] {
		return path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" {
		return ""
	}
	if parts[1] == "social" {
		switch parts[2] {
		case "posts":
			if len(parts) == 4 {
				return "/v1/social/posts/{post_id}"
			}
			if len(parts) == 5 && (parts[4] == "like" || parts[4] == "save" || parts[4] == "comments" || parts[4] == "reports") {
				return "/v1/social/posts/{post_id}/" + parts[4]
			}
		case "profiles":
			if len(parts) == 4 {
				return "/v1/social/profiles/{profile_id}"
			}
			if len(parts) == 5 && (parts[4] == "follow" || parts[4] == "relationship") {
				return "/v1/social/profiles/{profile_id}/" + parts[4]
			}
		case "follow-requests":
			if len(parts) == 5 && parts[4] == "accept" {
				return "/v1/social/follow-requests/{follower_id}/accept"
			}
		case "media":
			if len(parts) == 5 && parts[4] == "appeals" {
				return "/v1/social/media/{media_id}/appeals"
			}
		case "ephemeral":
			if len(parts) == 5 && parts[4] == "highlight" {
				return "/v1/social/ephemeral/{content_id}/highlight"
			}
		case "collections":
			if len(parts) == 5 && parts[4] == "posts" {
				return "/v1/social/collections/{collection_id}/posts"
			}
		case "conversations":
			if len(parts) == 5 && (parts[4] == "accept" || parts[4] == "messages" || parts[4] == "calls") {
				return "/v1/social/conversations/{conversation_id}/" + parts[4]
			}
		case "calls":
			if len(parts) == 5 && parts[4] == "signals" {
				return "/v1/social/calls/{call_id}/signals"
			}
		case "presence":
			if len(parts) == 4 {
				return "/v1/social/presence/{profile_id}"
			}
		}
	}
	if parts[1] == "moderation" && len(parts) == 5 {
		if parts[2] == "reports" && parts[4] == "decision" {
			return "/v1/moderation/reports/{report_id}/decision"
		}
		if parts[2] == "media" && (parts[4] == "process" || parts[4] == "appeal-decision") {
			return "/v1/moderation/media/{media_id}/" + parts[4]
		}
	}
	if (parts[1] == "homes" || parts[1] == "classifieds") && len(parts) >= 4 && parts[2] == "listings" {
		base := "/v1/" + parts[1] + "/listings/{listing_id}"
		if len(parts) == 4 {
			return base
		}
		if len(parts) == 5 {
			return base + "/" + parts[4]
		}
	}
	if parts[1] == "emergency" && parts[2] == "requests" {
		if len(parts) == 4 {
			return "/v1/emergency/requests/{request_id}"
		}
		if len(parts) == 5 {
			return "/v1/emergency/requests/{request_id}/" + parts[4]
		}
	}
	return ""
}

func phase5GovernanceConfiguration(now time.Time) governance.Configuration {
	report := func(id, title, domain, metric, unit string, value int64) governance.ReportCard {
		return governance.ReportCard{ID: id, Title: title, Domain: domain, Metric: metric, Value: value, Unit: unit, Freshness: now}
	}
	return governance.Configuration{
		TenantID: syntheticTenant,
		Countries: []governance.CountryControl{
			{Country: "IN", Currency: "INR", Locales: []string{"en", "ta"}, FeatureFlags: map[string]bool{"socio": true, "homes": true, "classifieds": true, "emergency": true}, PolicyVersion: "policy-IN-2026.08"},
			{Country: "NG", Currency: "NGN", Locales: []string{"en"}, FeatureFlags: map[string]bool{"socio": true, "homes": true, "classifieds": true}, PolicyVersion: "policy-NG-2026.08"},
		},
		Reports:     []governance.ReportCard{report("report-orders", "Orders", "commerce", "completed", "count", 1234), report("report-revenue", "Revenue", "finance", "net", "minor_currency", 884400), report("report-wallet", "Wallet", "wallet", "liability", "points", 45000), report("report-vendors", "Vendors", "supply", "approved", "count", 41), report("report-riders", "Riders", "fulfillment", "online", "count", 17), report("report-food", "Food", "food", "delivered", "count", 219), report("report-socio", "Socio", "social", "active", "count", 5600), report("report-homes", "Homes", "homes", "active", "count", 98), report("report-classifieds", "Classifieds", "classifieds", "active", "count", 340), report("report-emergency", "Emergency SLA", "emergency", "within_sla", "percent", 99)},
		MapCells:    []governance.MapCell{{RegionCode: "IN-TN-CHN", Label: "Chennai", Count: 281, Intensity: 82}},
		Leaderboard: []governance.LeaderboardEntry{{Rank: 1, Label: "Loc***ide", Score: 980, Badge: "Community guide"}, {Rank: 2, Label: "Gre***art", Score: 920, Badge: "Trusted seller"}},
		Insights:    []governance.Insight{{ID: "insight-evening-demand", Title: "Evening demand", Summary: "Local-service demand peaks between 18:00 and 20:00.", Confidence: "HIGH", Evidence: []string{"30-day bookings", "minimum cohort 100"}, GeneratedAt: now}},
	}
}
