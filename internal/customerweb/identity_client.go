package customerweb

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/identity"
)

type HTTPIdentityClientConfig struct {
	BaseURL     *url.URL
	HTTPClient  *http.Client
	GuestSecret []byte
	Clock       func() time.Time
	MaxBytes    int64
}

type HTTPIdentityClient struct {
	baseURL     *url.URL
	httpClient  *http.Client
	guestSecret []byte
	clock       func() time.Time
	maxBytes    int64
}

type IdentityUpstreamError struct {
	Status int
}

func (err *IdentityUpstreamError) Error() string { return "identity service request failed" }

func IdentityErrorIsRetryable(err error) bool {
	var upstream *IdentityUpstreamError
	return errors.As(err, &upstream) && (upstream.Status == 0 || upstream.Status >= http.StatusInternalServerError)
}

func NewHTTPIdentityClient(config HTTPIdentityClientConfig) (*HTTPIdentityClient, error) {
	if config.BaseURL == nil || (config.BaseURL.Scheme != "http" && config.BaseURL.Scheme != "https") || config.BaseURL.Host == "" ||
		config.HTTPClient == nil || len(config.GuestSecret) < 32 || config.Clock == nil {
		return nil, ErrInvalidConfiguration
	}
	maxBytes := config.MaxBytes
	if maxBytes == 0 {
		maxBytes = 1 << 20
	}
	if maxBytes < 1024 || maxBytes > 2<<20 {
		return nil, ErrInvalidConfiguration
	}
	base := *config.BaseURL
	return &HTTPIdentityClient{baseURL: &base, httpClient: config.HTTPClient, guestSecret: append([]byte(nil), config.GuestSecret...), clock: config.Clock, maxBytes: maxBytes}, nil
}

func (client *HTTPIdentityClient) CreateGuest(ctx context.Context, country string) (PlatformSession, error) {
	body, err := json.Marshal(map[string]string{"country": country})
	if err != nil {
		return PlatformSession{}, err
	}
	timestamp := strconv.FormatInt(client.clock().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, client.guestSecret)
	_, _ = mac.Write([]byte("v1\n" + timestamp + "\n"))
	_, _ = mac.Write(body)
	headers := map[string]string{
		"X-P4U-Guest-Timestamp": timestamp,
		"X-P4U-Guest-Signature": "v1=" + hex.EncodeToString(mac.Sum(nil)),
	}
	var guest identity.GuestSession
	if err := client.request(ctx, http.MethodPost, "/internal/v1/customer-guest-sessions", body, headers, &guest); err != nil {
		return PlatformSession{}, err
	}
	return PlatformSession{
		PlatformSession: guest.SessionID, TenantID: guest.TenantID, Country: guest.Country, DisplayName: "Guest",
		Roles: []identity.Role{identity.RoleGuest}, Guest: true, AccessToken: guest.AccessToken, AccessExpiresAt: guest.AccessExpiresAt,
	}, nil
}

func (client *HTTPIdentityClient) Exchange(ctx context.Context, input identity.ExchangeInput) (PlatformSession, error) {
	body, err := json.Marshal(map[string]string{
		"provider": input.Provider, "provider_token": input.ProviderToken, "device_id": input.DeviceID, "country": input.Country,
	})
	if err != nil {
		return PlatformSession{}, err
	}
	var authentication identity.Authentication
	if err := client.request(ctx, http.MethodPost, "/v1/auth/exchange", body, nil, &authentication); err != nil {
		return PlatformSession{}, err
	}
	return PlatformSession{
		PlatformSession: authentication.Session.ID, IdentityID: authentication.IdentityID, TenantID: authentication.TenantID,
		Country: authentication.Country, DisplayName: authentication.Profile.DisplayName, Roles: authentication.Roles,
		AccessToken: authentication.Tokens.AccessToken, AccessExpiresAt: authentication.Tokens.AccessExpiresAt,
		RefreshToken: authentication.Tokens.RefreshToken, RefreshExpiresAt: authentication.Tokens.RefreshExpiresAt,
	}, nil
}

func (client *HTTPIdentityClient) Refresh(ctx context.Context, refreshToken string) (identity.TokenPair, error) {
	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return identity.TokenPair{}, err
	}
	var tokens identity.TokenPair
	if err := client.request(ctx, http.MethodPost, "/v1/auth/refresh", body, nil, &tokens); err != nil {
		return identity.TokenPair{}, err
	}
	return tokens, nil
}

func (client *HTTPIdentityClient) Revoke(ctx context.Context, refreshToken string) error {
	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return err
	}
	return client.request(ctx, http.MethodPost, "/v1/auth/revoke", body, nil, nil)
}

func (client *HTTPIdentityClient) request(ctx context.Context, method, path string, body []byte, headers map[string]string, destination any) error {
	target := client.baseURL.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return &IdentityUpstreamError{Status: 0}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, client.maxBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil || int64(len(responseBody)) > client.maxBytes {
		return &IdentityUpstreamError{Status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &IdentityUpstreamError{Status: response.StatusCode}
	}
	if destination == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return &IdentityUpstreamError{Status: response.StatusCode}
	}
	return nil
}

func RedactIdentityError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if strings.Contains(strings.ToLower(value), "token") {
		return "identity request failed"
	}
	return value
}
