package configcms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/platformlocale"
)

func splashFixture(role, locale, title string) Page {
	return Page{ID: "mobile-splash-" + role + "-" + locale, Route: "/app/splash/" + role + "/" + locale, TitleKey: "startup.title", Audience: []string{"PUBLIC"}, Enabled: true, Blocks: []PageBlock{{ID: "splash", Kind: "ONBOARDING", Enabled: true, Content: map[string]any{"version": "1", "title": title, "message": "Published startup message"}}}}
}

func TestPublicStartupProjectsOnlyInstalledTenantPublicText(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	snapshot := validSnapshot(now)
	snapshot.Pages = []Page{splashFixture("customer", "en", "Public English"), splashFixture("customer", "ta", "வணக்கம்"), splashFixture("rider", "en", "Rider welcome"), {ID: "private-page", Route: "/private", TitleKey: "private.title", Audience: []string{"CUSTOMER"}, Enabled: true, Blocks: []PageBlock{{ID: "private", Kind: "BANNER", Enabled: true, Content: map[string]any{"title": "Private account content"}}}}}
	repository, err := NewMemoryRepository(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(repository, func() time.Time { return now })
	config := PublicStartupConfig{TenantID: snapshot.TenantID, Countries: []string{"IN"}}
	handler, err := NewHandlerWithPublicStartup(service, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	// Constructor ownership prevents later mutation of the deployment allowlist.
	config.Countries[0] = "NG"
	request := httptest.NewRequest(http.MethodGet, "/v1/startup?country=IN&role=customer&locale=ta", nil)
	request.Header.Set("X-Planext4u-Tenant", "foreign-tenant")
	request.Header.Set("X-Planext4u-Country", "NG")
	request.Header.Set("X-Planext4u-Roles", "SUPER_ADMIN")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result StartupDocument
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || result.Splash == nil || result.Splash.Title != "வணக்கம்" || result.Locale != "ta" || result.Country != "IN" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("startup=%d %s err=%v", response.Code, response.Body.String(), err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(response.Body.Bytes(), &fields)
	if len(fields) != 6 {
		t.Fatalf("startup projection broadened: %s", response.Body.String())
	}
	for _, private := range []string{"tenant_id", "Private account content", "consent_policies", "flags", "home_sections", "audience_groups", "pages"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("private/bootstrap data entered startup: %s", private)
		}
	}
	for _, test := range []struct{ path, title, locale string }{
		{"/v1/startup?country=IN&role=customer&locale=hi", "Public English", "en"},
		{"/v1/startup?country=IN&role=rider&locale=ta", "Rider welcome", "en"},
	} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest("GET", test.path, nil))
		var got StartupDocument
		_ = json.Unmarshal(r.Body.Bytes(), &got)
		if r.Code != 200 || got.Splash == nil || got.Splash.Title != test.title || got.Locale != test.locale {
			t.Fatalf("wrong language/role fallback: %d %s", r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"?country=IN&role=customer&tenant_id=other", "?country=IN&role=customer&role=rider", "?country=IN&role=admin", "?country=IN&role=customer&locale=ta&locale=en", "?country=IN", "?country=IN&role=customer&subject_id=anyone", "?country=IN;bad&role=customer"} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest("GET", "/v1/startup"+path, nil))
		if r.Code != 422 {
			t.Fatalf("invalid/authority query accepted: %s %d", path, r.Code)
		}
	}
	r := httptest.NewRecorder()
	handler.ServeHTTP(r, httptest.NewRequest("GET", "/v1/startup?country=NG&role=customer", nil))
	if r.Code != 404 {
		t.Fatalf("unconfigured country exposed: %d", r.Code)
	}
}

func TestPublicStartupMissingDisabledAndUnconfiguredNeverCreatesContent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	snapshot := validSnapshot(now)
	disabled := splashFixture("customer", "en", "Disabled startup")
	disabled.Enabled = false
	snapshot.Pages = []Page{disabled}
	repository, _ := NewMemoryRepository(snapshot)
	service, _ := NewService(repository, func() time.Time { return now })
	value, err := service.PublicStartup(context.Background(), snapshot.TenantID, "IN", "customer", "en")
	if err != nil || value.Splash != nil {
		t.Fatalf("disabled splash exposed: %+v %v", value, err)
	}
	handler, _ := NewHandler(service)
	r := httptest.NewRecorder()
	handler.ServeHTTP(r, httptest.NewRequest("GET", "/v1/startup?country=IN&role=customer", nil))
	if r.Code != 404 {
		t.Fatalf("unconfigured tenant routing enabled: %d", r.Code)
	}
}

