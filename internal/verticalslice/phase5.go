package verticalslice

import (
	"net/http"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/social"
)

func phase5Handler(clock func() time.Time) (http.Handler, error) {
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
		return nil, err
	}
	return social.NewHandler(service)
}

func phase5Route(path string) string {
	exact := map[string]bool{
		"/v1/social/feed": true, "/v1/social/posts": true, "/v1/moderation/reports": true,
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
		}
	}
	if parts[1] == "moderation" && len(parts) == 5 && parts[2] == "reports" && parts[4] == "decision" {
		return "/v1/moderation/reports/{report_id}/decision"
	}
	return ""
}
