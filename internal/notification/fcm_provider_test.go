package notification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFCMProviderUsesResolvedTokenAndDeepLinkWithoutLeaks(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository()
	endpoint := DeviceEndpoint{ID: "endpoint-001", TenantID: testTenant, Country: "IN", SubjectID: "customer-001", DeviceReference: "device-001", Platform: DeviceAndroid, Locale: "en", Enabled: true, Token: "fcm_token_secret_0000000000001"}
	_ = repository.SaveDevice(context.Background(), endpoint)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/projects/planext4u-staging/messages:send" || request.Header.Get("Authorization") != "Bearer oauth_access_token_0000000000001" {
			t.Fatalf("request = %s headers=%v", request.URL.Path, request.Header)
		}
		var payload map[string]any
		if json.NewDecoder(request.Body).Decode(&payload) != nil {
			t.Fatal("invalid provider payload")
		}
		message := payload["message"].(map[string]any)
		if message["token"] != endpoint.Token || message["data"].(map[string]any)["deep_link"] != "/app/orders/order-001" {
			t.Fatalf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"name":"projects/planext4u-staging/messages/message-001"}`))
	}))
	defer server.Close()
	provider, err := newFCMProvider("planext4u-staging", repository, staticAccessToken("oauth_access_token_0000000000001"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := provider.Send(context.Background(), ProviderMessage{DeliveryID: "00000000-0000-4000-8000-000000000001", TenantID: testTenant, RecipientRef: endpoint.ID, Subject: "Order update", Body: "Your order was placed.", Data: map[string]string{"deep_link": "/app/orders/order-001"}})
	if err != nil || receipt.Status != DeliverySent || !strings.Contains(receipt.ProviderMessageID, "message-001") {
		t.Fatalf("receipt = %#v err=%v", receipt, err)
	}
}

func TestFCMProviderClassifiesRetryAndNeverReturnsProviderToken(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository()
	endpoint := DeviceEndpoint{ID: "endpoint-001", TenantID: testTenant, Country: "IN", SubjectID: "customer-001", DeviceReference: "device-001", Platform: DeviceAndroid, Locale: "en", Enabled: true, Token: "fcm_token_secret_0000000000001"}
	_ = repository.SaveDevice(context.Background(), endpoint)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, endpoint.Token, http.StatusTooManyRequests)
	}))
	defer server.Close()
	provider, _ := newFCMProvider("planext4u-staging", repository, staticAccessToken("oauth_access_token_0000000000001"), server.Client(), server.URL)
	_, err := provider.Send(context.Background(), ProviderMessage{DeliveryID: "00000000-0000-4000-8000-000000000001", TenantID: testTenant, RecipientRef: endpoint.ID, Body: "Order update"})
	providerErr := &ProviderError{}
	if !errors.As(err, &providerErr) || !providerErr.Retryable || strings.Contains(err.Error(), endpoint.Token) {
		t.Fatalf("provider error = %v", err)
	}
}

type staticAccessToken string

func (value staticAccessToken) AccessToken(context.Context, string) (string, error) {
	return string(value), nil
}
