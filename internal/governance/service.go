package governance

import (
	"regexp"
	"strings"
	"time"
)

type Service struct {
	config Configuration
	clock  func() time.Time
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$`)

func NewService(config Configuration, clock func() time.Time) (*Service, error) {
	if clock == nil || !safeID.MatchString(config.TenantID) || len(config.Countries) == 0 {
		return nil, ErrInvalidRequest
	}
	return &Service{config: config, clock: clock}, nil
}

func (service *Service) Dashboard(actor Actor) (Dashboard, error) {
	if !validActor(actor) || !actor.MFAVerified {
		return Dashboard{}, ErrMFARequired
	}
	if !allowed(actor) {
		return Dashboard{}, ErrForbidden
	}
	countries := []CountryControl{}
	for _, country := range service.config.Countries {
		if country.Country == actor.Country || hasRole(actor, "SUPER_ADMIN") {
			country.Locales = append([]string(nil), country.Locales...)
			country.FeatureFlags = cloneFlags(country.FeatureFlags)
			countries = append(countries, country)
		}
	}
	reports := append([]ReportCard(nil), service.config.Reports...)
	for index := range reports {
		reports[index].Masked = true
		reports[index].ExportPolicy = "MFA_AND_AUDIT_REQUIRED"
	}
	maps := append([]MapCell(nil), service.config.MapCells...)
	for index := range maps {
		maps[index].Precision = "aggregate_region"
	}
	leaders := append([]LeaderboardEntry(nil), service.config.Leaderboard...)
	for index := range leaders {
		leaders[index].PIIMasked = true
	}
	insights := append([]Insight(nil), service.config.Insights...)
	for index := range insights {
		insights[index].Evidence = append([]string(nil), insights[index].Evidence...)
	}
	return Dashboard{GeneratedAt: service.clock().UTC(), Reports: reports, MapCells: maps, Leaderboard: leaders, Insights: insights, Countries: countries, PrivacyMode: "aggregate_and_masked"}, nil
}

func validActor(actor Actor) bool {
	return safeID.MatchString(actor.TenantID) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(actor.Country) && safeID.MatchString(actor.Subject) && len(actor.Roles) > 0
}
func allowed(actor Actor) bool {
	return hasRole(actor, "SUPER_ADMIN") || hasRole(actor, "COUNTRY_ADMIN") || hasRole(actor, "AUDITOR") || hasRole(actor, "OPS_ADMIN") || hasRole(actor, "FINANCE") || hasRole(actor, "CONTENT_ADMIN") || hasRole(actor, "FRANCHISE_ADMIN") || hasRole(actor, "EMERGENCY_ADMIN")
}
func hasRole(actor Actor, expected string) bool {
	for _, role := range actor.Roles {
		if strings.EqualFold(strings.TrimSpace(role), expected) {
			return true
		}
	}
	return false
}
func cloneFlags(value map[string]bool) map[string]bool {
	result := map[string]bool{}
	for key, item := range value {
		result[key] = item
	}
	return result
}
