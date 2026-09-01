package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/catalog"
	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
	"github.com/yazhsab/planext4u-backend/internal/platform/server"
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
)

var version = "dev"

type runtimeConfig struct {
	service             platformconfig.Config
	databaseURLFile     string
	databaseURL         string
	zonesFile           string
	staleTTL            time.Duration
	databaseMaxConns    int32
	databaseMinConns    int32
	databaseMaxLifetime time.Duration
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid catalog service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load catalog database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure catalog database", "error", err)
		return 2
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		logger.Error("create catalog database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStartup()
	if err := pool.Ping(startupContext); err != nil {
		logger.Error("connect catalog database", "error", "database is unavailable")
		return 1
	}
	repository, err := catalog.NewPostgresRepository(pool)
	if err != nil || repository.Ready(startupContext) != nil {
		logger.Error("check catalog repository", "error", "catalog schema is unavailable")
		return 1
	}
	var zones []catalog.Zone
	if runtime.zonesFile != "" {
		zones, err = readZones(runtime.zonesFile)
	} else {
		zones, err = repository.Zones(startupContext)
	}
	if err != nil || len(zones) == 0 {
		logger.Error("load serviceability zones", "error", "zone configuration is unavailable or invalid")
		return 2
	}
	service, err := catalog.NewService(repository, zones, runtime.staleTTL, time.Now)
	if err != nil {
		logger.Error("configure catalog service", "error", err)
		return 2
	}
	application, err := catalog.NewHandler(service)
	if err != nil {
		logger.Error("configure catalog handler", "error", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	traceRatio := 1.0
	if runtime.service.Environment == platformconfig.EnvironmentProduction {
		traceRatio = 0.10
	}
	observability, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: runtime.service.ServiceName, ServiceVersion: version, Environment: string(runtime.service.Environment), TraceRatio: traceRatio})
	if err != nil {
		logger.Error("initialize telemetry", "error", err)
		return 1
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), runtime.service.ShutdownTimeout)
		defer cancel()
		if err := observability.Shutdown(shutdownContext); err != nil {
			logger.Warn("flush telemetry", "error", err)
		}
	}()
	httpServer := server.New(runtime.service, logger, version, server.WithTelemetry(observability), server.WithApplication(application, catalogRoute), server.WithReadiness(func(ctx context.Context) error { return repository.Ready(ctx) }))
	logger.Info("catalog service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("catalog service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("catalog service stopped")
	return 0
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	wrappedLookup := func(key string) (string, bool) {
		switch key {
		case "SERVICE_NAME":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return "planext4u-catalog", true
		case "HTTP_ADDRESS":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return ":8085", true
		default:
			return lookup(key)
		}
	}
	base, err := platformconfig.LoadFrom(wrappedLookup)
	if err != nil {
		return runtimeConfig{}, err
	}
	result := runtimeConfig{
		service: base, databaseURLFile: requiredValue(lookup, "DATABASE_URL_FILE"), databaseURL: requiredValue(lookup, "DATABASE_URL"),
		zonesFile: requiredValue(lookup, "SERVICEABILITY_ZONES_FILE"), staleTTL: 5 * time.Minute,
		databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute,
	}
	for key, destination := range map[string]*time.Duration{"CATALOG_STALE_TTL": &result.staleTTL, "DATABASE_MAX_LIFETIME": &result.databaseMaxLifetime} {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			parsed, parseErr := time.ParseDuration(strings.TrimSpace(value))
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
			}
			*destination = parsed
		}
	}
	for key, destination := range map[string]*int32{"DATABASE_MAX_CONNS": &result.databaseMaxConns, "DATABASE_MIN_CONNS": &result.databaseMinConns} {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			parsed, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
			*destination = int32(parsed)
		}
	}
	if (result.databaseURLFile == "") == (result.databaseURL == "") || result.staleTTL < time.Second || result.staleTTL > 24*time.Hour || result.databaseMaxConns < 1 || result.databaseMaxConns > 500 || result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns || result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour {
		return runtimeConfig{}, errors.New("required catalog service settings are missing or outside their safe range")
	}
	return result, nil
}

func loadDatabaseURL(runtime runtimeConfig) (string, error) {
	if runtime.databaseURL != "" {
		return runtime.databaseURL, nil
	}
	value, err := readRegularFile(runtime.databaseURLFile, 4096)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func databaseConfig(value string, runtime runtimeConfig) (*pgxpool.Config, error) {
	parsedURL, err := url.Parse(value)
	if err != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") || parsedURL.Host == "" {
		return nil, errors.New("database URL is invalid")
	}
	if runtime.service.Environment != platformconfig.EnvironmentDevelopment && parsedURL.Query().Get("sslmode") != "verify-full" {
		return nil, errors.New("database URL must use sslmode=verify-full outside development")
	}
	config, err := pgxpool.ParseConfig(value)
	if err != nil {
		return nil, errors.New("database URL is invalid")
	}
	if runtime.service.Environment != platformconfig.EnvironmentDevelopment && config.ConnConfig.TLSConfig == nil {
		return nil, errors.New("database URL must require TLS outside development")
	}
	config.MaxConns, config.MinConns, config.MaxConnLifetime = runtime.databaseMaxConns, runtime.databaseMinConns, runtime.databaseMaxLifetime
	config.MaxConnIdleTime, config.HealthCheckPeriod = 5*time.Minute, 30*time.Second
	return config, nil
}

func readZones(path string) ([]catalog.Zone, error) {
	contents, err := readRegularFile(path, 256*1024)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var zones []catalog.Zone
	if err := decoder.Decode(&zones); err != nil || len(zones) == 0 || len(zones) > 10000 {
		return nil, errors.New("zone configuration is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("zone configuration contains trailing data")
	}
	return zones, nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("file is invalid")
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

func requiredValue(lookup func(string) (string, bool), key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func catalogRoute(request *http.Request) string {
	switch request.URL.Path {
	case "/v1/home", "/v1/catalog/categories", "/v1/catalog/items", "/v1/catalog/search", "/v1/catalog/suggestions", "/v1/geocoding/search", "/v1/serviceability/check":
		return request.URL.Path
	default:
		if strings.HasPrefix(request.URL.Path, "/v1/catalog/items/") {
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) == 5 && parts[4] == "questions" {
				return "/v1/catalog/items/{item_id}/questions"
			}
			return "/v1/catalog/items/{item_id}"
		}
		return "unmatched"
	}
}
