package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestSupportRoutesAreBounded(t *testing.T) {
	t.Parallel()
	for input, expected := range map[string]string{
		"/v1/support/tickets": "/v1/support/tickets",
		"/v1/support/tickets/2c231280-3cf4-49be-9e6c-96683f4de86a":          "/v1/support/tickets/{ticket_id}",
		"/v1/support/tickets/2c231280-3cf4-49be-9e6c-96683f4de86a/messages": "/v1/support/tickets/{ticket_id}/messages",
		"/internal/v1/support/tickets":                                      "/internal/v1/support/tickets",
		"/v1/support/tickets/id/messages/trailing":                          "unmatched",
	} {
		request := httptest.NewRequest("GET", input, nil)
		if route := supportRoute(request); route != expected {
			t.Errorf("route(%q)=%q want %q", input, route, expected)
		}
	}
}

func TestSupportRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://support:secret@database.example/p4u?sslmode=verify-full",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-support" || config.service.HTTPAddress != ":8090" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	values["DATABASE_URL_FILE"] = "/run/secrets/support-database-url"
	if _, err = loadRuntimeConfig(lookup); err == nil {
		t.Fatal("ambiguous database secret sources were accepted")
	}
	delete(values, "DATABASE_URL_FILE")
	values["DATABASE_MAX_CONNS"] = "0"
	if _, err = loadRuntimeConfig(lookup); err == nil {
		t.Fatal("zero database connections were accepted")
	}
}

func TestSupportDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		service:             platformconfig.Config{Environment: platformconfig.EnvironmentProduction},
		databaseMaxConns:    10,
		databaseMinConns:    1,
		databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://support:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}
