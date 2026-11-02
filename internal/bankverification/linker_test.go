package bankverification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var linkTestNow = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

func linkTestInput() LinkRequest {
	return LinkRequest{Scope: Scope{TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000001"}, ApplicationRole: "VENDOR", AttemptID: "30000000-0000-4000-8000-000000000001"}
}

func linkTestResult(input LinkRequest) LinkResult {
	input.ProviderSession = "session_synthetic_1234"
	return LinkResult{LinkRequest: input, Status: "AWAITING_USER", ExpiresAt: linkTestNow.Add(10 * time.Minute), LaunchURL: "https://bank.example.test/link?one_time=synthetic"}
}

func newTestLinker(t *testing.T, handler http.HandlerFunc) Linker {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	linker, err := NewHTTPLinker(LinkerConfig{Endpoint: endpoint, Client: &http.Client{Timeout: time.Second}, Token: strings.Repeat("s", 32), Clock: func() time.Time { return linkTestNow }, LaunchOrigins: []string{"https://bank.example.test"}, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return linker
}

func TestHostedBankLinkUsesScopedIdempotentAuthenticatedProviderProtocol(t *testing.T) {
	for _, action := range []string{"", ":resolve", ":cancel"} {
		t.Run(action, func(t *testing.T) {
			input := linkTestInput()
			if action != "" {
				input.ProviderSession = "session_synthetic_1234"
			}
			linker := newTestLinker(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/bank-accounts/link-sessions"+action || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) || r.Header.Get("Idempotency-Key") != input.AttemptID || r.Header.Get("Content-Type") != "application/json" {
					t.Error("provider request did not preserve the fixed authenticated protocol")
				}
				var received LinkRequest
				if json.NewDecoder(r.Body).Decode(&received) != nil || received != input {
					t.Error("request scope/role/attempt/session changed")
				}
				result := linkTestResult(input)
				if action == ":cancel" {
					result.Status, result.LaunchURL = "CANCELLED", ""
				}
				_ = json.NewEncoder(w).Encode(result)
			})
			var result LinkResult
			var err error
			switch action {
			case "":
				result, err = linker.Create(t.Context(), input)
			case ":resolve":
				result, err = linker.Resolve(t.Context(), input)
			case ":cancel":
				result, err = linker.Cancel(t.Context(), input)
			}
			if err != nil || result.ProviderSession != "session_synthetic_1234" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestHostedBankLinkAcceptsOnlyFreshBoundedMaskedProviderResults(t *testing.T) {
	for _, status := range []string{"AWAITING_USER", "VERIFYING", "VERIFIED", "DECLINED", "CANCELLED", "EXPIRED"} {
		t.Run(status, func(t *testing.T) {
			result := linkTestResult(linkTestInput())
			result.Status = status
			if status != "AWAITING_USER" {
				result.LaunchURL = ""
			}
			if status == "EXPIRED" {
				result.ExpiresAt = linkTestNow
			}
			if status == "VERIFIED" {
				result.Account = &Account{Reference: "bankref_synthetic1234", HolderName: "Synthetic Owner", Last4: "1234", IFSC: "TEST0123456"}
				result.VerificationID = "verification_1234"
				result.VerifiedAt = linkTestNow.Add(-time.Minute)
			}
			linker := newTestLinker(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(result) })
			if _, err := linker.Create(t.Context(), linkTestInput()); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*LinkResult)
	}{
		{"tenant", func(r *LinkResult) { r.TenantID = "10000000-0000-4000-8000-000000000002" }},
		{"owner", func(r *LinkResult) { r.OwnerID = "20000000-0000-4000-8000-000000000002" }},
		{"country", func(r *LinkResult) { r.Country = "GB" }},
		{"role", func(r *LinkResult) { r.ApplicationRole = "RIDER" }},
		{"attempt", func(r *LinkResult) { r.AttemptID = "30000000-0000-4000-8000-000000000002" }},
		{"session", func(r *LinkResult) { r.ProviderSession = "short" }},
		{"unknown-status", func(r *LinkResult) { r.Status = "APPROVED" }},
		{"expired", func(r *LinkResult) { r.ExpiresAt = linkTestNow }},
		{"unbounded-expiry", func(r *LinkResult) { r.ExpiresAt = linkTestNow.Add(31 * time.Minute) }},
		{"unapproved-origin", func(r *LinkResult) { r.LaunchURL = "https://bank.example.test.evil.test/link" }},
		{"userinfo", func(r *LinkResult) { r.LaunchURL = "https://secret@bank.example.test/link" }},
		{"fragment", func(r *LinkResult) { r.LaunchURL += "#secret" }},
		{"downgrade", func(r *LinkResult) { r.LaunchURL = "http://bank.example.test/link" }},
		{"alternate-port", func(r *LinkResult) { r.LaunchURL = "https://bank.example.test:444/link" }},
		{"javascript", func(r *LinkResult) { r.LaunchURL = "javascript:alert(1)" }},
		{"huge-url", func(r *LinkResult) { r.LaunchURL += strings.Repeat("s", 4096) }},
		{"pending-account", func(r *LinkResult) { r.Account = &Account{Reference: "bankref_synthetic1234"} }},
		{"pending-proof", func(r *LinkResult) { r.VerificationID = "verification_1234" }},
		{"pending-time", func(r *LinkResult) { r.VerifiedAt = linkTestNow }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := linkTestResult(linkTestInput())
			test.mutate(&result)
			linker := newTestLinker(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(result) })
			if got, err := linker.Create(t.Context(), linkTestInput()); !errors.Is(err, ErrRejected) || got.Status != "" {
				t.Fatalf("unsafe result accepted: %+v %v", got, err)
			}
		})
	}
}

func TestHostedBankLinkRejectsUnverifiedStaleOrUnmaskedCompletedResults(t *testing.T) {
	for _, mutate := range []func(*LinkResult){
		func(r *LinkResult) { r.Account = nil },
		func(r *LinkResult) { r.Account.Reference = "client_claim" },
		func(r *LinkResult) { r.Account.Last4 = "123456789012" },
		func(r *LinkResult) { r.Account.HolderName = "x" },
		func(r *LinkResult) { r.Account.HolderName = "Name\nInjected" },
		func(r *LinkResult) { r.Account.IFSC = strings.Repeat("s", 33) },
		func(r *LinkResult) { r.VerificationID = "" },
		func(r *LinkResult) { r.VerifiedAt = linkTestNow.Add(-16 * time.Minute) },
		func(r *LinkResult) { r.VerifiedAt = linkTestNow.Add(2 * time.Minute) },
		func(r *LinkResult) { r.LaunchURL = "https://bank.example.test/link" },
	} {
		result := linkTestResult(linkTestInput())
		result.Status, result.LaunchURL = "VERIFIED", ""
		result.Account = &Account{Reference: "bankref_synthetic1234", HolderName: "Synthetic Owner", Last4: "1234"}
		result.VerificationID, result.VerifiedAt = "verification_1234", linkTestNow
		mutate(&result)
		linker := newTestLinker(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(result) })
		if got, err := linker.Create(t.Context(), linkTestInput()); !errors.Is(err, ErrRejected) || got.Account != nil {
			t.Fatalf("unsafe proof accepted: %+v %v", got, err)
		}
	}
}

func TestHostedBankLinkRejectsInvalidInputBeforeProviderSideEffects(t *testing.T) {
	var calls atomic.Int32
	linker := newTestLinker(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, mutate := range []func(*LinkRequest){
		func(r *LinkRequest) { r.TenantID = "" },
		func(r *LinkRequest) { r.OwnerID = "00000000-0000-0000-0000-000000000000" },
		func(r *LinkRequest) { r.Country = "in" },
		func(r *LinkRequest) { r.ApplicationRole = "ADMIN" },
		func(r *LinkRequest) { r.AttemptID = "client-token" },
		func(r *LinkRequest) { r.ProviderSession = "session_synthetic_1234" },
	} {
		input := linkTestInput()
		mutate(&input)
		if _, err := linker.Create(t.Context(), input); !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	}
	if _, err := linker.Resolve(t.Context(), linkTestInput()); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached provider")
	}
}

func TestHostedBankLinkNeverFollowsRedirectsLeaksCookiesOrAcceptsRawAccountFields(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	for _, body := range []string{"redirect", "unavailable", "unknown", "oversize", "trailing", "malformed", "forbidden"} {
		t.Run(body, func(t *testing.T) {
			linker := newTestLinker(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cookie") != "" {
					t.Error("cookie leaked")
				}
				switch body {
				case "redirect":
					http.Redirect(w, r, target.URL, 307)
				case "unavailable":
					w.WriteHeader(503)
				case "forbidden":
					w.WriteHeader(403)
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", 16385)))
				case "unknown":
					_, _ = w.Write([]byte(`{"account_number":"123456789012"}`))
				case "trailing":
					_ = json.NewEncoder(w).Encode(linkTestResult(linkTestInput()))
					_, _ = w.Write([]byte(`{}`))
				default:
					_, _ = w.Write([]byte(`{"status":true}`))
				}
			})
			if _, err := linker.Create(t.Context(), linkTestInput()); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	if leaked.Load() != 0 {
		t.Fatal("provider credentials followed a redirect")
	}
	endpoint, _ := url.Parse(target.URL)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(endpoint, []*http.Cookie{{Name: "secret", Value: "private"}})
	linker, err := NewHTTPLinker(LinkerConfig{Endpoint: endpoint, Client: &http.Client{Timeout: time.Second, Jar: jar}, Token: strings.Repeat("s", 32), Clock: time.Now, LaunchOrigins: []string{"https://bank.example.test"}, AllowLoopbackHTTP: true})
	if err != nil || linker.(*HTTPLinker).client.Jar != nil {
		t.Fatal("service client retained a browser cookie jar")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := linker.Create(ctx, linkTestInput()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestHostedBankLinkConfigurationFailsClosed(t *testing.T) {
	for _, endpoint := range []string{"http://bank.example.test", "https://secret@bank.example.test", "https://bank.example.test/path", "https://bank.example.test?secret=x", "https://bank.example.test#fragment"} {
		u, _ := url.Parse(endpoint)
		if _, err := NewHTTPLinker(LinkerConfig{Endpoint: u, Client: &http.Client{Timeout: time.Second}, Token: strings.Repeat("s", 32), Clock: time.Now, LaunchOrigins: []string{"https://bank.example.test"}, AllowLoopbackHTTP: true}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	u, _ := url.Parse("https://provider.example.test")
	for _, origin := range []string{"http://bank.example.test", "https://bank.example.test/", "https://secret@bank.example.test", "https://bank.example.test?secret=x", "https://bank.example.test#fragment", ""} {
		if _, err := NewHTTPLinker(LinkerConfig{Endpoint: u, Client: &http.Client{Timeout: time.Second}, Token: strings.Repeat("s", 32), Clock: time.Now, LaunchOrigins: []string{origin}}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unsafe launch origin accepted: %s", origin)
		}
	}
	linker, err := NewHTTPLinker(LinkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = linker.Create(t.Context(), linkTestInput()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfigured provider manufactured success")
	}
}

func TestHostedBankCancelResolvesUnknownAttemptWithoutCreatingVerification(t *testing.T) {
	linker := newTestLinker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bank-accounts/link-sessions:cancel" {
			t.Fatal("cancellation initiated a new provider verification")
		}
		var input LinkRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.ProviderSession != "" {
			t.Fatal("interrupted cancellation did not preserve its unknown session")
		}
		result := linkTestResult(input)
		result.Status, result.LaunchURL = "CANCELLED", ""
		_ = json.NewEncoder(w).Encode(result)
	})
	if result, err := linker.Cancel(t.Context(), linkTestInput()); err != nil || result.Status != "CANCELLED" {
		t.Fatalf("cancel=%+v err=%v", result, err)
	}
}
