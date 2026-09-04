package main

import (
	"context"
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
	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
	"github.com/yazhsab/planext4u-backend/internal/platform/server"
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
	"github.com/yazhsab/planext4u-backend/internal/support"
)

var version = "dev"

type runtimeConfig struct {
	service             platformconfig.Config
	databaseURLFile     string
	databaseURL         string
	databaseMaxConns    int32
	databaseMinConns    int32
	databaseMaxLifetime time.Duration
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid support service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load support database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure support database", "error", err)
		return 2
	}
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startupContext, poolConfig)
	if err != nil {
		logger.Error("create support database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	if err = pool.Ping(startupContext); err != nil {
		logger.Error("connect support database", "error", "database is unavailable")
		return 1
	}
	repository, err := support.NewPostgresRepository(pool)
	if err != nil || repository.Ready(startupContext) != nil {
		logger.Error("check support repository", "error", "support schema is unavailable")
		return 1
	}
	service, err := support.NewService(repository, time.Now)
	if err != nil {
		logger.Error("configure support service", "error", err)
		return 2
	}
	application, err := support.NewHandler(service)
	if err != nil {
		logger.Error("configure support handler", "error", err)
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
		if shutdownErr := observability.Shutdown(shutdownContext); shutdownErr != nil {
			logger.Warn("flush telemetry", "error", shutdownErr)
		}
	}()
	httpServer := server.New(runtime.service, logger, version, server.WithTelemetry(observability), server.WithApplication(application, supportRoute), server.WithReadiness(repository.Ready))
	logger.Info("support service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err = httpServer.Serve(ctx); err != nil {
		logger.Error("support service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("support service stopped")
	return 0
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	wrappedLookup := func(key string) (string, bool) {
		switch key {
		case "SERVICE_NAME":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return "planext4u-support", true
		case "HTTP_ADDRESS":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return ":8090", true
		default:
			return lookup(key)
		}
	}
	base, err := platformconfig.LoadFrom(wrappedLookup)
	if err != nil {
		return runtimeConfig{}, err
	}
	result := runtimeConfig{service: base, databaseURLFile: requiredValue(lookup, "DATABASE_URL_FILE"), databaseURL: requiredValue(lookup, "DATABASE_URL"), databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute}
	if value, ok := lookup("DATABASE_MAX_LIFETIME"); ok && strings.TrimSpace(value) != "" {
		result.databaseMaxLifetime, err = time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("DATABASE_MAX_LIFETIME must be a duration")
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
	if (result.databaseURLFile == "") == (result.databaseURL == "") || result.databaseMaxConns < 1 || result.databaseMaxConns > 500 || result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns || result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour {
		return runtimeConfig{}, errors.New("required support service settings are missing or outside their safe range")
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

func supportRoute(request *http.Request) string {
	if request.URL.Path == "/v1/support/tickets" || request.URL.Path == "/internal/v1/support/tickets" {
		return request.URL.Path
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "support" && parts[2] == "tickets" {
		return "/v1/support/tickets/{ticket_id}"
	}
	if len(parts) == 5 && parts[0] == "v1" && parts[1] == "support" && parts[2] == "tickets" && parts[4] == "messages" {
		return "/v1/support/tickets/{ticket_id}/messages"
	}
	return "unmatched"
}
