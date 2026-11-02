package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestLiveAuthorityRejectsRevokedDisabledMismatchedAndUnavailableSessions(t *testing.T) {
	status := 200
	body := `{"id":"customer-1","tenant_id":"tenant-1","country":"IN","roles":["CUSTOMER"],"profile":{}}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Planext4u-Session") != "session-1" || request.Header.Get("Authorization") != "" {
			t.Error("session evidence missing or provider token forwarded")
		}
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	authority, err := NewHTTPSessionAuthority(base)
	if err != nil {
		t.Fatal(err)
	}
	input := Principal{Subject: "customer-1", SessionID: "session-1", TenantID: "tenant-1", Country: "IN", Roles: []string{"ADMIN"}}
	result, err := authority.Authenticate(context.Background(), input)
	if err != nil || len(result.Roles) != 1 || result.Roles[0] != "CUSTOMER" {
		t.Fatalf("stale authority retained: %+v %v", result, err)
	}
	for _, code := range []int{401, 403} {
		status = code
		if _, err = authority.Authenticate(context.Background(), input); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("inactive session accepted: %v", err)
		}
	}
	status = 503
	if _, err = authority.Authenticate(context.Background(), input); !errors.Is(err, ErrSessionAuthorityUnavailable) {
		t.Fatal(err)
	}
	status = 200
	body = `{"id":"customer-2","tenant_id":"tenant-1","country":"IN","roles":["CUSTOMER"]}`
	if _, err = authority.Authenticate(context.Background(), input); !errors.Is(err, ErrTokenInvalid) {
		t.Fatal("scope mismatch accepted")
	}
	server.Close()
	if _, err = authority.Authenticate(context.Background(), input); !errors.Is(err, ErrSessionAuthorityUnavailable) {
		t.Fatal(err)
	}
}
