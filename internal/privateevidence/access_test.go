package privateevidence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestReviewerGrantIsScopedReadyAndShortLived(t *testing.T) {
	accesses := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Planext4u-Tenant") != testScope.TenantID || r.Header.Get("X-Planext4u-Country") != testScope.Country || r.Header.Get("X-Planext4u-Subject") != testScope.OwnerID {
			t.Error("media owner scope changed")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/media/"+testAsset:
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"purpose":"IDENTITY_DOCUMENT","state":"READY"}`, testAsset)))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/media/"+testAsset+"/access":
			accesses++
			_, _ = w.Write([]byte(fmt.Sprintf(`{"asset_id":%q,"url":"https://private.example/document?signature=secret","expires_at":%q}`, testAsset, time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano))))
		default:
			t.Errorf("unexpected media request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	verifier, err := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider := verifier.(AccessProvider)
	grant, err := provider.AccessIdentityDocument(t.Context(), testScope, testAsset)
	if err != nil || grant.URL != "https://private.example/document?signature=secret" || accesses != 1 {
		t.Fatalf("grant=%+v error=%v accesses=%d", grant, err, accesses)
	}
	if _, err = provider.AccessIdentityDocument(t.Context(), testScope, "not-an-asset"); !errors.Is(err, ErrRejected) || accesses != 1 {
		t.Fatalf("invalid reference reached media: %v", err)
	}
}

func TestReviewerGrantFailsClosedForUnsafeOrUnavailableMedia(t *testing.T) {
	for name, tc := range map[string]struct {
		getStatus  int
		postStatus int
		postBody   string
		want       error
		postCalls  int
	}{
		"unscanned":       {getStatus: 200, postStatus: 200, postBody: `{"asset_id":"unused"}`, want: ErrRejected},
		"other owner":     {getStatus: 404, want: ErrRejected},
		"media denied":    {getStatus: 200, postStatus: 403, want: ErrRejected, postCalls: 1},
		"unsafe url":      {getStatus: 200, postStatus: 200, postBody: `{"asset_id":"` + testAsset + `","url":"javascript:alert(1)","expires_at":"2099-01-01T00:00:00Z"}`, want: ErrUnavailable, postCalls: 1},
		"wrong reference": {getStatus: 200, postStatus: 200, postBody: `{"asset_id":"a0000000-0000-4000-8000-000000000099","url":"https://private.example/x?sig=1","expires_at":"2099-01-01T00:00:00Z"}`, want: ErrUnavailable, postCalls: 1},
		"redirect":        {getStatus: 200, postStatus: 307, want: ErrUnavailable, postCalls: 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(tc.getStatus)
					state := "READY"
					if name == "unscanned" {
						state = "PENDING_SCAN"
					}
					_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"purpose":"IDENTITY_DOCUMENT","state":%q}`, testAsset, state)))
					return
				}
				calls++
				if name == "redirect" {
					w.Header().Set("Location", "https://other.example/")
				}
				w.WriteHeader(tc.postStatus)
				_, _ = w.Write([]byte(tc.postBody))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			verifier, _ := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second})
			_, err := verifier.(AccessProvider).AccessIdentityDocument(t.Context(), testScope, testAsset)
			if !errors.Is(err, tc.want) || calls != tc.postCalls {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
	if validAccessGrant("http://evil.example/x?sig=1", time.Now().Add(time.Minute)) || validAccessGrant("https://user:pass@private.example/x?sig=1", time.Now().Add(time.Minute)) || validAccessGrant("https://private.example/x?sig=1#fragment", time.Now().Add(time.Minute)) || validAccessGrant("https://private.example/x?sig=1", time.Now().Add(3*time.Minute)) {
		t.Fatal("unsafe grant accepted")
	}
	verifier := Unavailable()
	if _, ok := verifier.(AccessProvider); ok {
		t.Fatal("unconfigured verifier exposed reviewer access")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached media") }))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	configured, _ := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second})
	if _, err := configured.(AccessProvider).AccessIdentityDocument(ctx, testScope, strings.ToUpper(testAsset)); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
