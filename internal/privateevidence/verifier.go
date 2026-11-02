// Package privateevidence verifies opaque KYC references with their owning media
// service. A UUID supplied by a participant is never evidence of ownership.
package privateevidence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var (
	ErrRejected    = errors.New("private identity evidence rejected")
	ErrUnavailable = errors.New("private evidence verification unavailable")
)

type Scope struct{ TenantID, Country, OwnerID string }
type Verifier interface {
	VerifyIdentityDocuments(context.Context, Scope, []string) error
}
type VerifyFunc func(context.Context, Scope, []string) error

func (f VerifyFunc) VerifyIdentityDocuments(ctx context.Context, scope Scope, ids []string) error {
	return f(ctx, scope, ids)
}

type unavailable struct{}

func (unavailable) VerifyIdentityDocuments(context.Context, Scope, []string) error {
	return ErrUnavailable
}
func Unavailable() Verifier { return unavailable{} }

type HTTPVerifier struct {
	endpoint url.URL
	client   *http.Client
}

// The deployment config validates the trusted service destination. Redirects
// are refused so scope headers cannot escape to a different server.
func NewHTTPVerifier(endpoint *url.URL, client *http.Client) (Verifier, error) {
	if endpoint == nil {
		return Unavailable(), nil
	}
	if client == nil || client.Timeout <= 0 || client.Timeout > 10*time.Second ||
		(endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, ErrUnavailable
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPVerifier{endpoint: *endpoint, client: &copyClient}, nil
}

func (v *HTTPVerifier) VerifyIdentityDocuments(ctx context.Context, scope Scope, ids []string) error {
	if uuid.Validate(scope.TenantID) != nil || uuid.Validate(scope.OwnerID) != nil || !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(scope.Country) || len(ids) < 1 || len(ids) > 10 {
		return ErrRejected
	}
	seen := map[string]bool{}
	canonical := make([]string, 0, len(ids))
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil || seen[parsed.String()] {
			return ErrRejected
		}
		seen[parsed.String()] = true
		canonical = append(canonical, parsed.String())
	}
	for _, id := range canonical {
		destination := v.endpoint
		destination.Path = "/v1/media/" + id
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.String(), nil)
		if err != nil {
			return ErrUnavailable
		}
		request.Header.Set("X-Planext4u-Tenant", scope.TenantID)
		request.Header.Set("X-Planext4u-Country", scope.Country)
		request.Header.Set("X-Planext4u-Subject", scope.OwnerID)
		request.Header.Set("Accept", "application/json")
		response, err := v.client.Do(request)
		if err != nil {
			return ErrUnavailable
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 32769))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
			return ErrRejected
		}
		if readErr != nil || len(body) > 32768 || response.StatusCode != http.StatusOK {
			return ErrUnavailable
		}
		var asset struct {
			ID      string `json:"id"`
			Purpose string `json:"purpose"`
			State   string `json:"state"`
		}
		if json.Unmarshal(body, &asset) != nil {
			return ErrUnavailable
		}
		if asset.ID != id || asset.Purpose != "IDENTITY_DOCUMENT" || asset.State != "READY" {
			return ErrRejected
		}
	}
	return nil
}
