package bankverification

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

func TestVerifiedBankProofIsFreshAndBoundToOwnerReferenceAndMaskedAccount(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	scope := Scope{TenantID: "10000000-0000-4000-8000-000000000001", OwnerID: "20000000-0000-4000-8000-000000000001", Country: "IN"}
	account := Account{Reference: "bankref_synthetic1234", HolderName: "Synthetic Owner", Last4: "1234", IFSC: "TEST0123456"}
	for _, test := range []struct {
		name   string
		mutate func(*proof)
		want   error
	}{
		{"valid", func(*proof) {}, nil},
		{"another-owner", func(p *proof) { p.OwnerID = "20000000-0000-4000-8000-000000000002" }, ErrRejected},
		{"another-tenant", func(p *proof) { p.TenantID = "10000000-0000-4000-8000-000000000002" }, ErrRejected},
		{"another-country", func(p *proof) { p.Country = "GB" }, ErrRejected},
		{"another-reference", func(p *proof) { p.Reference = "bankref_other123456" }, ErrRejected},
		{"another-mask", func(p *proof) { p.Last4 = "9999" }, ErrRejected},
		{"another-holder", func(p *proof) { p.HolderName = "Different Owner" }, ErrRejected},
		{"another-bank", func(p *proof) { p.IFSC = "DIFF0123456" }, ErrRejected},
		{"pending", func(p *proof) { p.Status = "PENDING" }, ErrRejected},
		{"stale", func(p *proof) { p.VerifiedAt = now.Add(-16 * time.Minute) }, ErrRejected},
		{"future", func(p *proof) { p.VerifiedAt = now.Add(2 * time.Minute) }, ErrRejected},
		{"expired", func(p *proof) { p.ExpiresAt = now }, ErrRejected},
		{"unbounded", func(p *proof) { p.ExpiresAt = now.Add(25 * time.Hour) }, ErrRejected},
		{"invalid-provenance", func(p *proof) { p.VerificationID = "client claims verified" }, ErrRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/bank-accounts/verification:resolve" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
					t.Error("provider request not authenticated")
				}
				var input struct {
					Scope
					Account
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.Scope != scope || input.Account != account {
					t.Error("provider scope changed")
				}
				p := proof{Scope: scope, Account: account, VerificationID: "verification_123456", Status: "VERIFIED", VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
				test.mutate(&p)
				_ = json.NewEncoder(w).Encode(p)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			v, err := NewHTTPVerifier(endpoint, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			id, err := v.Verify(context.Background(), scope, account)
			if !errors.Is(err, test.want) || (err == nil && id != "verification_123456") || (err != nil && id != "") {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
}

func TestBankProviderNeverRedirectsOrAcceptsUnavailableMalformedResults(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	scope := Scope{TenantID: "10000000-0000-4000-8000-000000000001", OwnerID: "20000000-0000-4000-8000-000000000001", Country: "IN"}
	for _, body := range []string{"redirect", "unavailable", "malformed", "oversized"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch body {
				case "redirect":
					http.Redirect(w, r, target.URL, 307)
				case "unavailable":
					w.WriteHeader(503)
				case "malformed":
					_, _ = w.Write([]byte(`{"status":true}`))
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", 32769)))
				}
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			v, _ := NewHTTPVerifier(u, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now)
			if _, err := v.Verify(t.Context(), scope, Account{Reference: "bankref_synthetic1234"}); !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
	if received.Load() != 0 {
		t.Fatal("bank proof/token followed redirect")
	}
	v, _ := NewHTTPVerifier(nil, nil, "", time.Now)
	if _, err := v.Verify(t.Context(), scope, Account{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfigured bank accepted")
	}
	for _, endpoint := range []string{"http://user:pass@provider.example", "https://provider.example/path", "https://provider.example?token=secret"} {
		u, _ := url.Parse(endpoint)
		if _, err := NewHTTPVerifier(u, &http.Client{Timeout: time.Second}, strings.Repeat("s", 32), time.Now); err == nil {
			t.Fatal("unsafe destination accepted")
		}
	}
}
