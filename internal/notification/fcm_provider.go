package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const firebaseMessagingScope = "https://www.googleapis.com/auth/firebase.messaging"

type AccessTokenSource interface {
	AccessToken(context.Context, string) (string, error)
}

type DeviceResolver interface {
	Device(context.Context, string, string) (DeviceEndpoint, error)
}

type FCMProvider struct {
	projectID string
	resolver  DeviceResolver
	tokens    AccessTokenSource
	client    *http.Client
	endpoint  string
}

func NewFCMProvider(projectID string, resolver DeviceResolver, tokens AccessTokenSource, client *http.Client) (*FCMProvider, error) {
	return newFCMProvider(projectID, resolver, tokens, client, "https://fcm.googleapis.com")
}

func newFCMProvider(projectID string, resolver DeviceResolver, tokens AccessTokenSource, client *http.Client, endpoint string) (*FCMProvider, error) {
	parsed, err := url.Parse(endpoint)
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{4,29}$`).MatchString(projectID) || resolver == nil || tokens == nil || client == nil || err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, ErrInvalidRequest
	}
	return &FCMProvider{projectID: projectID, resolver: resolver, tokens: tokens, client: client, endpoint: strings.TrimRight(endpoint, "/")}, nil
}

func (provider *FCMProvider) Send(ctx context.Context, message ProviderMessage) (ProviderReceipt, error) {
	if !uuidPattern.MatchString(message.DeliveryID) || !uuidPattern.MatchString(message.TenantID) || !safeID(message.RecipientRef) || len(message.Subject) > 240 || len(message.Body) == 0 || len(message.Body) > 32_000 || len(message.Data) > 20 {
		return ProviderReceipt{}, &ProviderError{Code: "INVALID_MESSAGE"}
	}
	endpoint, err := provider.resolver.Device(ctx, message.TenantID, message.RecipientRef)
	if err != nil || !endpoint.Enabled || endpoint.Token == "" {
		return ProviderReceipt{}, &ProviderError{Code: "INVALID_RECIPIENT"}
	}
	for key, value := range message.Data {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(key) || len(value) > 2000 || strings.ContainsRune(value, '\x00') {
			return ProviderReceipt{}, &ProviderError{Code: "INVALID_MESSAGE"}
		}
	}
	accessToken, err := provider.tokens.AccessToken(ctx, firebaseMessagingScope)
	accessToken = strings.TrimSpace(accessToken)
	if err != nil || len(accessToken) < 20 || len(accessToken) > 8192 || strings.ContainsAny(accessToken, "\r\n") {
		return ProviderReceipt{}, &ProviderError{Code: "AUTH_UNAVAILABLE", Retryable: true}
	}
	payload := map[string]any{"message": map[string]any{
		"token":        endpoint.Token,
		"notification": map[string]string{"title": message.Subject, "body": message.Body},
		"data":         cloneStringMap(message.Data),
	}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ProviderReceipt{}, &ProviderError{Code: "INVALID_MESSAGE"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint+"/v1/projects/"+url.PathEscape(provider.projectID)+"/messages:send", bytes.NewReader(encoded))
	if err != nil {
		return ProviderReceipt{}, &ProviderError{Code: "PROVIDER_UNAVAILABLE", Retryable: true}
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		return ProviderReceipt{}, &ProviderError{Code: "PROVIDER_UNAVAILABLE", Retryable: true}
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if readErr != nil || len(body) > 64*1024 {
		return ProviderReceipt{}, &ProviderError{Code: "PROVIDER_INVALID_RESPONSE", Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ProviderReceipt{}, fcmHTTPError(response.StatusCode)
	}
	var result struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &result) != nil || strings.TrimSpace(result.Name) == "" || len(result.Name) > 1024 || strings.ContainsAny(result.Name, "\r\n") {
		return ProviderReceipt{}, &ProviderError{Code: "PROVIDER_INVALID_RESPONSE", Retryable: true}
	}
	return ProviderReceipt{ProviderMessageID: result.Name, Status: DeliverySent, OccurredAt: time.Now().UTC()}, nil
}

func fcmHTTPError(status int) error {
	switch {
	case status == http.StatusTooManyRequests:
		return &ProviderError{Code: "RATE_LIMITED", Retryable: true}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &ProviderError{Code: "PROVIDER_AUTH_FAILED", Retryable: true}
	case status == http.StatusNotFound:
		return &ProviderError{Code: "INVALID_RECIPIENT"}
	case status >= 500:
		return &ProviderError{Code: "PROVIDER_UNAVAILABLE", Retryable: true}
	case status >= 400:
		return &ProviderError{Code: "PROVIDER_REJECTED"}
	default:
		return errors.New("unexpected FCM status")
	}
}
