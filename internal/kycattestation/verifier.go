// Package kycattestation resolves independent, provider-owned identity document
// decisions. Private media readiness and a human viewing a document are not
// attestations and cannot satisfy this boundary.
package kycattestation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrRejected    = errors.New("identity document attestation rejected")
	ErrUnavailable = errors.New("identity document attestation unavailable")
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	kindPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
)

type Scope struct {
	TenantID string `json:"tenant_id"`
	Country  string `json:"country"`
	OwnerID  string `json:"owner_id"`
}

type Document struct {
	Kind    string `json:"kind"`
	AssetID string `json:"asset_id"`
}

type Verifier interface {
	Verify(context.Context, Scope, []Document) (string, error)
}

// VerifyFunc exists for synthetic tests. Runtime configuration uses only the
// authenticated HTTP verifier or the fail-closed unavailable verifier.
type VerifyFunc func(context.Context, Scope, []Document) (string, error)

func (f VerifyFunc) Verify(ctx context.Context, scope Scope, documents []Document) (string, error) {
	return f(ctx, scope, documents)
}

type unavailable struct{}

func (unavailable) Verify(context.Context, Scope, []Document) (string, error) {
	return "", ErrUnavailable
}

func Unavailable() Verifier { return unavailable{} }

type HTTPVerifier struct {
	endpoint url.URL
	client   *http.Client
	token    string
	clock    func() time.Time
}

func NewHTTPVerifier(endpoint *url.URL, client *http.Client, token string, clock func() time.Time) (Verifier, error) {
	if endpoint == nil && token == "" {
		return Unavailable(), nil
	}
	if endpoint == nil || client == nil || clock == nil || client.Timeout <= 0 || client.Timeout > 10*time.Second || len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") || strings.TrimSpace(token) != token ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, ErrUnavailable
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPVerifier{endpoint: *endpoint, client: &copyClient, token: token, clock: clock}, nil
}

type proof struct {
	Scope
	Purpose       string     `json:"purpose"`
	Documents     []Document `json:"documents"`
	AttestationID string     `json:"attestation_id"`
	Status        string     `json:"status"`
	VerifiedAt    time.Time  `json:"verified_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
}

func validDocuments(scope Scope, documents []Document) bool {
	if !canonicalUUID(scope.TenantID) || !canonicalUUID(scope.OwnerID) || !countryPattern.MatchString(scope.Country) || len(documents) == 0 || len(documents) > 16 {
		return false
	}
	kinds, assets := make(map[string]bool, len(documents)), make(map[string]bool, len(documents))
	for _, document := range documents {
		if !kindPattern.MatchString(document.Kind) || !canonicalUUID(document.AssetID) || kinds[document.Kind] || assets[document.AssetID] {
			return false
		}
		kinds[document.Kind], assets[document.AssetID] = true, true
	}
	return true
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func canonical(documents []Document) []Document {
	copyDocuments := append([]Document(nil), documents...)
	sort.Slice(copyDocuments, func(i, j int) bool { return copyDocuments[i].Kind < copyDocuments[j].Kind })
	return copyDocuments
}

func (v *HTTPVerifier) Verify(ctx context.Context, scope Scope, documents []Document) (string, error) {
	if ctx == nil || !validDocuments(scope, documents) {
		return "", ErrRejected
	}
	ordered := canonical(documents)
	destination := v.endpoint
	destination.Path = "/v1/identity-documents/verification:resolve"
	body, _ := json.Marshal(struct {
		Scope
		Purpose   string     `json:"purpose"`
		Documents []Document `json:"documents"`
	}{scope, "ONBOARDING_KYC", ordered})
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
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnprocessableEntity {
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
	if !validDocuments(result.Scope, result.Documents) || result.Scope != scope || result.Purpose != "ONBOARDING_KYC" || result.Status != "VERIFIED" || !idPattern.MatchString(result.AttestationID) || len(result.Documents) != len(ordered) {
		return "", ErrRejected
	}
	for index, document := range canonical(result.Documents) {
		if document != ordered[index] {
			return "", ErrRejected
		}
	}
	now := v.clock().UTC()
	if result.VerifiedAt.Before(now.Add(-15*time.Minute)) || result.VerifiedAt.After(now.Add(time.Minute)) || !result.ExpiresAt.After(now) || !result.ExpiresAt.After(result.VerifiedAt) || result.ExpiresAt.After(now.Add(24*time.Hour)) {
		return "", ErrRejected
	}
	return result.AttestationID, nil
}
