package configcms

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type geographyStub struct {
	calls                                        int
	tenant, country, kind, parent, search, after string
	err                                          error
}

func (s *geographyStub) Geography(_ context.Context, t, c, k, p, q, a string) (GeographyPage, error) {
	s.calls++
	s.tenant, s.country, s.kind, s.parent, s.search, s.after = t, c, k, p, q, a
	return GeographyPage{Country: c, Kind: k, ParentID: p, Items: []GeographyReference{}}, s.err
}
func TestGeographyDirectoryHTTPRejectsForgedScopeAndInvalidHierarchy(t *testing.T) {
	repo, _ := NewMemoryRepository(validSnapshot(time.Now()))
	service, _ := NewService(repo, time.Now)
	stub := &geographyStub{}
	handler, _ := NewHandlerWithReferenceDirectories(service, nil, PublicStartupConfig{}, nil, stub)
	tenant, parent := uuid.NewString(), uuid.NewString()
	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-Planext4u-Tenant", tenant)
		r.Header.Set("X-Planext4u-Country", "IN")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"?kind=OTHER", "?kind=STATE", "?kind=AREA&parent_id=invalid", "?kind=COUNTRY&parent_id=" + parent, "?kind=COUNTRY&country=US", "?kind=COUNTRY&kind=AREA", "?kind=COUNTRY&after=bad", "?kind=COUNTRY&search=%00"} {
		if got := call("/v1/geography" + path); got.Code != 422 {
			t.Fatalf("%s: %d", path, got.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid queries reached owner")
	}
	got := call("/v1/geography?kind=STATE&parent_id=" + parent + "&search=%25")
	if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" || stub.tenant != tenant || stub.country != "IN" || stub.kind != "STATE" || stub.parent != parent || stub.search != "%" {
		t.Fatalf("current hierarchy: %d %+v", got.Code, stub)
	}
	stub.err = errors.New("private directory failure")
	if got = call("/v1/geography?kind=COUNTRY"); got.Code != 503 || strings.Contains(got.Body.String(), "private") {
		t.Fatalf("unsafe failure: %d %s", got.Code, got.Body.String())
	}
	missing, _ := NewHandler(service)
	r := httptest.NewRequest(http.MethodGet, "/v1/geography?kind=COUNTRY", nil)
	r.Header.Set("X-Planext4u-Tenant", tenant)
	r.Header.Set("X-Planext4u-Country", "IN")
	got = httptest.NewRecorder()
	missing.ServeHTTP(got, r)
	if got.Code != 503 {
		t.Fatal(got.Code)
	}
	if _, err := NewPostgresGeographyDirectory(nil); err == nil {
		t.Fatal("nil directory accepted")
	}
	for _, kind := range []string{"COUNTRY", "STATE", "DISTRICT", "CITY", "AREA"} {
		p := parent
		if kind == "COUNTRY" {
			p = ""
		}
		if !validGeographyQuery(tenant, "IN", kind, p, "", "") {
			t.Fatal(kind)
		}
	}
}
