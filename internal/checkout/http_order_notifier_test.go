package checkout

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/order"
)

func TestHTTPOrderNotifierSignsExactPayload(t *testing.T) {
	t.Parallel()
	secret := []byte("order-notification-hmac-test-key-material")
	value := order.Notification{ID: "notification-001", TenantID: "afc1e0db-73cf-40b3-9927-33590133da0b", Country: "IN", CustomerID: "2da29782-f277-40ba-a526-d153be421243", OrderID: "3fb914af-d76b-4758-af66-3475f18a3942", Status: order.StatusPlaced, Revision: 1, CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var decoded order.Notification
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(decoded)
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(body)
		if request.URL.Path != "/internal/v1/order-notifications" || request.Header.Get("X-Planext4u-Internal-Signature") != hex.EncodeToString(mac.Sum(nil)) || decoded.ID != value.ID {
			t.Errorf("request path=%s notification=%#v", request.URL.Path, decoded)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	notifier, err := NewHTTPOrderNotifier(HTTPOrderNotifierConfig{BaseURL: base, Secret: secret, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Send(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}
