package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestAuditRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://audit:secret@database.example/p4u?sslmode=verify-full",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-audit" || config.service.HTTPAddress != ":8086" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	values["DATABASE_URL_FILE"] = "/run/secrets/audit-database-url"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("ambiguous database secret sources were accepted")
	}
	delete(values, "DATABASE_URL_FILE")
	delete(values, "DATABASE_URL")
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("missing database secret was accepted")
	}
}

func TestAuditDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		service:          platformconfig.Config{Environment: platformconfig.EnvironmentProduction},
		databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://audit:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}

func TestAuditRoutesAreBounded(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"/v1/admin/audit/events", "/v1/admin/audit/exports"} {
		request := httptest.NewRequest("GET", value, nil)
		if route := auditRoute(request); route != value {
			t.Fatalf("route=%q", route)
		}
	}
	request := httptest.NewRequest("GET", "/v1/admin/audit/events/arbitrary", nil)
	if route := auditRoute(request); route != "unmatched" {
		t.Fatalf("unbounded route=%q", route)
	}
}
