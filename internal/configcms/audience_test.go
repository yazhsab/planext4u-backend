package configcms

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublishedPageAudienceAppliesToDirectAndBootstrapReads(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	first, second := validSnapshot(now), validSnapshot(now)
	second.TenantID = "second-tenant"
	for _, snapshot := range []*Snapshot{&first, &second} {
		snapshot.Pages = []Page{}
		for _, audience := range []string{"PUBLIC", "CUSTOMER", "VENDOR", "RIDER"} {
			snapshot.Pages = append(snapshot.Pages, Page{ID: strings.ToLower(audience), Route: "/" + strings.ToLower(audience), TitleKey: "page.title", Audience: []string{audience}, Enabled: true, Blocks: []PageBlock{}})
		}
	}
	repository, err := NewMemoryRepository(first, second)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(repository, func() time.Time { return now })
	handler, _ := NewHandler(service)
	for _, roles := range []string{"", "GUEST", "CUSTOMER", "VENDOR", "RIDER", "CUSTOMER,VENDOR", "ADMIN", "VENDOR_EVIL"} {
		for _, tenant := range []string{first.TenantID, second.TenantID, "absent-tenant"} {
			for _, country := range []string{"IN", "GB"} {
				t.Run(roles+"/"+tenant+"/"+country, func(t *testing.T) {
					request := httptest.NewRequest("GET", "/v1/bootstrap?platform=ANDROID&app_version=1.2.0&audience=RIDER&roles=RIDER", nil)
					request.Header.Set("X-Planext4u-Tenant", tenant)
					request.Header.Set("X-Planext4u-Country", country)
					request.Header.Set("X-Planext4u-Roles", roles)
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					if tenant == "absent-tenant" || country != "IN" {
						if response.Code != 404 {
							t.Fatal(response.Code)
						}
						return
					}
					if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
						t.Fatal(response.Code, response.Header())
					}
					var result Bootstrap
					if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					for _, page := range result.Pages {
						if !pageVisible(page, roles) {
							t.Fatal("cross-audience page in bootstrap", page.ID)
						}
					}
					for _, page := range first.Pages {
						direct := httptest.NewRequest("GET", "/v1/pages/"+page.ID+"?audience=RIDER", nil)
						direct.Header = request.Header.Clone()
						out := httptest.NewRecorder()
						handler.ServeHTTP(out, direct)
						want := http.StatusNotFound
						if pageVisible(page, roles) {
							want = http.StatusOK
						}
						if out.Code != want {
							t.Fatalf("%s got %d want %d", page.ID, out.Code, want)
						}
					}
				})
			}
		}
	}
}
