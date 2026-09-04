package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRuntimeConfigAcceptsSafeDevelopmentValues(t *testing.T) {
	t.Parallel()

	values := validRuntimeValues()
	values["REQUEST_TIMEOUT"] = "4s"
	values["SHUTDOWN_TIMEOUT"] = "8s"
	values["MAX_REQUEST_BYTES"] = "2048"
	values["IP_RATE_LIMIT"] = "40"
	values["GUEST_RATE_LIMIT"] = "12"
	values["PRINCIPAL_RATE_LIMIT"] = "20"
	values["RATE_WINDOW"] = "30s"
	values["UPSTREAM_ROUTES"] = `{"/v1/auth":"http://127.0.0.1:8083","/v1/catalog":"http://127.0.0.1:8085"}`
	values["ANONYMOUS_ROUTES"] = `{"/v1/provider/notification-receipts":["POST"]}`
	config, err := loadRuntimeConfig(mapLookup(values))
	if err != nil {
		t.Fatalf("loadRuntimeConfig() error = %v", err)
	}
	if config.environment != "development" ||
		config.httpAddress != ":8082" ||
		config.upstreamURL.String() != "http://127.0.0.1:8090" ||
		config.requestTimeout != 4*time.Second ||
		config.shutdownTimeout != 8*time.Second ||
		config.maxRequestBytes != 2048 ||
		config.IPRateLimit != 40 ||
		config.guestRateLimit != 12 ||
		config.principalRateLimit != 20 ||
		config.rateWindow != 30*time.Second {
		t.Fatalf("config = %#v", config)
	}
	if len(config.upstreamRoutes) != 2 || config.upstreamRoutes["/v1/auth"].String() != "http://127.0.0.1:8083" {
		t.Fatalf("upstream routes = %#v", config.upstreamRoutes)
	}
	if _, ok := config.anonymousRoutes["/v1/provider/notification-receipts"]["POST"]; !ok {
		t.Fatalf("anonymous routes = %#v", config.anonymousRoutes)
	}
}

func TestLoadRuntimeConfigAcceptsPrivateServiceDiscoveryHTTPInProduction(t *testing.T) {
	t.Parallel()

	values := validRuntimeValues()
	values["APP_ENV"] = "production"
	values["UPSTREAM_URL"] = "http://transaction:8086"
	values["UPSTREAM_ROUTES"] = `{"/v1/auth":"http://identity:8083","/v1/catalog":"http://catalog.production.internal:8085"}`
	values["JWT_ISSUER"] = "https://identity.planext4u.example"
	config, err := loadRuntimeConfig(mapLookup(values))
	if err != nil {
		t.Fatalf("loadRuntimeConfig() error = %v", err)
	}
	if config.upstreamURL.Host != "transaction:8086" || len(config.upstreamRoutes) != 2 {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadRuntimeConfigRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{
			name: "missing issuer",
			mutate: func(values map[string]string) {
				delete(values, "JWT_ISSUER")
			},
			wantErr: "required",
		},
		{
			name: "staging HTTP upstream",
			mutate: func(values map[string]string) {
				values["APP_ENV"] = "staging"
				values["JWT_ISSUER"] = "https://identity.staging.planext4u.net"
			},
			wantErr: "HTTPS",
		},
		{
			name: "staging public HTTP route",
			mutate: func(values map[string]string) {
				values["APP_ENV"] = "staging"
				values["UPSTREAM_URL"] = "https://service.staging.planext4u.net"
				values["UPSTREAM_ROUTES"] = `{"/v1/catalog":"http://catalog.example.com:8085"}`
				values["JWT_ISSUER"] = "https://identity.staging.planext4u.net"
			},
			wantErr: "HTTPS",
		},
		{
			name: "invalid route prefix",
			mutate: func(values map[string]string) {
				values["UPSTREAM_ROUTES"] = `{"v1/catalog":"http://127.0.0.1:8085"}`
			},
			wantErr: "path prefix",
		},
		{
			name: "unsafe anonymous method",
			mutate: func(values map[string]string) {
				values["ANONYMOUS_ROUTES"] = `{"/v1/catalog/items":["GET"]}`
			},
			wantErr: "only permits POST",
		},
		{
			name: "staging HTTP issuer",
			mutate: func(values map[string]string) {
				values["APP_ENV"] = "staging"
				values["UPSTREAM_URL"] = "https://service.staging.planext4u.net"
			},
			wantErr: "JWT_ISSUER",
		},
		{
			name: "oversized request policy",
			mutate: func(values map[string]string) {
				values["MAX_REQUEST_BYTES"] = "999999999"
			},
			wantErr: "safe range",
		},
		{
			name: "invalid guest rate policy",
			mutate: func(values map[string]string) {
				values["GUEST_RATE_LIMIT"] = "0"
			},
			wantErr: "safe range",
		},
		{
			name: "invalid rate policy",
			mutate: func(values map[string]string) {
				values["PRINCIPAL_RATE_LIMIT"] = "0"
			},
			wantErr: "safe range",
		},
		{
			name: "invalid address",
			mutate: func(values map[string]string) {
				values["HTTP_ADDRESS"] = "localhost"
			},
			wantErr: "HTTP_ADDRESS",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := validRuntimeValues()
			test.mutate(values)
			_, err := loadRuntimeConfig(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestRedisOptionsRequireTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()

	if _, err := redisOptionsForEnvironment("redis://127.0.0.1:6379/0", "development"); err != nil {
		t.Fatalf("development Redis error = %v", err)
	}
	if _, err := redisOptionsForEnvironment("redis://cache.internal:6379/0", "staging"); err == nil {
		t.Fatal("staging plaintext Redis was accepted")
	}
	if _, err := redisOptionsForEnvironment("rediss://cache.internal:6379/0", "staging"); err != nil {
		t.Fatalf("staging TLS Redis error = %v", err)
	}
}

func validRuntimeValues() map[string]string {
	return map[string]string{
		"APP_ENV":             "development",
		"UPSTREAM_URL":        "http://127.0.0.1:8090",
		"JWT_ISSUER":          "http://identity.local",
		"JWT_AUDIENCE":        "planext4u-mobile",
		"JWT_KEY_ID":          "synthetic-key",
		"JWT_PUBLIC_KEY_FILE": "/run/config/jwt-public.pem",
		"REDIS_URL_FILE":      "/run/secrets/redis-url",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, exists := values[key]
		return value, exists
	}
}
