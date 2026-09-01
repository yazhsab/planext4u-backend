package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestMediaRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":                    "production",
		"DATABASE_URL":               "postgres://media:secret@database.example/p4u?sslmode=verify-full",
		"AWS_REGION":                 "ap-south-1",
		"MEDIA_BUCKET":               "planext4u-media-production",
		"MALWARE_SCANNER_URL":        "https://scanner.example/v1/scan",
		"MALWARE_SCANNER_TOKEN_FILE": "/run/secrets/scanner-token",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-media" || config.service.HTTPAddress != ":8087" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	values["S3_ENDPOINT"] = "http://object-store.example"
	if _, err := loadRuntimeConfig(lookup); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("plain object endpoint error=%v", err)
	}
	delete(values, "S3_ENDPOINT")
	values["S3_ACCESS_KEY_ID_FILE"] = "/run/secrets/access"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("incomplete static credentials were accepted")
	}
}

func TestMediaDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		service:          platformconfig.Config{Environment: platformconfig.EnvironmentProduction},
		databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://media:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}

func TestMediaRoutesAreBounded(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"/v1/media/uploads":                "/v1/media/uploads",
		"/v1/media/asset-1":                "/v1/media/{asset_id}",
		"/v1/media/asset-1/complete":       "/v1/media/{asset_id}/complete",
		"/v1/media/asset-1/complete/extra": "unmatched",
	}
	for path, expected := range tests {
		request := httptest.NewRequest("GET", path, nil)
		if route := mediaRoute(request); route != expected {
			t.Fatalf("path=%s route=%s", path, route)
		}
	}
}
