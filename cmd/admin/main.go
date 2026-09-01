package main

import (
	"bytes"
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
	"github.com/yazhsab/planext4u-backend/internal/adminops"
	"github.com/yazhsab/planext4u-backend/internal/adminshell"
	"github.com/yazhsab/planext4u-backend/internal/audit"
	"github.com/yazhsab/planext4u-backend/internal/configcms"
	"github.com/yazhsab/planext4u-backend/internal/governance"
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
	exchangeSecretFile  string
	allowedOrigins      []string
	sessionTTL          time.Duration
	exchangeMaxSkew     time.Duration
	cleanupInterval     time.Duration
	cacheTTL            time.Duration
	requireMFA          bool
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid administrator service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load administrator database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure administrator database", "error", err)
		return 2
	}
	startup, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startup, poolConfig)
	if err != nil {
		logger.Error("create administrator database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		logger.Error("connect administrator database", "error", "database is unavailable")
		return 1
	}
	exchangeSecret, err := readRegularFile(runtime.exchangeSecretFile, 4096)
	exchangeSecret = bytes.TrimSpace(exchangeSecret)
	if err != nil || len(exchangeSecret) < 32 {
		logger.Error("load administrator session exchange key", "error", "session exchange key is unavailable or invalid")
		return 2
	}

	sessions, err := adminshell.NewPostgresSessionStore(pool, time.Now)
	if err != nil {
		logger.Error("configure administrator sessions", "error", err)
		return 2
	}
	configurationRepository, err := configcms.NewPostgresRepository(pool)
	if err != nil {
		logger.Error("configure administrator CMS repository", "error", err)
		return 2
	}
	configurationCache, err := configcms.NewCachedRepository(configurationRepository, runtime.cacheTTL, time.Now)
	if err != nil {
		logger.Error("configure administrator CMS cache", "error", err)
		return 2
	}
	publisher, err := configcms.NewPublisher(configurationRepository, configurationCache, nil, time.Now)
	if err != nil {
		logger.Error("configure administrator CMS publication", "error", err)
		return 2
	}
	publication, err := configcms.NewPublicationServiceWithWorkspace(configurationRepository, configurationRepository, configurationRepository, publisher, time.Now)
	if err != nil {
		logger.Error("configure administrator CMS publication service", "error", err)
		return 2
	}
	cmsExecutor, err := configcms.NewAdminOperationExecutor(publication)
	if err != nil {
		logger.Error("configure administrator CMS command executor", "error", err)
		return 2
	}
	domainExecutor, err := adminops.NewPostgresDomainExecutor(pool, time.Now)
	if err != nil {
		logger.Error("configure administrator domain executor", "error", err)
		return 2
	}
	controlExecutor, err := adminops.NewPostgresControlPlaneExecutor(pool, time.Now)
	if err != nil {
		logger.Error("configure published control plane executor", "error", err)
		return 2
	}
	cmsRoute := adminops.ExecuteFunc(func(principal adminops.Principal, change adminops.Change) error {
		if change.Command.Action == adminops.ActionCMSPublish {
			return cmsExecutor.Execute(principal, change)
		}
		return domainExecutor.Execute(principal, change)
	})
	router, err := adminops.NewRoutingExecutor(domainExecutor, map[adminops.Domain]adminops.Executor{
		adminops.DomainCMS:          cmsRoute,
		adminops.DomainPolicy:       controlExecutor,
		adminops.DomainCountry:      controlExecutor,
		adminops.DomainIntelligence: controlExecutor,
		adminops.DomainContent:      controlExecutor,
		adminops.DomainEmergency:    controlExecutor,
	})
	if err != nil {
		logger.Error("configure administrator domain routing", "error", err)
		return 2
	}
	operations, err := adminops.NewPostgresService(pool, time.Now, router)
	if err != nil {
		logger.Error("configure administrator operations", "error", err)
		return 2
	}
	auditRepository, err := audit.NewPostgresRepository(pool)
	if err != nil {
		logger.Error("configure administrator audit repository", "error", err)
		return 2
	}
	auditService, err := audit.NewService(auditRepository, time.Now)
	if err != nil {
		logger.Error("configure administrator audit service", "error", err)
		return 2
	}
	authoring, err := configcms.NewAuthoringService(configurationRepository, time.Now)
	if err != nil {
		logger.Error("configure administrator CMS authoring", "error", err)
		return 2
	}
	workspaceAuthoring, err := configcms.NewWorkspaceAuthoringService(configurationRepository, time.Now)
	if err != nil {
		logger.Error("configure administrator CMS workspace authoring", "error", err)
		return 2
	}
	governanceService, err := governance.NewPostgresService(pool, time.Now)
	if err != nil {
		logger.Error("configure durable governance dashboard", "error", err)
		return 2
	}
	adminHandler, err := adminshell.NewHandler(adminshell.Config{
		Sessions: sessions, Audit: auditService, Operations: operations, CMS: authoring, CMSWorkspace: workspaceAuthoring, Governance: governanceService, Clock: time.Now,
		AllowedOrigins: runtime.allowedOrigins, RequireMFA: runtime.requireMFA,
	})
	if err != nil {
		logger.Error("configure administrator API", "error", err)
		return 2
	}
	governanceHandler, err := governance.NewHandler(governanceService)
	if err != nil {
		logger.Error("configure governance API", "error", err)
		return 2
	}
	exchangeHandler, err := adminshell.NewSessionExchangeHandler(adminshell.SessionExchangeConfig{
		Sessions: sessions, Secret: exchangeSecret, Clock: time.Now, SessionTTL: runtime.sessionTTL,
		MaxSkew: runtime.exchangeMaxSkew, RequireMFA: runtime.requireMFA,
	})
	if err != nil {
		logger.Error("configure administrator session exchange", "error", err)
		return 2
	}
	application := http.NewServeMux()
	application.Handle("/internal/v1/admin-sessions", exchangeHandler)
	application.Handle("/admin/", adminHandler)
	application.Handle("/v1/governance/", governanceHandler)

	dependenciesReady := func(ctx context.Context) error {
		for _, check := range []func(context.Context) error{
			pool.Ping, sessions.Ready, operations.Ready, domainExecutor.Ready, controlExecutor.Ready,
			configurationRepository.Ready, auditRepository.Ready, governanceService.Ready,
		} {
			if err := check(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if err := dependenciesReady(startup); err != nil {
		logger.Error("check administrator dependencies", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sessionCleanupLoop(ctx, logger, sessions, runtime.cleanupInterval)
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
		shutdown, cancel := context.WithTimeout(context.Background(), runtime.service.ShutdownTimeout)
		defer cancel()
		if err := observability.Shutdown(shutdown); err != nil {
			logger.Warn("flush telemetry", "error", err)
		}
	}()
	httpServer := server.New(runtime.service, logger, version,
		server.WithTelemetry(observability), server.WithApplication(application, adminRoute), server.WithReadiness(dependenciesReady))
	logger.Info("administrator service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("administrator service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("administrator service stopped")
	return 0
}

func sessionCleanupLoop(ctx context.Context, logger *slog.Logger, sessions *adminshell.PostgresSessionStore, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if purged, err := sessions.PurgeExpired(ctx, 1000); err != nil && ctx.Err() == nil {
			logger.Warn("purge expired administrator sessions", "error", err)
		} else if purged > 0 {
			logger.Info("purged expired administrator sessions", "count", purged)
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
			return "planext4u-admin", true
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
	result := runtimeConfig{
		service: base, databaseURLFile: requiredValue(lookup, "DATABASE_URL_FILE"), databaseURL: requiredValue(lookup, "DATABASE_URL"),
		exchangeSecretFile: requiredValue(lookup, "ADMIN_EXCHANGE_SECRET_FILE"), allowedOrigins: splitCSV(requiredValue(lookup, "ADMIN_ALLOWED_ORIGINS")),
		databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute,
		sessionTTL: 8 * time.Hour, exchangeMaxSkew: time.Minute, cleanupInterval: 5 * time.Minute, cacheTTL: time.Minute, requireMFA: true,
	}
	for key, destination := range map[string]*time.Duration{
		"DATABASE_MAX_LIFETIME": &result.databaseMaxLifetime, "ADMIN_SESSION_TTL": &result.sessionTTL,
		"ADMIN_EXCHANGE_MAX_SKEW": &result.exchangeMaxSkew, "ADMIN_SESSION_CLEANUP_INTERVAL": &result.cleanupInterval,
		"ADMIN_CMS_CACHE_TTL": &result.cacheTTL,
	} {
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
	if value, ok := lookup("ADMIN_REQUIRE_MFA"); ok && strings.TrimSpace(value) != "" {
		result.requireMFA, err = strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return runtimeConfig{}, errors.New("ADMIN_REQUIRE_MFA must be a boolean")
		}
	}
	if (result.databaseURLFile == "") == (result.databaseURL == "") || result.exchangeSecretFile == "" || len(result.allowedOrigins) == 0 ||
		result.databaseMaxConns < 1 || result.databaseMaxConns > 500 || result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns ||
		result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour || result.sessionTTL < 5*time.Minute || result.sessionTTL > 24*time.Hour ||
		result.exchangeMaxSkew < time.Second || result.exchangeMaxSkew > 5*time.Minute || result.cleanupInterval < time.Minute || result.cleanupInterval > time.Hour ||
		result.cacheTTL < time.Second || result.cacheTTL > time.Hour || base.Environment == platformconfig.EnvironmentProduction && !result.requireMFA {
		return runtimeConfig{}, errors.New("required administrator service settings are missing or outside their safe range")
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

func splitCSV(value string) []string {
	result := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func adminRoute(request *http.Request) string {
	path := request.URL.Path
	switch {
	case strings.HasPrefix(path, "/v1/governance/"):
		return "/v1/governance/{resource}"
	case path == "/internal/v1/admin-sessions":
		return path
	case path == "/admin/api/v1/session", path == "/admin/api/v1/session/country", path == "/admin/api/v1/audit/events",
		path == "/admin/api/v1/operations", path == "/admin/api/v1/operations/audit", path == "/admin/api/v1/governance", path == "/admin/api/v1/cms/pages",
		path == "/admin/api/v1/cms/workspace", path == "/admin/api/v1/cms/workspace/draft":
		return path
	default:
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 6 && strings.Join(parts[:4], "/") == "admin/api/v1/operations" && parts[4] != "" && (parts[5] == "approve" || parts[5] == "reject") {
			return "/admin/api/v1/operations/{change_id}/{action}"
		}
		if len(parts) == 7 && strings.Join(parts[:5], "/") == "admin/api/v1/cms/pages" && parts[5] != "" && parts[6] == "draft" {
			return "/admin/api/v1/cms/pages/{page_id}/draft"
		}
		return "unmatched"
	}
}
