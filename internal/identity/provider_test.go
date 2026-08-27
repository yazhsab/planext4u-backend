package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProviderVerifierAcceptsIdentityButNeverProviderRoles(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/identity/verify" {
			t.Errorf("provider request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"active":true,"provider":"local","subject":"provider_subject_001","roles":["ADMIN"]}`))
	}))
	defer provider.Close()
	verifier := newProviderVerifier(t, provider.URL, 500*time.Millisecond)

	identity, err := verifier.Verify(context.Background(), "local", "synthetic-provider-token")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Provider != "local" || identity.Subject != "provider_subject_001" {
		t.Fatalf("identity = %#v", identity)
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d", requests.Load())
	}

	if _, err := verifier.Verify(context.Background(), "unapproved", "synthetic-provider-token"); !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("unapproved provider error = %v", err)
	}
	if _, err := verifier.Verify(context.Background(), "local", "token with whitespace"); !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("unsafe token error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("invalid local input reached provider")
	}
}

func TestHTTPProviderVerifierNormalizesProviderFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.Handler
		timeout time.Duration
		want    error
	}{
		{
			name: "rejected",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"provider_secret":"must-not-escape"}`))
			}),
			timeout: time.Second,
			want:    ErrProviderRejected,
		},
		{
			name: "malformed success",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"active":true,"provider":"local"}`))
			}),
			timeout: time.Second,
			want:    ErrProviderRejected,
		},
		{
			name: "unknown success field",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"active":true,"provider":"local","subject":"subject_001","unsafe":"value"}`))
			}),
			timeout: time.Second,
			want:    ErrProviderUnavailable,
		},
		{
			name: "oversized",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(strings.Repeat("x", maxProviderResponseBytes+1)))
			}),
			timeout: time.Second,
			want:    ErrProviderUnavailable,
		},
		{
			name: "timeout",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				time.Sleep(100 * time.Millisecond)
				_, _ = writer.Write([]byte(`{"active":true,"provider":"local","subject":"late"}`))
			}),
			timeout: 10 * time.Millisecond,
			want:    ErrProviderUnavailable,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(test.handler)
			defer server.Close()
			verifier := newProviderVerifier(t, server.URL, test.timeout)
			_, err := verifier.Verify(context.Background(), "local", "synthetic-token")
			if !errors.Is(err, test.want) {
				t.Fatalf("Verify() error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "provider_secret") {
				t.Fatalf("provider payload leaked in error: %v", err)
			}
		})
	}
}

func TestHTTPProviderVerifierRefusesRedirects(t *testing.T) {
	t.Parallel()
	targetRequests := atomic.Int64{}
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		_, _ = writer.Write([]byte(`{"active":true,"provider":"local","subject":"unexpected"}`))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	verifier := newProviderVerifier(t, redirector.URL, time.Second)
	if _, err := verifier.Verify(context.Background(), "local", "synthetic-token"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("redirect error = %v", err)
	}
	if targetRequests.Load() != 0 {
		t.Fatal("provider verifier followed redirect")
	}
}

func newProviderVerifier(t *testing.T, rawURL string, timeout time.Duration) *HTTPProviderVerifier {
	t.Helper()
	baseURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewHTTPProviderVerifier(HTTPProviderVerifierConfig{
		BaseURL:          baseURL,
		AllowedProviders: []string{"local", "firebase", "oidc"},
		Timeout:          timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(verifier.CloseIdleConnections)
	return verifier
}
