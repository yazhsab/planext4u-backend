package configcms

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type occupationStub struct {
	calls                          int
	tenant, country, search, after string
	err                            error
}

func (stub *occupationStub) Occupations(_ context.Context, tenant, country, search, after string) (OccupationPage, error) {
	stub.calls++
	stub.tenant, stub.country, stub.search, stub.after = tenant, country, search, after
	return OccupationPage{Country: country, Items: []Occupation{}}, stub.err
}
func TestOccupationDirectoryHTTPQueryIsolationAndFailure(t *testing.T) {
	now := time.Now()
	repository, _ := NewMemoryRepository(validSnapshot(now))
	service, _ := NewService(repository, time.Now)
	stub := &occupationStub{}
	handler, _ := NewHandlerWithOccupations(service, nil, PublicStartupConfig{}, stub)
	tenant := uuid.NewString()
	call := func(path string, region string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-Planext4u-Tenant", tenant)
		request.Header.Set("X-Planext4u-Country", region)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, path := range []string{"/v1/occupations?country=US", "/v1/occupations?tenant_id=forged", "/v1/occupations?search=a&search=b", "/v1/occupations?after=invalid", "/v1/occupations?search=%00"} {
		if got := call(path, "IN"); got.Code != 422 {
			t.Fatalf("%s: %d", path, got.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid query reached directory")
	}
	if got := call("/v1/occupations", "in"); got.Code != 422 {
		t.Fatal(got.Code)
	}
	got := call("/v1/occupations?search=%25", "IN")
	if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" || stub.tenant != tenant || stub.country != "IN" || stub.search != "%" {
		t.Fatalf("current scope: %d %+v", got.Code, stub)
	}
	stub.err = errors.New("synthetic private database details")
	if got = call("/v1/occupations", "IN"); got.Code != 503 {
		t.Fatal(got.Code)
	}
	missing, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/v1/occupations", nil)
	request.Header.Set("X-Planext4u-Tenant", tenant)
	request.Header.Set("X-Planext4u-Country", "IN")
	got = httptest.NewRecorder()
	missing.ServeHTTP(got, request)
	if got.Code != 503 {
		t.Fatalf("missing owner: %d", got.Code)
	}
}
