package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

var ErrSessionAuthorityUnavailable = errors.New("session authority unavailable")

// HTTPSessionAuthority checks the owner before forwarding a protected command.
// A valid but revoked JWT is insufficient, and a stale JWT role list is replaced
// by current server-owned roles. No positive cache delays staff/account revocation.
type HTTPSessionAuthority struct {
	endpoint string
	client   *http.Client
}

func NewHTTPSessionAuthority(base *url.URL) (*HTTPSessionAuthority, error) {
	if base == nil || !base.IsAbs() || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Scheme != "http" && base.Scheme != "https" {
		return nil, ErrInvalidConfiguration
	}
	endpoint := base.ResolveReference(&url.URL{Path: "/v1/me"})
	return &HTTPSessionAuthority{endpoint: endpoint.String(), client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (authority *HTTPSessionAuthority) Authenticate(ctx context.Context, principal Principal) (Principal, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, authority.endpoint, nil)
	if err != nil {
		return Principal{}, ErrSessionAuthorityUnavailable
	}
	request.Header.Set("X-Planext4u-Subject", principal.Subject)
	request.Header.Set("X-Planext4u-Session", principal.SessionID)
	request.Header.Set("X-Planext4u-Tenant", principal.TenantID)
	request.Header.Set("X-Planext4u-Country", principal.Country)
	request.Header.Set("Accept", "application/json")
	response, err := authority.client.Do(request)
	if err != nil {
		return Principal{}, ErrSessionAuthorityUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return Principal{}, ErrTokenInvalid
	}
	if response.StatusCode != 200 {
		return Principal{}, ErrSessionAuthorityUnavailable
	}
	var result struct {
		ID      string   `json:"id"`
		Tenant  string   `json:"tenant_id"`
		Country string   `json:"country"`
		Roles   []string `json:"roles"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64*1024))
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Principal{}, ErrSessionAuthorityUnavailable
	}
	if result.ID != principal.Subject || result.Tenant != principal.TenantID || result.Country != principal.Country || len(result.Roles) == 0 || len(result.Roles) > 8 {
		return Principal{}, ErrTokenInvalid
	}
	for _, role := range result.Roles {
		switch role {
		case "CUSTOMER", "VENDOR", "RIDER", "ADMIN":
		default:
			return Principal{}, ErrTokenInvalid
		}
	}
	principal.Roles = append([]string(nil), result.Roles...)
	return principal, nil
}
