package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRuntimeConfigUsesBoundedDefaults(t *testing.T) {
	t.Parallel()
	config, err := loadRuntimeConfig(configLookup(validIdentityConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if config.environment != "development" || config.httpAddress != ":8083" ||
		config.providerTimeout != 3*time.Second || config.accessTTL != 10*time.Minute ||
		config.sessionTTL != 30*24*time.Hour || config.databaseMaxConns != 50 ||
		strings.Join(config.allowedCountries, ",") != "IN,GB" {
		t.Fatalf("defaults = %#v", config)
	}
	if providers := providersForEnvironment("development"); strings.Join(providers, ",") != "firebase,oidc,local" {
		t.Fatalf("development providers = %v", providers)
	}
	if providers := providersForEnvironment("production"); strings.Join(providers, ",") != "firebase,oidc" {
		t.Fatalf("production providers = %v", providers)
	}
}

func TestLoadRuntimeConfigRejectsUnsafeSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{name: "missing secret file", mutate: func(values map[string]string) { delete(values, "DATABASE_URL_FILE") }, wantErr: "required identity"},
		{name: "bad environment", mutate: func(values map[string]string) { values["APP_ENV"] = "qa" }, wantErr: "APP_ENV"},
		{name: "plain provider staging", mutate: func(values map[string]string) {
			values["APP_ENV"] = "staging"
			values["PROVIDER_BASE_URL"] = "http://provider.internal"
			values["JWT_ISSUER"] = "https://identity.staging.planext4u.net"
		}, wantErr: "HTTPS"},
		{name: "plain issuer staging", mutate: func(values map[string]string) {
			values["APP_ENV"] = "staging"
			values["PROVIDER_BASE_URL"] = "https://provider.staging.planext4u.net"
			values["JWT_ISSUER"] = "http://identity.internal"
		}, wantErr: "HTTPS"},
		{name: "provider credentials", mutate: func(values map[string]string) { values["PROVIDER_BASE_URL"] = "https://user:secret@provider.example" }, wantErr: "PROVIDER_BASE_URL"},
		{name: "country", mutate: func(values map[string]string) { values["ALLOWED_COUNTRIES"] = "IND" }, wantErr: "ALLOWED_COUNTRIES"},
		{name: "access ttl", mutate: func(values map[string]string) { values["ACCESS_TTL"] = "30m" }, wantErr: "safe range"},
		{name: "pool bounds", mutate: func(values map[string]string) {
			values["DATABASE_MIN_CONNS"] = "20"
			values["DATABASE_MAX_CONNS"] = "10"
		}, wantErr: "safe range"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := validIdentityConfig()
			test.mutate(values)
			_, err := loadRuntimeConfig(configLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("loadRuntimeConfig() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestDatabaseConfigRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		environment:         "production",
		databaseMaxConns:    20,
		databaseMinConns:    2,
		databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://user:password@db.example/identity?sslmode=require", runtime); err == nil {
		t.Fatal("sslmode=require accepted in production")
	}
	config, err := databaseConfig("postgres://user:password@db.example/identity?sslmode=verify-full", runtime)
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConns != 20 || config.MinConns != 2 || config.ConnConfig.TLSConfig == nil {
		t.Fatalf("database config = %#v", config)
	}
}

func validIdentityConfig() map[string]string {
	return map[string]string{
		"DATABASE_URL_FILE":     "/run/secrets/identity-database-url",
		"PROVIDER_BASE_URL":     "http://127.0.0.1:18081",
		"TENANT_ID":             "c688a212-50fc-4d5c-b370-35a8c1d04f55",
		"ALLOWED_COUNTRIES":     "IN, GB, IN",
		"JWT_ISSUER":            "http://identity.local",
		"JWT_AUDIENCE":          "planext4u-mobile",
		"JWT_KEY_ID":            "identity-2026-01",
		"JWT_PRIVATE_KEY_FILE":  "/run/secrets/identity-private-key",
		"REFRESH_HMAC_KEY_FILE": "/run/secrets/identity-refresh-hmac",
	}
}

func configLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, exists := values[key]
		return value, exists
	}
}
