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

var testScope = Scope{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000001"}

const testAsset = "a0000000-0000-4000-8000-000000000001"

func TestPrivateEvidenceUsesOwnerScopeAndRejectsUnusableAssets(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"ready identity":  {200, fmt.Sprintf(`{"id":%q,"purpose":"IDENTITY_DOCUMENT","state":"READY"}`, testAsset), nil},
		"other owner":     {404, `{}`, ErrRejected},
		"forbidden":       {403, `{}`, ErrRejected},
		"unscanned":       {200, fmt.Sprintf(`{"id":%q,"purpose":"IDENTITY_DOCUMENT","state":"PENDING_SCAN"}`, testAsset), ErrRejected},
		"quarantined":     {200, fmt.Sprintf(`{"id":%q,"purpose":"IDENTITY_DOCUMENT","state":"REJECTED"}`, testAsset), ErrRejected},
		"wrong purpose":   {200, fmt.Sprintf(`{"id":%q,"purpose":"AVATAR","state":"READY"}`, testAsset), ErrRejected},
		"wrong reference": {200, `{"id":"unrelated","purpose":"IDENTITY_DOCUMENT","state":"READY"}`, ErrRejected},
		"provider outage": {503, `{}`, ErrUnavailable},
		"malformed":       {200, `{`, ErrUnavailable},
		"oversized":       {200, strings.Repeat(" ", 32769), ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/media/"+testAsset || r.Header.Get("X-Planext4u-Tenant") != testScope.TenantID || r.Header.Get("X-Planext4u-Country") != "IN" || r.Header.Get("X-Planext4u-Subject") != testScope.OwnerID {
					t.Error("trusted owner scope was lost")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			client, err := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if err = client.VerifyIdentityDocuments(context.Background(), testScope, []string{testAsset}); !errors.Is(err, tc.want) {
				t.Fatalf("verification=%v want %v", err, tc.want)
			}
			if name == "ready identity" {
				if err = client.VerifyIdentityDocuments(context.Background(), testScope, []string{strings.ToUpper(testAsset)}); err != nil {
					t.Fatalf("canonical UUID rejected: %v", err)
				}
			}
		})
	}
}

func TestPrivateEvidenceDoesNotForwardScopeAcrossRedirects(t *testing.T) {
	forwarded := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client, _ := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second})
	if err := client.VerifyIdentityDocuments(context.Background(), testScope, []string{testAsset}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if forwarded {
		t.Fatal("private scope followed a redirect")
	}
}

func TestPrivateEvidenceFailsClosedWhenMissingMalformedOrCancelled(t *testing.T) {
	client, err := NewHTTPVerifier(nil, nil)
	if err != nil || !errors.Is(client.VerifyIdentityDocuments(context.Background(), testScope, []string{testAsset}), ErrUnavailable) {
		t.Fatal("missing provider allowed evidence")
	}
	for _, raw := range []string{"ftp://media.internal", "https://user:secret@media.internal", "https://media.internal/path", "https://media.internal?redirect=other"} {
		endpoint, _ := url.Parse(raw)
		if _, err = NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}); err == nil {
			t.Fatal("invalid provider configuration accepted")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid/cancelled proof reached provider") }))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client, _ = NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second})
	for _, scope := range []Scope{{}, {TenantID: testScope.TenantID, OwnerID: testScope.OwnerID, Country: "bad"}} {
		if err = client.VerifyIdentityDocuments(context.Background(), scope, []string{testAsset}); !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	}
	for _, ids := range [][]string{{}, {testAsset, testAsset}, {testAsset, strings.ToUpper(testAsset)}, {"forged"}} {
		if err = client.VerifyIdentityDocuments(context.Background(), testScope, ids); !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = client.VerifyIdentityDocuments(ctx, testScope, []string{testAsset}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
