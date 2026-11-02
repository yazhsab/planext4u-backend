package media

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPMalwareScannerAuthenticatesAndValidatesResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer scanner-token-safe" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"clean":true,"reason_code":""}`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/v1/media/scan")
	scanner, err := NewHTTPMalwareScanner(endpoint, &http.Client{Timeout: time.Second}, "scanner-token-safe")
	if err != nil {
		t.Fatal(err)
	}
	result, err := scanner.Scan(t.Context(), "tenants/tenant/owners/owner/media/asset")
	if err != nil || !result.Clean {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestHTTPMalwareScannerNeverFollowsRedirectsWithPrivateScope(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = writer.Write([]byte(`{"clean":true,"reason_code":""}`))
	}))
	defer target.Close()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, target.URL, status)
		}))
		endpoint, _ := url.Parse(server.URL)
		scanner, _ := NewHTTPMalwareScanner(endpoint, &http.Client{Timeout: time.Second}, "scanner-token-safe")
		_, err := scanner.Scan(t.Context(), "tenants/tenant/owners/owner/media/asset")
		server.Close()
		if err == nil || received.Load() != 0 {
			t.Fatalf("private scan followed redirect: count=%d err=%v", received.Load(), err)
		}
	}
}

func TestHTTPMalwareScannerRejectsMalformedOrUnsafeResponses(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"clean":false,"reason_code":"unsafe reason"}`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	scanner, _ := NewHTTPMalwareScanner(endpoint, &http.Client{Timeout: time.Second}, "scanner-token-safe")
	if _, err := scanner.Scan(t.Context(), "tenants/tenant/owners/owner/media/asset"); err == nil || !strings.Contains(err.Error(), "invalid result") {
		t.Fatalf("response validation error=%v", err)
	}
}
