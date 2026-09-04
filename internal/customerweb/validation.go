package customerweb

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

var countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

func validSession(session Session) bool {
	if !canonicalUUID(session.ID) || strings.TrimSpace(session.PlatformSession) == "" || len(session.PlatformSession) > 128 ||
		strings.TrimSpace(session.TenantID) == "" || len(session.TenantID) > 128 || !validCountry(session.Country) ||
		strings.TrimSpace(session.DisplayName) == "" || len(session.DisplayName) > 128 || len(session.Roles) == 0 || len(session.Roles) > 8 ||
		len(session.AccessToken) < 8 || len(session.AccessToken) > 16384 || len(session.CSRFToken) < 32 || len(session.CSRFToken) > 256 ||
		session.AccessExpiresAt.IsZero() || session.ExpiresAt.IsZero() || session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() ||
		session.ExpiresAt.Before(session.UpdatedAt) || session.AccessExpiresAt.Before(session.UpdatedAt) {
		return false
	}
	if session.Guest {
		return session.IdentityID == "" && session.RefreshToken == "" && session.RefreshExpiresAt.IsZero() &&
			len(session.Roles) == 1 && session.Roles[0] == identity.RoleGuest
	}
	if strings.TrimSpace(session.IdentityID) == "" || len(session.IdentityID) > 128 || len(session.RefreshToken) < 8 ||
		len(session.RefreshToken) > 256 || session.RefreshExpiresAt.IsZero() || session.RefreshExpiresAt.Before(session.UpdatedAt) {
		return false
	}
	for _, role := range session.Roles {
		if role != identity.RoleCustomer && role != identity.RoleVendor && role != identity.RoleRider {
			return false
		}
	}
	return true
}

func validCountry(value string) bool { return countryPattern.MatchString(value) }

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func minTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		if result.IsZero() || value.Before(result) {
			result = value
		}
	}
	return result
}
