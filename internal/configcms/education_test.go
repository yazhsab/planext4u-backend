package configcms

import (
	"strings"
	"testing"

	"github.com/yazhsab/planext4u-backend/internal/platformlocale"
)

func TestMobileEducationDraftsAreRoleLocaleAndTextBounded(t *testing.T) {
	page := Page{ID: "mobile-onboarding-customer-ta", Route: "/app/onboarding/customer/ta", Audience: []string{"CUSTOMER"}, Enabled: true, TitleKey: "onboarding.customer.ta", Blocks: []PageBlock{{ID: "welcome", Kind: "ONBOARDING", Enabled: true, Content: map[string]any{"version": "1", "title": "வணக்கம்", "message": "உங்கள் அனுமதிகளைத் தேர்வு செய்யுங்கள்"}}}}
	for _, role := range []string{"customer", "vendor", "rider"} {
		for _, locale := range platformlocale.All() {
			copy := cloneSnapshot(Snapshot{Pages: []Page{page}}).Pages[0]
			copy.ID = "mobile-onboarding-" + role + "-" + locale
			copy.Route = "/app/onboarding/" + role + "/" + locale
			copy.Audience = []string{strings.ToUpper(role)}
			if !validEducationPage(copy) {
				t.Fatal(role, locale)
			}
		}
	}
	for _, mutate := range []func(*Page){
		func(p *Page) { p.Audience = []string{"PUBLIC"} },
		func(p *Page) { p.Route = "/different" },
		func(p *Page) { p.ID = "mobile-onboarding-admin-en" },
		func(p *Page) { p.Blocks = nil },
		func(p *Page) { p.Blocks[0].Kind = "HTML" },
		func(p *Page) { p.Blocks[0].Enabled = false },
		func(p *Page) { p.Blocks[0].Content["version"] = "invalid edition" },
		func(p *Page) { p.Blocks[0].Content["title"] = strings.Repeat("த", 121) },
		func(p *Page) { p.Blocks[0].Content["message"] = " " },
		func(p *Page) { p.Blocks[0].Content["unexpected"] = "payload" },
	} {
		copy := cloneSnapshot(Snapshot{Pages: []Page{page}}).Pages[0]
		mutate(&copy)
		if validEducationPage(copy) {
			t.Fatal("invalid reserved page accepted", copy)
		}
	}
}
