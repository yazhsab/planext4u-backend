// Package bankverification resolves provider-owned bank references through a
// configured trusted service. It never collects account numbers or infers bank
// verification from an administrator's approval or a client-supplied token.
package bankverification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrRejected    = errors.New("bank verification rejected")
	ErrUnavailable = errors.New("bank verification unavailable")
)

type Scope struct {
	TenantID string `json:"tenant_id"`
	Country  string `json:"country"`
	OwnerID  string `json:"owner_id"`
}
type Account struct {
	Reference  string `json:"reference"`
	HolderName string `json:"holder_name,omitempty"`
	Last4      string `json:"last4,omitempty"`
	IFSC       string `json:"ifsc,omitempty"`
}
type Verifier interface {
	Verify(context.Context, Scope, Account) (string, error)
}
type VerifyFunc func(context.Context, Scope, Account) (string, error)

func (f VerifyFunc) Verify(ctx context.Context, scope Scope, account Account) (string, error) {
	return f(ctx, scope, account)
}

type unavailable struct{}

func (unavailable) Verify(context.Context, Scope, Account) (string, error) { return "", ErrUnavailable }
func Unavailable() Verifier                                                { return unavailable{} }

type HTTPVerifier struct {
	endpoint url.URL
	client   *http.Client
	token    string
	clock    func() time.Time
}

// TLS/deployment destinations are validated by the runtime. The configured
// service must resolve existing provider verification records, without creating
// bank accounts, initiating transfers, or charging for a new verification.
func NewHTTPVerifier(endpoint *url.URL, client *http.Client, token string, clock func() time.Time) (Verifier, error) {
	if endpoint == nil && token == "" {
		return Unavailable(), nil
	}
	if endpoint == nil || client == nil || clock == nil || client.Timeout <= 0 || client.Timeout > 10*time.Second || len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") || strings.TrimSpace(token) != token ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, ErrUnavailable
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPVerifier{endpoint: *endpoint, client: &copyClient, token: token, clock: clock}, nil
}

type proof struct {
	Scope
	Account
	VerificationID string    `json:"verification_id"`
	Status         string    `json:"status"`
	VerifiedAt     time.Time `json:"verified_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (v *HTTPVerifier) Verify(ctx context.Context, scope Scope, account Account) (string, error) {
	if uuid.Validate(scope.TenantID) != nil || uuid.Validate(scope.OwnerID) != nil || !countryPattern.MatchString(scope.Country) || !referencePattern.MatchString(account.Reference) {
		return "", ErrRejected
	}
	destination := v.endpoint
	destination.Path = "/v1/bank-accounts/verification:resolve"
	body, _ := json.Marshal(struct {
		Scope
		Account
	}{scope, account})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.String(), bytes.NewReader(body))
	if err != nil {
		return "", ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+v.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := v.client.Do(request)
	if err != nil {
		return "", ErrUnavailable
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden {
		return "", ErrRejected
	}
	if err != nil || len(contents) > 32768 || response.StatusCode != http.StatusOK {
		return "", ErrUnavailable
	}
	var result proof
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", ErrUnavailable
	}
	now := v.clock().UTC()
	if result.Scope != scope || result.Reference != account.Reference || result.Status != "VERIFIED" ||
		!verificationPattern.MatchString(result.VerificationID) ||
		result.VerifiedAt.Before(now.Add(-15*time.Minute)) || result.VerifiedAt.After(now.Add(time.Minute)) ||
		!result.ExpiresAt.After(now) || !result.ExpiresAt.After(result.VerifiedAt) || result.ExpiresAt.After(now.Add(24*time.Hour)) ||
		len(strings.TrimSpace(result.HolderName)) < 3 || len(result.HolderName) > 200 || !last4Pattern.MatchString(result.Last4) ||
		(account.HolderName != "" && !strings.EqualFold(strings.TrimSpace(account.HolderName), strings.TrimSpace(result.HolderName))) ||
		(account.Last4 != "" && result.Last4 != account.Last4) || (account.IFSC != "" && result.IFSC != account.IFSC) {
		return "", ErrRejected
	}
	return result.VerificationID, nil
}
