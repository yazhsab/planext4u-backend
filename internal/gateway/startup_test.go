package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnonymousStartupStripsIdentityAndOnlyForwardsExactGET(t *testing.T) {
	captured := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"splash":null}`))
	}))
	defer upstream.Close()
	config := DefaultConfig(mustURL(t, upstream.URL))
	config.AnonymousRoutes["/v1/startup"] = map[string]struct{}{http.MethodGet: {}}
	handler, err := NewHandler(config, fixedVerifier{err: ErrTokenInvalid}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/v1/startup?country=IN&role=customer", nil)
	for _, name := range []string{"Authorization", "Cookie", "X-Planext4u-Tenant", "X-Planext4u-Subject", "X-Planext4u-Country", "X-Planext4u-Roles", "X-Planext4u-MFA"} {
		request.Header.Set(name, "synthetic-hostile-authority")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("public startup failed: %d %s", response.Code, response.Body.String())
	}
	headers := <-captured
	for _, name := range []string{"Authorization", "Cookie", "X-Planext4u-Tenant", "X-Planext4u-Subject", "X-Planext4u-Country", "X-Planext4u-Roles", "X-Planext4u-MFA"} {
		if headers.Get(name) != "" {
			t.Fatalf("anonymous startup received identity: %s", name)
		}
	}
	for _, path := range []string{"GET /v1/bootstrap", "GET /v1/startup/extra", "POST /v1/startup", "DELETE /v1/startup"} {
		method, target := path[:3], path[4:]
		if path[0] == 'P' {
			method, target = "POST", "/v1/startup"
		} else if path[0] == 'D' {
			method, target = "DELETE", "/v1/startup"
		}
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(method, target, nil))
		if r.Code != 401 {
			t.Fatalf("anonymous method/path escaped: %s %d", path, r.Code)
		}
	}
	config.AnonymousRoutes["/v1/bootstrap"] = map[string]struct{}{"GET": {}}
	if _, err := NewHandler(config, fixedVerifier{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("generic anonymous GET configuration accepted")
	}
}
