package main

import (
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestNotificationRouteNamesPreferenceDimensions(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest("GET", "/v1/notification/preferences/MARKETING/PUSH", nil)
	if route := notificationRoute(request); route != "/v1/notification/preferences/{purpose}/{channel}" {
		t.Fatalf("route = %q", route)
	}
}

func TestNotificationRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":                          "production",
		"DATABASE_URL":                     "postgres://notification:secret@database.example/p4u?sslmode=verify-full",
		"DEVICE_TOKEN_KEY_FILE":            "/run/secrets/device-token-key",
		"FIREBASE_SERVICE_ACCOUNT_FILE":    "/run/secrets/firebase.json",
		"ORDER_NOTIFICATION_HMAC_KEY_FILE": "/run/secrets/order-notification-key",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-notification" || config.service.HTTPAddress != ":8089" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	delete(values, "FIREBASE_SERVICE_ACCOUNT_FILE")
	if _, err := loadRuntimeConfig(lookup); err == nil || !strings.Contains(err.Error(), "Firebase") {
		t.Fatalf("missing Firebase error=%v", err)
	}
}

func TestNotificationDeviceTokenKeyIsStrictBase64(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := loadDeviceTokenKey(path); err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
}

func TestNotificationDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		service:          platformconfig.Config{Environment: platformconfig.EnvironmentProduction},
		databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://notification:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}
