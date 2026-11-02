package privateevidence

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// AccessGrant is a short-lived, owner-scoped download capability. Callers must
// never log or persist URL; it contains the object-store signature.
type AccessGrant struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AccessProvider is deliberately separate from Verifier so test verifiers and
// deployments without an actual private media service fail closed.
type AccessProvider interface {
	AccessIdentityDocument(context.Context, Scope, string) (AccessGrant, error)
}

func (v *HTTPVerifier) AccessIdentityDocument(ctx context.Context, scope Scope, id string) (AccessGrant, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || !validAccessScope(scope) {
		return AccessGrant{}, ErrRejected
	}
	id = parsed.String()
	if err := v.VerifyIdentityDocuments(ctx, scope, []string{id}); err != nil {
		return AccessGrant{}, err
	}
	destination := v.endpoint
	destination.Path = "/v1/media/" + id + "/access"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.String(), nil)
	if err != nil {
		return AccessGrant{}, ErrUnavailable
	}
	request.Header.Set("X-Planext4u-Tenant", scope.TenantID)
	request.Header.Set("X-Planext4u-Country", scope.Country)
	request.Header.Set("X-Planext4u-Subject", scope.OwnerID)
	request.Header.Set("Accept", "application/json")
	response, err := v.client.Do(request)
	if err != nil {
		return AccessGrant{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusConflict {
		return AccessGrant{}, ErrRejected
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(data) > 16384 || response.StatusCode != http.StatusOK {
		return AccessGrant{}, ErrUnavailable
	}
	var grant struct {
		AssetID   string    `json:"asset_id"`
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(data, &grant) != nil || grant.AssetID != id || !validAccessGrant(grant.URL, grant.ExpiresAt) {
		return AccessGrant{}, ErrUnavailable
	}
	return AccessGrant{URL: grant.URL, ExpiresAt: grant.ExpiresAt}, nil
}

func validAccessScope(scope Scope) bool {
	return uuid.Validate(scope.TenantID) == nil && uuid.Validate(scope.OwnerID) == nil && len(scope.Country) == 2 && scope.Country[0] >= 'A' && scope.Country[0] <= 'Z' && scope.Country[1] >= 'A' && scope.Country[1] <= 'Z'
}

func validAccessGrant(raw string, expires time.Time) bool {
	if raw == "" || len(raw) > 8192 || expires.IsZero() {
		return false
	}
	now := time.Now().UTC()
	if !expires.After(now) || expires.After(now.Add(2*time.Minute+5*time.Second)) {
		return false
	}
	destination, err := url.Parse(raw)
	if err != nil || destination.Host == "" || destination.User != nil || destination.Fragment != "" || destination.RawQuery == "" {
		return false
	}
	if destination.Scheme == "https" {
		return true
	}
	if destination.Scheme != "http" {
		return false
	}
	return destination.Hostname() == "localhost" || net.ParseIP(destination.Hostname()) != nil && net.ParseIP(destination.Hostname()).IsLoopback()
}
