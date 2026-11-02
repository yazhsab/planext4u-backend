package kycattestation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestKYCProofIsFreshAndBoundToExactScopeAndDocumentSet(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	scope := Scope{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000001"}
	documents := []Document{{Kind: "OWNER_IDENTITY", AssetID: "a0000000-0000-4000-8000-000000000001"}, {Kind: "BUSINESS_REGISTRATION", AssetID: "a0000000-0000-4000-8000-000000000002"}}
	for _, test := range []struct {
		name   string
		mutate func(*proof)
		want   error
	}{
		{"valid", func(*proof) {}, nil},
		{"another tenant", func(p *proof) { p.TenantID = "10000000-0000-4000-8000-000000000002" }, ErrRejected},
		{"another country", func(p *proof) { p.Country = "GB" }, ErrRejected},
		{"another owner", func(p *proof) { p.OwnerID = "20000000-0000-4000-8000-000000000002" }, ErrRejected},
		{"another asset", func(p *proof) { p.Documents[0].AssetID = "a0000000-0000-4000-8000-000000000003" }, ErrRejected},
		{"another kind", func(p *proof) { p.Documents[0].Kind = "DRIVER_LICENSE" }, ErrRejected},
		{"missing document", func(p *proof) { p.Documents = p.Documents[:1] }, ErrRejected},
		{"duplicate document", func(p *proof) { p.Documents[1] = p.Documents[0] }, ErrRejected},
		{"wrong purpose", func(p *proof) { p.Purpose = "MEDIA_SCAN" }, ErrRejected},
		{"pending", func(p *proof) { p.Status = "PENDING" }, ErrRejected},
		{"stale", func(p *proof) { p.VerifiedAt = now.Add(-16 * time.Minute) }, ErrRejected},
		{"future", func(p *proof) { p.VerifiedAt = now.Add(2 * time.Minute) }, ErrRejected},
		{"expired", func(p *proof) { p.ExpiresAt = now }, ErrRejected},
		{"unbounded expiry", func(p *proof) { p.ExpiresAt = now.Add(25 * time.Hour) }, ErrRejected},
		{"invalid attestation", func(p *proof) { p.AttestationID = "client said yes" }, ErrRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/identity-documents/verification:resolve" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) || r.Header.Get("Content-Type") != "application/json" {
					t.Error("untrusted provider request")
				}
				var input struct {
					Scope
					Purpose   string     `json:"purpose"`
					Documents []Document `json:"documents"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.Scope != scope || input.Purpose != "ONBOARDING_KYC" || len(input.Documents) != 2 || input.Documents[0].Kind != "BUSINESS_REGISTRATION" || input.Documents[1].Kind != "OWNER_IDENTITY" {
					t.Error("scope or ordered document set changed")
				}
				result := proof{Scope: scope, Purpose: "ONBOARDING_KYC", Documents: append([]Document(nil), documents...), AttestationID: "kyc_attestation_123456", Status: "VERIFIED", VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
				test.mutate(&result)
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			verifier, err := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			id, err := verifier.Verify(context.Background(), scope, documents)
			if !errors.Is(err, test.want) || err == nil && id != "kyc_attestation_123456" || err != nil && id != "" {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
}

func TestKYCProviderFailsClosedOnInvalidInputOrResponse(t *testing.T) {
	scope := Scope{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000001"}
	documents := []Document{{Kind: "OWNER_IDENTITY", AssetID: "a0000000-0000-4000-8000-000000000001"}}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, response := range []string{"redirect", "unavailable", "malformed", "oversized"} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch response {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "unavailable":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "malformed":
					_, _ = w.Write([]byte(`{"status":true}`))
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", 32769)))
				}
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			verifier, _ := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now)
			if _, err := verifier.Verify(t.Context(), scope, documents); !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("attestation credential followed redirect")
	}
	verifier, _ := NewHTTPVerifier(nil, nil, "", time.Now)
	if _, err := verifier.Verify(t.Context(), scope, documents); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfigured provider accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid request reached provider") }))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	verifier, _ = NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now)
	for _, input := range [][]Document{nil, {{Kind: "OWNER_IDENTITY", AssetID: "not-an-asset"}}, {{Kind: "OWNER_IDENTITY", AssetID: "A0000000-0000-4000-8000-000000000001"}}, {documents[0], documents[0]}} {
		if _, err := verifier.Verify(t.Context(), scope, input); !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	}
	if _, err := verifier.Verify(nil, scope, documents); !errors.Is(err, ErrRejected) {
		t.Fatal("nil request context accepted", err)
	}
	for _, bad := range []Scope{
		{TenantID: "A0000000-0000-4000-8000-000000000001", Country: scope.Country, OwnerID: scope.OwnerID},
		{TenantID: scope.TenantID, Country: scope.Country, OwnerID: "B0000000-0000-4000-8000-000000000001"},
	} {
		if _, err := verifier.Verify(t.Context(), bad, documents); !errors.Is(err, ErrRejected) {
			t.Fatal("noncanonical scope accepted", err)
		}
	}
	for _, raw := range []string{"http://user:pass@provider.example", "https://provider.example/path", "https://provider.example?token=secret"} {
		u, _ := url.Parse(raw)
		if _, err := NewHTTPVerifier(u, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now); err == nil {
			t.Fatal("unsafe attestation endpoint accepted")
		}
	}
}
