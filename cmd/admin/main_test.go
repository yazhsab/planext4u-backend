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
