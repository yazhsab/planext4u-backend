package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestCatalogRuntimeConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"APP_ENV":                   "production",
		"DATABASE_URL":              "postgres://catalog:secret@database.example/p4u?sslmode=verify-full",
		"SERVICEABILITY_ZONES_FILE": "/run/config/catalog-zones.json",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	config, err := loadRuntimeConfig(lookup)
	if err != nil || config.service.ServiceName != "planext4u-catalog" || config.service.HTTPAddress != ":8085" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	values["DATABASE_URL_FILE"] = "/run/secrets/catalog-database-url"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("ambiguous database secret sources were accepted")
	}
	delete(values, "DATABASE_URL_FILE")
	delete(values, "SERVICEABILITY_ZONES_FILE")
	if config, err := loadRuntimeConfig(lookup); err != nil || config.zonesFile != "" {
		t.Fatalf("database-backed zones config=%#v error=%v", config, err)
	}
}

func TestCatalogDatabaseRequiresVerifiedTLSOutsideDevelopment(t *testing.T) {
	t.Parallel()
	runtime := runtimeConfig{service: platformconfig.Config{Environment: platformconfig.EnvironmentProduction}, databaseMaxConns: 10, databaseMinConns: 1, databaseMaxLifetime: 30 * time.Minute}
	if _, err := databaseConfig("postgres://catalog:secret@database.example/p4u?sslmode=require", runtime); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("TLS validation error=%v", err)
	}
}

func TestCatalogZonesAreStrictAndRoutesAreBounded(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "zones.json")
	contents := `[{"tenant_id":"d1f47ba2-1ad1-46bf-aa23-2969a9ea656f","id":"chennai-core","country":"IN","locality":"Chennai","minimum_latitude":12.75,"maximum_latitude":13.35,"minimum_longitude":79.9,"maximum_longitude":80.5,"postal_codes":["600001"]}]`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	zones, err := readZones(path)
	if err != nil || len(zones) != 1 || zones[0].ID != "chennai-core" {
		t.Fatalf("zones=%#v err=%v", zones, err)
	}
	request := httptest.NewRequest("POST", "/v1/catalog/items/item-001/questions", nil)
	if route := catalogRoute(request); route != "/v1/catalog/items/{item_id}/questions" {
		t.Fatalf("route=%q", route)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"x","unexpected":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readZones(path); err == nil {
		t.Fatal("unknown zone fields were accepted")
	}
}
