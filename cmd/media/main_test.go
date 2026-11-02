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

func TestPublicS3EndpointIsExplicitRootAndRequiresTLSInProduction(t *testing.T) {
	values := map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://media:local@db.example/p4u?sslmode=verify-full", "AWS_REGION": "ap-south-1", "MEDIA_BUCKET": "p4u-test-private", "MALWARE_SCANNER_MODE": "clamav", "CLAMAV_SOCKET": "/run/clamav/clamd.sock", "S3_ENDPOINT": "https://internal.example", "S3_PUBLIC_ENDPOINT": "https://uploads.example"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.s3PublicEndpoint.Host != "uploads.example" {
		t.Fatalf("public signing endpoint rejected: %v", err)
	}
	for _, value := range []string{"http://uploads.example", "https://uploads.example/prefix", "https://user:password@uploads.example", "https://uploads.example?host=other", "https://uploads.example#fragment"} {
		values["S3_PUBLIC_ENDPOINT"] = value
		if _, err := loadRuntimeConfig(lookup); err == nil {
			t.Fatalf("unsafe public endpoint accepted: %s", value)
		}
	}
	values["S3_PUBLIC_ENDPOINT"] = "https://uploads.example"
	delete(values, "S3_ENDPOINT")
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("implicit private/public store split accepted")
	}
}

func TestClamAVRuntimeRequiresPrivateSocketAndUnambiguousMode(t *testing.T) {
	values := map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://media:local@db.example/p4u?sslmode=verify-full", "AWS_REGION": "ap-south-1", "MEDIA_BUCKET": "p4u-test-private", "MALWARE_SCANNER_MODE": "clamav", "CLAMAV_SOCKET": "/run/clamav/clamd.sock"}
	lookup := func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.scannerMode != "clamav" {
		t.Fatalf("private scanner configuration rejected: %v", err)
	}
	for _, socket := range []string{"", "tcp://scanner:3310", "relative", "/run/../clam.sock"} {
		values["CLAMAV_SOCKET"] = socket
		if _, err = loadRuntimeConfig(lookup); err == nil {
			t.Fatal("unsafe scanner socket accepted")
		}
	}
	values["CLAMAV_SOCKET"] = "/run/clamav/clamd.sock"
	values["MALWARE_SCANNER_TOKEN_FILE"] = "/private/unrelated-token"
	if _, err = loadRuntimeConfig(lookup); err == nil {
		t.Fatal("ambiguous scanner credentials accepted")
	}
	delete(values, "MALWARE_SCANNER_TOKEN_FILE")
	for _, age := range []string{"garbage", "0s", "59m", "169h"} {
		values["CLAMAV_MAX_SIGNATURE_AGE"] = age
		if _, err = loadRuntimeConfig(lookup); err == nil {
			t.Fatal("invalid signature age accepted")
		}
	}
	values["CLAMAV_MAX_SIGNATURE_AGE"] = "36h"
	config, err = loadRuntimeConfig(lookup)
	if err != nil || config.clamMaxSignatureAge != 36*time.Hour {
		t.Fatalf("signature policy rejected: %v", err)
	}
	delete(values, "CLAMAV_MAX_SIGNATURE_AGE")
	values["MALWARE_SCANNER_MODE"] = "untrusted"
	if _, err = loadRuntimeConfig(lookup); err == nil {
		t.Fatal("unknown scanner mode accepted")
	}
}

func TestMediaRoutesAreBounded(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"/v1/media/uploads":                "/v1/media/uploads",
		"/v1/media/presentations:resolve":  "/v1/media/presentations:resolve",
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
