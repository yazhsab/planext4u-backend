package configcms

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type reasonStub struct {
	calls                          int
	tenant, country, search, after string
	err                            error
}

func (stub *reasonStub) Reasons(_ context.Context, tenant, country, search, after string) (ReasonPage, error) {
	stub.calls++
	stub.tenant, stub.country, stub.search, stub.after = tenant, country, search, after
	return ReasonPage{Country: country, Items: []Reason{}}, stub.err
}

func TestReasonDirectoryHTTPQueryIsolationAndFailure(t *testing.T) {
	repository, _ := NewMemoryRepository(validSnapshot(time.Now()))
	service, _ := NewService(repository, time.Now)
	stub := &reasonStub{}
	handler, _ := NewHandlerWithAllReferenceDirectories(service, nil, PublicStartupConfig{}, nil, nil, stub)
	tenant := uuid.NewString()
	call := func(path, country string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-Planext4u-Tenant", tenant)
		request.Header.Set("X-Planext4u-Country", country)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, path := range []string{"/v1/reasons?country=US", "/v1/reasons?tenant_id=forged", "/v1/reasons?search=a&search=b", "/v1/reasons?after=invalid", "/v1/reasons?search=%00"} {
		if got := call(path, "IN"); got.Code != 422 {
			t.Fatalf("%s: %d", path, got.Code)
		}
	}
	if stub.calls != 0 || call("/v1/reasons", "in").Code != 422 {
		t.Fatal("invalid scope reached reason directory")
	}
	got := call("/v1/reasons?search=%25", "IN")
	if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" || stub.tenant != tenant || stub.country != "IN" || stub.search != "%" {
		t.Fatalf("current scope: %d %+v", got.Code, stub)
	}
	stub.err = errors.New("synthetic private database details")
	got = call("/v1/reasons", "IN")
	if got.Code != 503 || strings.Contains(got.Body.String(), "private") {
		t.Fatalf("owner failure: %d %s", got.Code, got.Body.String())
	}
	missing, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/v1/reasons", nil)
	request.Header.Set("X-Planext4u-Tenant", tenant)
	request.Header.Set("X-Planext4u-Country", "IN")
	got = httptest.NewRecorder()
	missing.ServeHTTP(got, request)
	if got.Code != 503 {
		t.Fatalf("missing owner: %d", got.Code)
	}
}
