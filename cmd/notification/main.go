package main

import (
	"context"
	"encoding/base64"
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
	"github.com/yazhsab/planext4u-backend/internal/notification"
	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
	"github.com/yazhsab/planext4u-backend/internal/platform/server"
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
)

var version = "dev"

type runtimeConfig struct {
	service                  platformconfig.Config
	databaseURLFile          string
	databaseURL              string
	databaseMaxConns         int32
	databaseMinConns         int32
	databaseMaxLifetime      time.Duration
	deviceTokenKeyFile       string
	deviceTokenKeyVersion    int
	firebaseCredentialsFile  string
	providerTimeout          time.Duration
	pollInterval             time.Duration
	batchSize                int
	orderNotificationKeyFile string
	orderTemplateVersion     int64
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid notification service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load notification database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure notification database", "error", err)
		return 2
	}
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startupContext, poolConfig)
	if err != nil {
		logger.Error("create notification database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	if err := pool.Ping(startupContext); err != nil {
		logger.Error("connect notification database", "error", "database is unavailable")
		return 1
	}
	key, err := loadDeviceTokenKey(runtime.deviceTokenKeyFile)
	if err != nil {
		logger.Error("load device-token encryption key", "error", "device-token key is unavailable or invalid")
		return 2
	}
	tokenCipher, err := notification.NewAESGCMTokenCipher(map[int][]byte{runtime.deviceTokenKeyVersion: key}, runtime.deviceTokenKeyVersion)
	if err != nil {
		logger.Error("configure device-token encryption", "error", err)
		return 2
	}
	repository, err := notification.NewPostgresRepository(pool, tokenCipher)
	if err != nil || repository.Ready(startupContext) != nil {
		logger.Error("check notification repository", "error", "notification schema is unavailable")
		return 1
	}
	providers := map[notification.Channel]notification.Provider{}
	if runtime.firebaseCredentialsFile != "" {
		credentials, readErr := readRegularFile(runtime.firebaseCredentialsFile, 64*1024)
		if readErr != nil {
			logger.Error("load Firebase credentials", "error", "Firebase credentials are unavailable or invalid")
			return 2
		}
		providerClient := &http.Client{Timeout: runtime.providerTimeout}
		tokenSource, sourceErr := notification.NewServiceAccountTokenSource(credentials, providerClient, time.Now)
		if sourceErr != nil {
			logger.Error("configure Firebase token source", "error", "Firebase credentials are invalid")
			return 2
		}
		provider, providerErr := notification.NewFCMProvider(tokenSource.ProjectID(), repository, tokenSource, providerClient)
		if providerErr != nil {
			logger.Error("configure Firebase provider", "error", providerErr)
			return 2
		}
		providers[notification.ChannelPush] = provider
	}
	service, err := notification.NewService(repository, providers, time.Now)
	if err != nil {
		logger.Error("configure notification service", "error", err)
		return 2
	}
	var application http.Handler
	if runtime.orderNotificationKeyFile != "" {
		internalKey, readErr := readRegularFile(runtime.orderNotificationKeyFile, 4096)
		if readErr != nil || len(internalKey) < 32 {
			logger.Error("load order notification authentication key", "error", "internal authentication key is unavailable or invalid")
			return 2
		}
		application, err = notification.NewHandlerWithInternalOrderNotifications(service, internalKey, runtime.orderTemplateVersion)
	} else {
		application, err = notification.NewHandler(service)
	}
	if err != nil {
		logger.Error("configure notification handler", "error", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(providers) > 0 {
		go processLoop(ctx, logger, repository, service, runtime)
	}
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
	httpServer := server.New(runtime.service, logger, version,
		server.WithTelemetry(observability), server.WithApplication(application, notificationRoute), server.WithReadiness(repository.Ready))
	logger.Info("notification service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("notification service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("notification service stopped")
	return 0
}

func processLoop(ctx context.Context, logger *slog.Logger, repository *notification.PostgresRepository, service *notification.Service, runtime runtimeConfig) {
	ticker := time.NewTicker(runtime.pollInterval)
	defer ticker.Stop()
	for {
		values, err := repository.QueuedDeliveryIDs(ctx, runtime.batchSize)
		if err != nil && ctx.Err() == nil {
			logger.Error("list queued notifications", "error", err)
		} else {
			for _, value := range values {
				if err := service.Process(ctx, value.TenantID, value.ID); err != nil && !errors.Is(err, notification.ErrProviderRetryable) && ctx.Err() == nil {
					logger.Error("process notification delivery", "delivery_id", value.ID, "error", err)
				}
			}
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
			return "planext4u-notification", true
		case "HTTP_ADDRESS":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return ":8089", true
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
		deviceTokenKeyFile: requiredValue(lookup, "DEVICE_TOKEN_KEY_FILE"), deviceTokenKeyVersion: 1,
		firebaseCredentialsFile: requiredValue(lookup, "FIREBASE_SERVICE_ACCOUNT_FILE"), providerTimeout: 15 * time.Second,
		pollInterval: time.Second, batchSize: 50, databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute,
		orderNotificationKeyFile: requiredValue(lookup, "ORDER_NOTIFICATION_HMAC_KEY_FILE"), orderTemplateVersion: 1,
	}
	for key, destination := range map[string]*time.Duration{
		"PROVIDER_TIMEOUT": &result.providerTimeout, "POLL_INTERVAL": &result.pollInterval,
		"DATABASE_MAX_LIFETIME": &result.databaseMaxLifetime,
	} {
		if raw := requiredValue(lookup, key); raw != "" {
			*destination, err = time.ParseDuration(raw)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
			}
		}
	}
	if raw := requiredValue(lookup, "DEVICE_TOKEN_KEY_VERSION"); raw != "" {
		result.deviceTokenKeyVersion, err = strconv.Atoi(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("DEVICE_TOKEN_KEY_VERSION must be an integer")
		}
	}
	if raw := requiredValue(lookup, "BATCH_SIZE"); raw != "" {
		result.batchSize, err = strconv.Atoi(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("BATCH_SIZE must be an integer")
		}
	}
	if raw := requiredValue(lookup, "ORDER_NOTIFICATION_TEMPLATE_VERSION"); raw != "" {
		result.orderTemplateVersion, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("ORDER_NOTIFICATION_TEMPLATE_VERSION must be an integer")
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
	if (result.databaseURLFile == "") == (result.databaseURL == "") || result.deviceTokenKeyFile == "" ||
		result.deviceTokenKeyVersion < 1 || result.deviceTokenKeyVersion > 1_000_000 ||
		result.providerTimeout < time.Second || result.providerTimeout > time.Minute || result.pollInterval < 100*time.Millisecond || result.pollInterval > time.Minute ||
		result.batchSize < 1 || result.batchSize > 100 || result.orderTemplateVersion < 1 || result.databaseMaxConns < 1 || result.databaseMaxConns > 500 ||
		result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns || result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour {
		return runtimeConfig{}, errors.New("required notification service settings are missing or outside their safe range")
	}
	if base.Environment != platformconfig.EnvironmentDevelopment && result.firebaseCredentialsFile == "" {
		return runtimeConfig{}, errors.New("Firebase credentials are required outside development")
	}
	if base.Environment != platformconfig.EnvironmentDevelopment && result.orderNotificationKeyFile == "" {
		return runtimeConfig{}, errors.New("order notification authentication key is required outside development")
	}
	return result, nil
}

func loadDeviceTokenKey(path string) ([]byte, error) {
	contents, err := readRegularFile(path, 1024)
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(contents))
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("device-token key must be base64-encoded 32-byte material")
	}
	return key, nil
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

func notificationRoute(request *http.Request) string {
	if request.URL.Path == "/v1/notifications/devices/current" {
		return request.URL.Path
	}
	return "unmatched"
}
