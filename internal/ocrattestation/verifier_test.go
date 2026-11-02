package ocrattestation

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

func TestOCRProofIsBoundToExactScopeRevisionAndDocuments(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	scope := Scope{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000001", DocumentRevision: 3}
	documents := []Document{{Kind: "OWNER_IDENTITY", AssetID: "a0000000-0000-4000-8000-000000000001"}, {Kind: "BUSINESS_REGISTRATION", AssetID: "a0000000-0000-4000-8000-000000000002"}}
	for _, test := range []struct {
		name   string
		mutate func(*proof)
		want   error
	}{
		{"valid", func(*proof) {}, nil},
		{"tenant", func(p *proof) { p.TenantID = "10000000-0000-4000-8000-000000000002" }, ErrRejected},
		{"country", func(p *proof) { p.Country = "GB" }, ErrRejected},
		{"owner", func(p *proof) { p.OwnerID = "20000000-0000-4000-8000-000000000002" }, ErrRejected},
		{"document revision", func(p *proof) { p.DocumentRevision++ }, ErrRejected},
		{"asset", func(p *proof) { p.Documents[0].AssetID = "a0000000-0000-4000-8000-000000000003" }, ErrRejected},
		{"kind", func(p *proof) { p.Documents[0].Kind = "DRIVER_LICENSE" }, ErrRejected},
		{"missing document", func(p *proof) { p.Documents = p.Documents[:1] }, ErrRejected},
		{"duplicate document", func(p *proof) { p.Documents[1] = p.Documents[0] }, ErrRejected},
		{"purpose", func(p *proof) { p.Purpose = "MEDIA_SCAN" }, ErrRejected},
		{"pending", func(p *proof) { p.Status = "PENDING" }, ErrRejected},
		{"stale", func(p *proof) { p.VerifiedAt = now.Add(-16 * time.Minute) }, ErrRejected},
		{"future", func(p *proof) { p.VerifiedAt = now.Add(2 * time.Minute) }, ErrRejected},
		{"expired", func(p *proof) { p.ExpiresAt = now }, ErrRejected},
		{"invalid ID", func(p *proof) { p.AttestationID = "untrusted result" }, ErrRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/ocr-verification:resolve" ||
					r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
					t.Error("untrusted OCR provider request")
				}
				var input struct {
					Scope
					Purpose   string     `json:"purpose"`
					Documents []Document `json:"documents"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.Scope != scope ||
					input.Purpose != "VENDOR_ONBOARDING_OCR" || len(input.Documents) != 2 ||
					input.Documents[0].Kind != "BUSINESS_REGISTRATION" || input.Documents[1].Kind != "OWNER_IDENTITY" {
					t.Error("OCR request changed scope, revision, or sorted documents")
				}
				result := proof{Scope: scope, Purpose: "VENDOR_ONBOARDING_OCR", Documents: append([]Document(nil), documents...),
					AttestationID: "ocr_attestation_123456", Status: "VERIFIED", VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
				test.mutate(&result)
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			verifier, err := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			id, err := verifier.Verify(t.Context(), scope, documents)
			if !errors.Is(err, test.want) || err == nil && id != "ocr_attestation_123456" || err != nil && id != "" {
				t.Fatalf("attestation=%q err=%v", id, err)
			}
		})
	}
}

func TestOCRProviderFailsClosedWhenUnavailableOrMalformed(t *testing.T) {
	scope := Scope{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000001", DocumentRevision: 2}
	documents := []Document{{Kind: "OWNER_IDENTITY", AssetID: "a0000000-0000-4000-8000-000000000001"}}
	verifier, _ := NewHTTPVerifier(nil, nil, "", time.Now)
	if _, err := verifier.Verify(t.Context(), scope, documents); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfigured OCR provider accepted")
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, response := range []string{"redirect", "unavailable", "malformed", "oversized", "rejected"} {
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
				case "rejected":
					w.WriteHeader(http.StatusUnprocessableEntity)
				}
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			verifier, _ := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now)
			_, err := verifier.Verify(context.Background(), scope, documents)
			want := ErrUnavailable
			if response == "rejected" {
				want = ErrRejected
			}
			if !errors.Is(err, want) {
				t.Fatalf("provider error=%v, want %v", err, want)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("OCR credential followed a redirect")
	}
	for _, invalid := range []Scope{{TenantID: "not-a-uuid", Country: "IN", OwnerID: scope.OwnerID, DocumentRevision: 2}, {TenantID: scope.TenantID, Country: "IN", OwnerID: scope.OwnerID, DocumentRevision: 0}} {
		if _, err := verifier.Verify(t.Context(), invalid, documents); !errors.Is(err, ErrUnavailable) {
			// The default verifier never trusts input, and never reaches a provider.
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"http://user:pass@provider.example", "https://provider.example/path", "https://provider.example?token=secret"} {
		endpoint, _ := url.Parse(raw)
		if _, err := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now); err == nil {
			t.Fatal("unsafe OCR endpoint accepted")
		}
	}
}
