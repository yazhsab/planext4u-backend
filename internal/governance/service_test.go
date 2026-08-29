package governance

import (
	"errors"
	"testing"
	"time"
)

func TestBEP5010And5011ScopedGovernanceReportsMapsLeaderboardsAndIntelligence(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service, err := NewService(testConfiguration(now), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	withoutMFA := Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: "ops-admin-001", Roles: []string{"OPS_ADMIN"}}
	if _, err = service.Dashboard(withoutMFA); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("MFA error=%v", err)
	}
	withoutMFA.MFAVerified = true
	value, err := service.Dashboard(withoutMFA)
	if err != nil || value.PrivacyMode != "aggregate_and_masked" || len(value.Reports) < 10 || !value.Reports[0].Masked || value.MapCells[0].Precision != "aggregate_region" || !value.Leaderboard[0].PIIMasked {
		t.Fatalf("dashboard=%#v err=%v", value, err)
	}
	if len(value.Countries) != 1 || value.Countries[0].Country != "IN" {
		t.Fatalf("country scope leaked=%#v", value.Countries)
	}
}

func testConfiguration(now time.Time) Configuration {
	return Configuration{TenantID: "tenant-synthetic-001", Countries: []CountryControl{{Country: "IN", Currency: "INR", Locales: []string{"en", "ta"}, FeatureFlags: map[string]bool{"socio": true, "homes": true, "classifieds": true, "emergency": true}, PolicyVersion: "policy-IN-2026.08"}, {Country: "NG", Currency: "NGN", Locales: []string{"en"}, FeatureFlags: map[string]bool{"socio": true}, PolicyVersion: "policy-NG-2026.08"}}, Reports: []ReportCard{{ID: "report-orders", Title: "Orders", Domain: "commerce", Metric: "completed", Value: 1234, Unit: "count", Freshness: now}, {ID: "report-revenue", Title: "Revenue", Domain: "finance", Metric: "net", Value: 884400, Unit: "minor_currency", Freshness: now}, {ID: "report-wallet", Title: "Wallet", Domain: "wallet", Metric: "liability", Value: 45000, Unit: "points", Freshness: now}, {ID: "report-vendors", Title: "Vendors", Domain: "supply", Metric: "approved", Value: 41, Unit: "count", Freshness: now}, {ID: "report-riders", Title: "Riders", Domain: "fulfillment", Metric: "online", Value: 17, Unit: "count", Freshness: now}, {ID: "report-food", Title: "Food", Domain: "food", Metric: "delivered", Value: 219, Unit: "count", Freshness: now}, {ID: "report-socio", Title: "Socio", Domain: "social", Metric: "active", Value: 5600, Unit: "count", Freshness: now}, {ID: "report-homes", Title: "Homes", Domain: "homes", Metric: "active", Value: 98, Unit: "count", Freshness: now}, {ID: "report-classifieds", Title: "Classifieds", Domain: "classifieds", Metric: "active", Value: 340, Unit: "count", Freshness: now}, {ID: "report-emergency", Title: "Emergency SLA", Domain: "emergency", Metric: "within_sla", Value: 99, Unit: "percent", Freshness: now}}, MapCells: []MapCell{{RegionCode: "IN-TN-CHN", Label: "Chennai", Count: 281, Intensity: 82}}, Leaderboard: []LeaderboardEntry{{Rank: 1, Label: "Loc***ide", Score: 980, Badge: "Community guide"}}, Insights: []Insight{{ID: "insight-001", Title: "Evening demand", Summary: "Local-service demand peaks between 18:00 and 20:00.", Confidence: "HIGH", Evidence: []string{"30-day bookings", "minimum cohort 100"}, GeneratedAt: now}}}
}
