package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProviderResponseBytes = 64 * 1024

type HTTPProviderVerifierConfig struct {
	BaseURL          *url.URL
	Client           *http.Client
	AllowedProviders []string
	Timeout          time.Duration
}

type HTTPProviderVerifier struct {
	endpoint *url.URL
	client   *http.Client
	allowed  map[string]struct{}
	timeout  time.Duration
}

func NewHTTPProviderVerifier(config HTTPProviderVerifierConfig) (*HTTPProviderVerifier, error) {
	if config.BaseURL == nil ||
		!config.BaseURL.IsAbs() ||
		(config.BaseURL.Scheme != "http" && config.BaseURL.Scheme != "https") ||
		config.BaseURL.Host == "" || config.BaseURL.User != nil ||
		config.BaseURL.RawQuery != "" || config.BaseURL.Fragment != "" ||
		config.Timeout <= 0 || config.Timeout > 15*time.Second ||
		len(config.AllowedProviders) == 0 {
		return nil, ErrInvalidConfiguration
	}
	base := *config.BaseURL
	endpoint := base.ResolveReference(&url.URL{Path: "/v1/identity/verify"})
	if endpoint.Scheme != base.Scheme || endpoint.Host != base.Host {
		return nil, ErrInvalidConfiguration
	}
	allowed := make(map[string]struct{}, len(config.AllowedProviders))
	for _, provider := range config.AllowedProviders {
		provider = strings.TrimSpace(provider)
		if !validProvider(provider) {
			return nil, ErrInvalidConfiguration
		}
		allowed[provider] = struct{}{}
	}
	client := config.Client
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          50,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: config.Timeout,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &HTTPProviderVerifier{endpoint: endpoint, client: client, allowed: allowed, timeout: config.Timeout}, nil
}

func (verifier *HTTPProviderVerifier) Verify(ctx context.Context, provider, token string) (ProviderIdentity, error) {
	if _, allowed := verifier.allowed[provider]; !allowed ||
		!validOpaqueToken(token, 8, 8192) {
		return ProviderIdentity{}, ErrProviderRejected
	}
	body, err := json.Marshal(map[string]string{"provider": provider, "token": token})
	if err != nil {
		return ProviderIdentity{}, ErrProviderUnavailable
	}
	requestContext, cancel := context.WithTimeout(ctx, verifier.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, verifier.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ProviderIdentity{}, ErrProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := verifier.client.Do(request)
	if err != nil {
		return ProviderIdentity{}, fmt.Errorf("verify provider identity: %w", ErrProviderUnavailable)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil || len(contents) > maxProviderResponseBytes {
		return ProviderIdentity{}, ErrProviderUnavailable
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ProviderIdentity{}, ErrProviderRejected
	}
	if response.StatusCode != http.StatusOK {
		return ProviderIdentity{}, ErrProviderUnavailable
	}
	var result struct {
		Active   bool     `json:"active"`
		Provider string   `json:"provider"`
		Subject  string   `json:"subject"`
		Roles    []string `json:"roles"`
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ProviderIdentity{}, ErrProviderUnavailable
	}
	if !result.Active || result.Provider != provider || !validSafeIdentifier(result.Subject, 128) {
		return ProviderIdentity{}, ErrProviderRejected
	}
	// Provider roles are deliberately ignored. Platform roles are owned by the
	// identity repository and cannot be granted by an external assertion.
	return ProviderIdentity{Provider: provider, Subject: result.Subject}, nil
}

func (verifier *HTTPProviderVerifier) CloseIdleConnections() {
	if closer, ok := verifier.client.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func validProvider(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && character != '-' {
			return false
		}
	}
	return true
}
