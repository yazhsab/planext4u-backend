package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/yazhsab/planext4u-backend/internal/messaging"
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
	databaseMaxConns    int32
	databaseMinConns    int32
	databaseMaxLifetime time.Duration
	natsURLFile         string
	natsCredentialsFile string
	natsStream          string
	natsSubjectPrefix   string
	bootstrapStream     bool
	workerID            string
	pollInterval        time.Duration
	lease               time.Duration
	batchSize           int
	maxAttempts         int
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid messaging service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load messaging database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure messaging database", "error", err)
		return 2
	}
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startupContext, poolConfig)
	if err != nil {
		logger.Error("create messaging database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	if err := pool.Ping(startupContext); err != nil {
		logger.Error("connect messaging database", "error", "database is unavailable")
		return 1
	}
	store, err := messaging.NewPostgresStore(pool)
	if err != nil || store.Ready(startupContext) != nil {
		logger.Error("check messaging repository", "error", "messaging schema is unavailable")
		return 1
	}
	connection, err := configureNATS(runtime)
	if err != nil {
		logger.Error("connect messaging broker", "error", "NATS configuration or connectivity is invalid")
		return 1
	}
	defer connection.Close()
	if runtime.bootstrapStream {
		if err := messaging.EnsureJetStream(startupContext, connection, runtime.natsStream, runtime.natsSubjectPrefix); err != nil {
			logger.Error("bootstrap messaging stream", "error", err)
			return 1
		}
	}
	publisher, err := messaging.NewJetStreamPublisher(connection, runtime.natsStream, runtime.natsSubjectPrefix)
	if err != nil || publisher.Ready(startupContext) != nil {
		logger.Error("check messaging stream", "error", "JetStream stream is unavailable")
		return 1
	}
	service, err := messaging.NewService(store, store, publisher, unavailableReplayAuditor{}, runtime.lease, runtime.maxAttempts, time.Now)
	if err != nil {
		logger.Error("configure messaging dispatcher", "error", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	traceRatio := 1.0
	if runtime.service.Environment == platformconfig.EnvironmentProduction {
		traceRatio = 0.10
	}
	observability, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: runtime.service.ServiceName, ServiceVersion: version,
		Environment: string(runtime.service.Environment), TraceRatio: traceRatio,
	})
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
	go dispatchLoop(ctx, logger, service, runtime)
	readiness := func(ctx context.Context) error {
		if err := store.Ready(ctx); err != nil {
			return err
		}
		return publisher.Ready(ctx)
	}
	httpServer := server.New(runtime.service, logger, version, server.WithTelemetry(observability), server.WithReadiness(readiness))
	logger.Info("messaging service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("messaging service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("messaging service stopped")
	return 0
}

func dispatchLoop(ctx context.Context, logger *slog.Logger, service *messaging.Service, runtime runtimeConfig) {
	ticker := time.NewTicker(runtime.pollInterval)
	defer ticker.Stop()
	for {
		if count, err := service.Dispatch(ctx, runtime.workerID, runtime.batchSize); err != nil && ctx.Err() == nil {
			logger.Error("dispatch outbox batch", "error", err)
		} else if count > 0 {
			logger.Info("dispatched outbox batch", "count", count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	wrappedLookup := func(key string) (string, bool) {
		switch key {
		case "SERVICE_NAME":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return "planext4u-messaging", true
		case "HTTP_ADDRESS":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return ":8088", true
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
		natsURLFile: requiredValue(lookup, "NATS_URL_FILE"), natsCredentialsFile: requiredValue(lookup, "NATS_CREDENTIALS_FILE"),
		natsStream: valueOrDefault(lookup, "NATS_STREAM", "P4U_EVENTS"), natsSubjectPrefix: valueOrDefault(lookup, "NATS_SUBJECT_PREFIX", "planext4u.events"),
		workerID: requiredValue(lookup, "WORKER_ID"), pollInterval: time.Second, lease: time.Minute, batchSize: 50, maxAttempts: 8,
		databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute,
	}
	if raw := requiredValue(lookup, "NATS_BOOTSTRAP_STREAM"); raw != "" {
		result.bootstrapStream, err = strconv.ParseBool(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("NATS_BOOTSTRAP_STREAM must be boolean")
		}
	}
	for key, destination := range map[string]*time.Duration{
		"POLL_INTERVAL": &result.pollInterval, "MESSAGE_LEASE": &result.lease, "DATABASE_MAX_LIFETIME": &result.databaseMaxLifetime,
	} {
		if raw := requiredValue(lookup, key); raw != "" {
			*destination, err = time.ParseDuration(raw)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
			}
		}
	}
	for key, destination := range map[string]*int{
		"BATCH_SIZE": &result.batchSize, "MAX_ATTEMPTS": &result.maxAttempts,
	} {
		if raw := requiredValue(lookup, key); raw != "" {
			*destination, err = strconv.Atoi(raw)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
		}
	}
	for key, destination := range map[string]*int32{
		"DATABASE_MAX_CONNS": &result.databaseMaxConns, "DATABASE_MIN_CONNS": &result.databaseMinConns,
	} {
		if raw := requiredValue(lookup, key); raw != "" {
			parsed, parseErr := strconv.ParseInt(raw, 10, 32)
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
			*destination = int32(parsed)
		}
	}
	if (result.databaseURLFile == "") == (result.databaseURL == "") || result.natsURLFile == "" || result.workerID == "" ||
		result.pollInterval < 100*time.Millisecond || result.pollInterval > time.Minute || result.lease < time.Second || result.lease > 5*time.Minute ||
		result.batchSize < 1 || result.batchSize > 100 || result.maxAttempts < 1 || result.maxAttempts > 20 ||
		result.databaseMaxConns < 1 || result.databaseMaxConns > 500 || result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns ||
		result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour {
		return runtimeConfig{}, errors.New("required messaging service settings are missing or outside their safe range")
	}
	if result.bootstrapStream && base.Environment != platformconfig.EnvironmentDevelopment {
		return runtimeConfig{}, errors.New("NATS_BOOTSTRAP_STREAM is restricted to development")
	}
	return result, nil
}

func configureNATS(runtime runtimeConfig) (*nats.Conn, error) {
	contents, err := readRegularFile(runtime.natsURLFile, 4096)
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(contents))
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "nats" && parsed.Scheme != "tls") {
		return nil, errors.New("NATS URL is invalid")
	}
	if runtime.service.Environment != platformconfig.EnvironmentDevelopment && (parsed.Scheme != "tls" || runtime.natsCredentialsFile == "") {
		return nil, errors.New("NATS requires TLS and credentials outside development")
	}
	options := []nats.Option{
		nats.Name(runtime.service.ServiceName + "-" + runtime.workerID), nats.Timeout(5 * time.Second),
		nats.ReconnectWait(time.Second), nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true),
	}
	if runtime.natsCredentialsFile != "" {
		options = append(options, nats.UserCredentials(runtime.natsCredentialsFile))
	}
	return nats.Connect(value, options...)
}

func loadDatabaseURL(runtime runtimeConfig) (string, error) {
	if runtime.databaseURL != "" {
		return runtime.databaseURL, nil
	}
	contents, err := readRegularFile(runtime.databaseURLFile, 4096)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(contents)), nil
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

func valueOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value := requiredValue(lookup, key); value != "" {
		return value
	}
	return fallback
}

type unavailableReplayAuditor struct{}

func (unavailableReplayAuditor) RecordReplay(context.Context, messaging.ReplayAudit) error {
	return messaging.ErrForbidden
}