func TestReservedSplashDraftsRejectPrivateTargetingAndUnboundedContent(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"customer", "vendor", "rider"} {
		for _, locale := range platformlocale.All() {
			if err := validatePages([]Page{splashFixture(role, locale, "Welcome")}); err != nil {
				t.Fatal(role, locale, err)
			}
		}
	}
	for _, mutate := range []func(*Page){
		func(p *Page) { p.Audience = []string{"CUSTOMER"} },
		func(p *Page) { p.Audience = []string{"PUBLIC", "CUSTOMER"} },
		func(p *Page) { p.AudienceGroups = []string{"c0685b62-abdb-4696-aed0-6977d2ac8dc5"} },
		func(p *Page) { p.Route = "/app/onboarding/customer/en" },
		func(p *Page) { p.Blocks = append(p.Blocks, p.Blocks[0]) },
		func(p *Page) { p.Blocks[0].Content["title"] = strings.Repeat("த", 121) },
		func(p *Page) { p.Blocks[0].Content["message"] = strings.Repeat("x", 1001) },
		func(p *Page) { p.Blocks[0].Content["redirect"] = "https://untrusted.invalid" },
		func(p *Page) { p.Blocks[0].Kind = "HTML" },
		func(p *Page) { p.Blocks[0].Content["version"] = "" },
	} {
		page := splashFixture("customer", "en", "Welcome")
		mutate(&page)
		if err := validatePages([]Page{page}); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("invalid splash entered normal CMS draft: %+v %v", page, err)
		}
	}
}

type startupUnavailableRepository struct{}

func (startupUnavailableRepository) Get(context.Context, string, string) (Snapshot, error) {
	return Snapshot{}, errors.New("private database connection details")
}

func TestPublicStartupProviderFailureIsBoundedAndDoesNotLeak(t *testing.T) {
	service, _ := NewService(startupUnavailableRepository{}, time.Now)
	handler, _ := NewHandlerWithPublicStartup(service, nil, PublicStartupConfig{TenantID: "tenant-synthetic", Countries: []string{"IN"}})
	r := httptest.NewRecorder()
	handler.ServeHTTP(r, httptest.NewRequest("GET", "/v1/startup?country=IN&role=customer", nil))
	if r.Code != 503 || strings.Contains(r.Body.String(), "private database") || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe startup failure: %d %s", r.Code, r.Body.String())
	}
}

type startupSnapshotRepository struct{ snapshot Snapshot }

func (repository startupSnapshotRepository) Get(context.Context, string, string) (Snapshot, error) {
	return repository.snapshot, nil
}

func TestPublicStartupRejectsCorruptPublishedScopeAndPrivateReservedPages(t *testing.T) {
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.TenantID = "foreign-tenant" },
		func(s *Snapshot) { s.Country = "NG" },
		func(s *Snapshot) { s.Pages[0].Audience = []string{"CUSTOMER"} },
		func(s *Snapshot) { s.Pages[0].AudienceGroups = []string{"c0685b62-abdb-4696-aed0-6977d2ac8dc5"} },
	} {
		snapshot := validSnapshot(time.Now().UTC())
		snapshot.Pages = []Page{splashFixture("customer", "en", "Private or foreign content")}
		mutate(&snapshot)
		service, _ := NewService(startupSnapshotRepository{snapshot}, time.Now)
		handler, _ := NewHandlerWithPublicStartup(service, nil, PublicStartupConfig{TenantID: "tenant-synthetic", Countries: []string{"IN"}})
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest("GET", "/v1/startup?country=IN&role=customer", nil))
		if r.Code != 503 || strings.Contains(r.Body.String(), "Private or foreign content") {
			t.Fatalf("corrupt owner data entered public startup: %d %s", r.Code, r.Body.String())
		}
	}
}

func TestPublicStartupReadsCurrentPublicationOutsideBootstrapCache(t *testing.T) {
	now := time.Now().UTC()
	snapshot := validSnapshot(now)
	snapshot.Pages = []Page{splashFixture("customer", "en", "Original public welcome")}
	repository, _ := NewMemoryRepository(snapshot)
	cache, _ := NewCachedRepository(repository, time.Hour, func() time.Time { return now })
	service, _ := NewServiceWithPublicRepository(cache, repository, func() time.Time { return now }, "local-development")
	if _, err := service.Bootstrap(context.Background(), snapshot.TenantID, "IN", PlatformAndroid, "1.2.0", "", "en"); err != nil {
		t.Fatal(err)
	}
	snapshot.Revision = 2
	snapshot.Pages[0].Enabled = false
	if err := repository.Publish(context.Background(), snapshot, 1); err != nil {
		t.Fatal(err)
	}
	current, err := service.PublicStartup(context.Background(), snapshot.TenantID, "IN", "customer", "en")
	if err != nil || current.Revision != 2 || current.Splash != nil {
		t.Fatalf("startup reused removed cached content: %+v %v", current, err)
	}
}
