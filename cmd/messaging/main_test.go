package main

import (
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestMessagingRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":               "production",
		"DATABASE_URL":          "postgres://messaging:secret@database.example/p4u?sslmode=verify-full",
		"NATS_URL_FILE":         "/run/secrets/nats-url",
		"NATS_CREDENTIALS_FILE": "/run/secrets/nats.creds",
		"WORKER_ID":             "outbox-worker-1",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-messaging" || config.service.HTTPAddress != ":8088" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	delete(values, "WORKER_ID")
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("missing worker identity was accepted")
	}
	values["WORKER_ID"] = "outbox-worker-1"
	values["BATCH_SIZE"] = "101"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("unbounded batch size was accepted")
	}
}

func TestMessagingDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{
		service:          platformconfig.Config{Environment: platformconfig.EnvironmentProduction},
		databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute,
	}
	if _, err := databaseConfig("postgres://messaging:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}
