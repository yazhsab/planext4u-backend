package configcms

import (
	"strings"
	"unicode/utf8"

	"github.com/yazhsab/planext4u-backend/internal/platformlocale"
)

// Reserved mobile education pages use the normal revisioned CMS drafts and
// independent publication approvals, with a bounded declarative text schema.
func validEducationPage(page Page) bool {
	if !strings.HasPrefix(page.ID, "mobile-onboarding-") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(page.ID, "mobile-onboarding-"), "-")
	if len(parts) != 2 || (parts[0] != "customer" && parts[0] != "vendor" && parts[0] != "rider") || !platformlocale.IsSupported(parts[1]) || page.Route != "/app/onboarding/"+parts[0]+"/"+parts[1] || len(page.Audience) != 1 || page.Audience[0] != strings.ToUpper(parts[0]) || len(page.Blocks) < 1 || len(page.Blocks) > 8 {
		return false
	}
	version := ""
	enabled := 0
	for _, block := range page.Blocks {
		if block.Kind != "ONBOARDING" || len(block.Content) != 3 {
			return false
		}
		edition, ok := block.Content["version"].(string)
		if !ok || len(edition) < 1 || len(edition) > 32 || version != "" && version != edition {
			return false
		}
		for _, character := range edition {
			if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && !strings.ContainsRune("._-", character) {
				return false
			}
		}
		version = edition
		for key, limit := range map[string]int{"title": 120, "message": 1000} {
			text, ok := block.Content[key].(string)
			if !ok || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > limit {
				return false
			}
		}
		if block.Enabled {
			enabled++
		}
	}
	return !page.Enabled || enabled > 0
}
