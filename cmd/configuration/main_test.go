package main

import (
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestConfigurationRuntimeConfigFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{"APP_ENV": "production", "DATABASE_URL_FILE": "/run/secrets/configuration-database-url", "WEB_DEPLOYMENT_ID": "web-2026.09.02-001"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-configuration" || config.service.HTTPAddress != ":8084" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	delete(values, "WEB_DEPLOYMENT_ID")
	if _, err := loadRuntimeConfig(lookup); err == nil || !strings.Contains(err.Error(), "required configuration") {
		t.Fatalf("missing web deployment identifier error=%v", err)
	}
	values["WEB_DEPLOYMENT_ID"] = "unsafe deployment"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("unsafe web deployment identifier was accepted")
	}
	values["WEB_DEPLOYMENT_ID"] = "web-2026.09.02-001"
	delete(values, "DATABASE_URL_FILE")
	if _, err := loadRuntimeConfig(lookup); err == nil || !strings.Contains(err.Error(), "required configuration") {
		t.Fatalf("missing secret error=%v", err)
	}
	values["DATABASE_URL"] = "postgres://configuration:secret@database.example/p4u?sslmode=verify-full"
	config, err = loadRuntimeConfig(lookup)
	if err != nil || config.databaseURL == "" || config.databaseURLFile != "" {
		t.Fatalf("ECS secret configuration=%#v err=%v", config, err)
	}
	values["DATABASE_URL_FILE"] = "/run/secrets/configuration-database-url"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("ambiguous database secret sources were accepted")
	}
}

func TestConfigurationDatabaseTLSIsRequiredOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{service: platformconfig.Config{Environment: platformconfig.EnvironmentProduction}, databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute}
	if _, err := databaseConfig("postgres://user:password@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}
