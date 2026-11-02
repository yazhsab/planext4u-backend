package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestAdminRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":                    "production",
		"DATABASE_URL":               "postgres://admin:secret@database.example/p4u?sslmode=verify-full",
		"ADMIN_EXCHANGE_SECRET_FILE": "/run/secrets/admin-exchange-key",
		"ADMIN_ALLOWED_ORIGINS":      "https://admin.planext4u.net,https://admin.planext4u.ng",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-admin" || config.service.HTTPAddress != ":8090" || len(config.allowedOrigins) != 2 || !config.requireMFA {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	values["DATABASE_URL_FILE"] = "/run/secrets/admin-database-url"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("ambiguous database secret sources were accepted")
	}
	delete(values, "DATABASE_URL_FILE")
	values["ADMIN_REQUIRE_MFA"] = "false"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("production administrator service accepted disabled MFA")
	}
	delete(values, "ADMIN_REQUIRE_MFA")
	delete(values, "ADMIN_EXCHANGE_SECRET_FILE")
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("missing exchange secret was accepted")
	}
}

func TestAdminFulfillmentOriginConfiguration(t *testing.T) {
	for _, test := range []struct {
		environment, origin string
		allowed             bool
	}{
		{"development", "http://transaction:8086", true},
		{"production", "https://transaction.internal", true},
		{"production", "http://transaction.internal", false},
		{"development", "https://someone:password@transaction.internal", false},
		{"development", "https://transaction.internal/api", false},
		{"development", "https://transaction.internal?tenant=foreign", false},
	} {
		t.Run(test.environment+"-"+test.origin, func(t *testing.T) {
			values := map[string]string{"APP_ENV": test.environment, "DATABASE_URL": "postgres://synthetic:local-only@database/p4u?sslmode=verify-full", "ADMIN_EXCHANGE_SECRET_FILE": "/synthetic/key", "ADMIN_ALLOWED_ORIGINS": "https://admin.planext4u.net", "FULFILLMENT_BASE_URL": test.origin}
			config, err := loadRuntimeConfig(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
			if (err == nil) != test.allowed || err == nil && config.fulfillmentURL == nil {
				t.Fatalf("origin acceptance=%v expected=%v", err, test.allowed)
			}
		})
	}
}

func TestAdminDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		service:          platformconfig.Config{Environment: platformconfig.EnvironmentProduction},
		databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://admin:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}

func TestAdminFirebaseLoginRequiresUnambiguousTenantConfiguration(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"project without platform tenant": {"FIREBASE_PROJECT_ID": "test-project"},
		"invalid platform tenant":         {"FIREBASE_PROJECT_ID": "test-project", "TENANT_ID": "invalid"},
		"auth tenant without project":     {"FIREBASE_AUTH_TENANT_ID": "test-auth-tenant"},
		"credentials without project":     {"FIREBASE_CREDENTIALS_FILE": "/private/test.json"},
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{"DATABASE_URL": "postgres://admin:local@localhost/p4u?sslmode=disable", "ADMIN_EXCHANGE_SECRET_FILE": "/private/test.key", "ADMIN_ALLOWED_ORIGINS": "https://localhost:4173"}
			for key, value := range extra {
				values[key] = value
			}
			lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
			if _, err := loadRuntimeConfig(lookup); err == nil {
				t.Fatal("ambiguous Firebase sign-in configuration accepted")
			}
			values["FIREBASE_PROJECT_ID"] = "test-project"
			values["TENANT_ID"] = "00000000-0000-4000-8000-000000000001"
			if _, err := loadRuntimeConfig(lookup); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminRoutesAreBounded(t *testing.T) {
	t.Parallel()
	for input, expected := range map[string]string{
		"/internal/v1/admin-sessions":                          "/internal/v1/admin-sessions",
		"/admin/api/v1/session":                                "/admin/api/v1/session",
		"/admin/api/v1/operations/change-1/approve":            "/admin/api/v1/operations/{change_id}/{action}",
		"/admin/api/v1/operations/change-1/reject":             "/admin/api/v1/operations/{change_id}/{action}",
		"/admin/api/v1/support/tickets":                        "/admin/api/v1/support/tickets",
		"/admin/api/v1/cms/pages/customer-home/draft":          "/admin/api/v1/cms/pages/{page_id}/draft",
		"/admin/api/v1/operations/change-1/arbitrary":          "unmatched",
		"/admin/api/v1/cms/pages/customer-home/draft/trailing": "unmatched",
	} {
		request := httptest.NewRequest("GET", input, nil)
		if route := adminRoute(request); route != expected {
			t.Errorf("route(%q)=%q want %q", input, route, expected)
		}
	}
}
