package configcms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/platformlocale"
)

// Public tenant routing is deployment configuration. Anonymous callers cannot
// choose a tenant or turn signed-in pages into public startup content.
type PublicStartupConfig struct {
	TenantID  string
	Countries []string
}

func (config PublicStartupConfig) Valid() bool {
	if config.TenantID == "" {
		return len(config.Countries) == 0
	}
	if !safeID(config.TenantID) || len(config.Countries) < 1 || len(config.Countries) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, country := range config.Countries {
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' || seen[country] {
			return false
		}
		seen[country] = true
	}
	return true
}

type StartupSplash struct {
	Version string `json:"version"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

type StartupDocument struct {
	Revision    int64          `json:"revision"`
	PublishedAt time.Time      `json:"published_at"`
	Country     string         `json:"country"`
	Role        string         `json:"role"`
	Locale      string         `json:"locale"`
	Splash      *StartupSplash `json:"splash"`
}

func validStartupRole(role string) bool {
	return role == "customer" || role == "vendor" || role == "rider"
}

// Reserved splash drafts use the existing text-only ONBOARDING block, with
// exactly one public panel. Arbitrary content, targeting and redirects cannot
// enter the anonymous response. Normal drafts/four-eyes publication still own
// every revision; this endpoint never publishes or creates a default page.
func validStartupPage(page Page) bool {
	if !strings.HasPrefix(page.ID, "mobile-splash-") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(page.ID, "mobile-splash-"), "-")
	if len(parts) != 2 || !validStartupRole(parts[0]) || !platformlocale.IsSupported(parts[1]) || page.Route != "/app/splash/"+parts[0]+"/"+parts[1] || len(page.Audience) != 1 || page.Audience[0] != "PUBLIC" || len(page.AudienceGroups) != 0 || len(page.Blocks) != 1 {
		return false
	}
	// Reuse the established bounded education schema without broadening its
	// role-scoped publication contract.
	copy := page
	copy.ID = "mobile-onboarding-" + parts[0] + "-" + parts[1]
	copy.Route = "/app/onboarding/" + parts[0] + "/" + parts[1]
	copy.Audience = []string{strings.ToUpper(parts[0])}
	return validEducationPage(copy)
}

func (service *Service) PublicStartup(ctx context.Context, tenant, country, role, requestedLocale string) (StartupDocument, error) {
	if !safeID(tenant) || len(country) != 2 || !validStartupRole(role) || len(requestedLocale) > 32 {
		return StartupDocument{}, ErrInvalidSnapshot
	}
	snapshot, err := service.publicRepository.Get(ctx, tenant, country)
	if err != nil {
		return StartupDocument{}, err
	}
	if snapshot.TenantID != tenant || snapshot.Country != country || snapshot.Revision < 1 || snapshot.PublishedAt.IsZero() {
		return StartupDocument{}, ErrInvalidSnapshot
	}
	locale := platformlocale.Resolve(requestedLocale, snapshot.DefaultLocale, snapshot.SupportedLocales)
	result := StartupDocument{Revision: snapshot.Revision, PublishedAt: snapshot.PublishedAt, Country: country, Role: role, Locale: locale}
	for _, candidate := range []string{locale, "en"} {
		for _, page := range snapshot.Pages {
			if page.ID != "mobile-splash-"+role+"-"+candidate || !page.Enabled {
				continue
			}
			if !validStartupPage(page) {
				return StartupDocument{}, ErrInvalidSnapshot
			}
			block := page.Blocks[0]
			if !block.Enabled {
				continue
			}
			result.Locale = candidate
			result.Splash = &StartupSplash{Version: block.Content["version"].(string), Title: block.Content["title"].(string), Message: block.Content["message"].(string)}
			return result, nil
		}
	}
	return result, nil
}

func (handler *Handler) publicStartup(writer http.ResponseWriter, request *http.Request) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || !validStartupQuery(query) {
		writeProblem(writer, request, 422, "STARTUP_REQUEST_INVALID", "The startup request is invalid.")
		return
	}
	country := query.Get("country")
	if handler.startup.TenantID == "" || !containsString(handler.startup.Countries, country) {
		writeProblem(writer, request, 404, "STARTUP_NOT_FOUND", "Startup content is unavailable for this region.")
		return
	}
	result, err := handler.service.PublicStartup(request.Context(), handler.startup.TenantID, country, query.Get("role"), query.Get("locale"))
	if err != nil {
		status, code := http.StatusServiceUnavailable, "STARTUP_UNAVAILABLE"
		if errors.Is(err, ErrNotFound) {
			status, code = 404, "STARTUP_NOT_FOUND"
		}
		writeProblem(writer, request, status, code, "Startup content could not be loaded.")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

func validStartupQuery(query url.Values) bool {
	if len(query) < 2 || len(query) > 3 || len(query["country"]) != 1 || len(query["role"]) != 1 || !validStartupRole(query.Get("role")) || len(query.Get("locale")) > 32 {
		return false
	}
	for key, values := range query {
		if (key != "country" && key != "role" && key != "locale") || len(values) != 1 {
			return false
		}
	}
	return true
}
