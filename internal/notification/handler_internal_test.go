package notification

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/order"
)

func TestInternalOrderNotificationRequiresValidSignature(t *testing.T) {
	t.Parallel()
	secret := []byte("order-notification-hmac-test-key-material")
	service, err := NewService(NewMemoryRepository(), map[Channel]Provider{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithInternalOrderNotifications(service, secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(order.Notification{ID: "notification-001", TenantID: testTenant, Country: "IN", CustomerID: "customer-1", OrderID: "order-1", Status: order.StatusPlaced, Revision: 1, CreatedAt: time.Now().UTC()})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/order-notifications", bytes.NewReader(body))
	request.Header.Set("X-Planext4u-Internal-Signature", "invalid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status=%d", response.Code)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	request = httptest.NewRequest(http.MethodPost, "/internal/v1/order-notifications", bytes.NewReader(body))
	request.Header.Set("X-Planext4u-Internal-Signature", hex.EncodeToString(mac.Sum(nil)))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid signature status=%d body=%s", response.Code, response.Body.String())
	}
}
