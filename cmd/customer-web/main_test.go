package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoadRuntimeConfigRequiresBFFSecretsAndHTTPSOrigin(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://runtime@database/customer", "CUSTOMER_WEB_ALLOWED_ORIGINS": "https://customer.example",
		"IDENTITY_BASE_URL": "http://identity:8080", "PLATFORM_GATEWAY_URL": "http://gateway:8080",
		"GUEST_SESSION_HMAC_KEY_FILE": "/run/secrets/guest", "CUSTOMER_WEB_TOKEN_KEY_FILE": "/run/secrets/tokens",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.service.ServiceName != "planext4u-customer-web" || config.service.HTTPAddress != ":8091" || len(config.allowedOrigins) != 1 {
		t.Fatalf("config = %#v", config)
	}
	values["CUSTOMER_WEB_ALLOWED_ORIGINS"] = "http://customer.example"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("insecure browser origin must be rejected")
	}
	values["CUSTOMER_WEB_ALLOWED_ORIGINS"] = "https://customer.example"
	delete(values, "CUSTOMER_WEB_TOKEN_KEY_FILE")
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("missing token-encryption key must be rejected")
	}
}

func TestDecodeTokenKeyAcceptsRawAndBase64EncodedKey(t *testing.T) {
	raw := []byte(strings.Repeat("k", 32))
	for name, input := range map[string][]byte{
		"raw":    raw,
		"base64": []byte(base64.StdEncoding.EncodeToString(raw)),
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decodeTokenKey(input)
			if err != nil || string(decoded) != string(raw) {
				t.Fatalf("decoded=%q err=%v", decoded, err)
			}
		})
	}
	if _, err := decodeTokenKey([]byte("short")); err == nil {
		t.Fatal("short token key must be rejected")
	}
}
