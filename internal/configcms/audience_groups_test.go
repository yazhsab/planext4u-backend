package configcms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type audienceResolverFunc func(context.Context, string, string, string, []string) (map[string]bool, error)

func (f audienceResolverFunc) Memberships(ctx context.Context, t, c, s string, g []string) (map[string]bool, error) {
	return f(ctx, t, c, s, g)
}

func TestDynamicAudienceReadsUseCurrentMembershipAndTrustedIdentity(t *testing.T) {
	now := time.Now().UTC()
	snapshot := validSnapshot(now)
	snapshot.TenantID = uuid.NewString()
	subject, group := uuid.NewString(), uuid.NewString()
	snapshot.Pages = []Page{{ID: "public", Route: "/public", TitleKey: "public.title", Audience: []string{"PUBLIC"}, Enabled: true, Blocks: []PageBlock{}}, {ID: "restricted", Route: "/restricted", TitleKey: "restricted.message", Audience: []string{"CUSTOMER"}, AudienceGroups: []string{group}, Enabled: true, Blocks: []PageBlock{}}}
	repository, err := NewMemoryRepository(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := NewCachedRepository(repository, 5*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(cached, func() time.Time { return now })
	member, unavailable, calls := true, false, 0
	resolver := audienceResolverFunc(func(_ context.Context, tenant, country, identity string, groups []string) (map[string]bool, error) {
		calls++
		if tenant != snapshot.TenantID || country != "IN" || identity != subject || len(groups) != 1 || groups[0] != group {
			t.Fatalf("caller authority reached resolver: %s %s %s %v", tenant, country, identity, groups)
		}
		if unavailable {
			return nil, errors.New("synthetic membership owner unavailable")
		}
		return map[string]bool{group: member}, nil
	})
	handler, _ := NewHandlerWithAudiences(service, resolver)
	call := func(path, identity, roles string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Planext4u-Tenant", snapshot.TenantID)
		req.Header.Set("X-Planext4u-Country", "IN")
		req.Header.Set("X-Planext4u-Subject", identity)
		req.Header.Set("X-Planext4u-Roles", roles)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	bootstrap := func(want int) {
		t.Helper()
		out := call("/v1/bootstrap?platform=ANDROID&app_version=1.2.0&subject_id=attacker&audience_groups=forged", subject, "CUSTOMER")
		var value Bootstrap
		if err := json.Unmarshal(out.Body.Bytes(), &value); err != nil || out.Code != 200 || len(value.Pages) != want {
			t.Fatalf("bootstrap %d pages=%d want=%d %v", out.Code, len(value.Pages), want, err)
		}
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("audience response cached")
		}
	}
	bootstrap(2)
	if out := call("/v1/pages/restricted?subject_id=attacker", subject, "CUSTOMER"); out.Code != 200 {
		t.Fatal(out.Code)
	}
	member = false
	bootstrap(1)
	if out := call("/v1/pages/restricted?audience_groups="+group, subject, "CUSTOMER"); out.Code != 404 {
		t.Fatal("revoked group retained through cached snapshot", out.Code)
	}
	member = true
	before := calls
	if out := call("/v1/pages/restricted?subject_id="+subject, "", "CUSTOMER"); out.Code != 404 {
		t.Fatal("query supplied missing identity", out.Code)
	}
	if out := call("/v1/pages/restricted", subject, "VENDOR"); out.Code != 404 {
		t.Fatal("group expanded base role", out.Code)
	}
	if calls != before {
		t.Fatal("unauthenticated/ineligible role caused membership query")
	}
	unavailable = true
	for _, path := range []string{"/v1/pages/restricted", "/v1/bootstrap?platform=ANDROID&app_version=1.2.0"} {
		out := call(path, subject, "CUSTOMER")
		if out.Code != 503 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("owner outage did not fail closed", out.Code)
		}
		var value struct {
			Error struct {
				Retryable bool `json:"retryable"`
			} `json:"error"`
		}
		if json.Unmarshal(out.Body.Bytes(), &value) != nil || !value.Error.Retryable {
			t.Fatal("unavailable response must allow retry")
		}
	}
	missing, _ := NewHandler(service)
	req := httptest.NewRequest("GET", "/v1/pages/restricted", nil)
	req.Header.Set("X-Planext4u-Tenant", snapshot.TenantID)
	req.Header.Set("X-Planext4u-Country", "IN")
	req.Header.Set("X-Planext4u-Subject", subject)
	req.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	out := httptest.NewRecorder()
	missing.ServeHTTP(out, req)
	if out.Code != 503 {
		t.Fatal("unconfigured membership accepted", out.Code)
	}
}

func TestAudienceGroupSnapshotValidationAndCopies(t *testing.T) {
	now := time.Now().UTC()
	snapshot := validSnapshot(now)
	group := uuid.NewString()
	page := Page{ID: "targeted", Route: "/targeted", TitleKey: "page.title", Audience: []string{"CUSTOMER"}, AudienceGroups: []string{group}, Enabled: true, Blocks: []PageBlock{}}
	snapshot.Pages = []Page{page}
	repository, err := NewMemoryRepository(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Pages[0].AudienceGroups[0] = uuid.NewString()
	first, err := repository.Get(context.Background(), snapshot.TenantID, snapshot.Country)
	if err != nil || first.Pages[0].AudienceGroups[0] != group {
		t.Fatal("source alias changed stored criteria", err)
	}
	first.Pages[0].AudienceGroups[0] = uuid.NewString()
	second, _ := repository.Get(context.Background(), snapshot.TenantID, snapshot.Country)
	if second.Pages[0].AudienceGroups[0] != group {
		t.Fatal("read alias changed stored criteria")
	}
	for _, groups := range [][]string{{"not-a-group"}, {group, group}, make([]string, 17)} {
		snapshot.Pages[0].AudienceGroups = groups
		if _, err = NewMemoryRepository(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("invalid group criteria accepted: %v", err)
		}
	}
}
