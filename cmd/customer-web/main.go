package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/customerweb"
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
	allowedOrigins      []string
	identityURL         *url.URL
	platformURL         *url.URL
	guestSecretFile     string
	tokenKeyFile        string
	sessionTTL          time.Duration
	requestTimeout      time.Duration
	cleanupInterval     time.Duration
	maxRequestBytes     int64
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid customer web service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load customer web database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		logger.Error("configure customer web database", "error", "database configuration is invalid")
		return 2
	}
	poolConfig.MaxConns, poolConfig.MinConns, poolConfig.MaxConnLifetime = runtime.databaseMaxConns, runtime.databaseMinConns, runtime.databaseMaxLifetime
	startup, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startup, poolConfig)
	if err != nil || pool.Ping(startup) != nil {
		logger.Error("connect customer web database", "error", "database is unavailable")
		return 1
	}
	defer pool.Close()
	guestSecret, err := readSecret(runtime.guestSecretFile, 4096)
	if err != nil || len(guestSecret) < 32 {
		logger.Error("load guest-session authentication key", "error", "guest-session key is unavailable or invalid")
		return 2
	}
	tokenKeyValue, err := readSecret(runtime.tokenKeyFile, 128)
	if err != nil {
		logger.Error("load session-token encryption key", "error", "session-token encryption key is unavailable or invalid")
		return 2
	}
	tokenKey, err := decodeTokenKey(tokenKeyValue)
	if err != nil {
		logger.Error("load session-token encryption key", "error", "session-token encryption key is unavailable or invalid")
		return 2
	}
	tokenCipher, err := customerweb.NewTokenCipher(tokenKey)
	if err != nil {
		logger.Error("configure session-token encryption", "error", err)
		return 2
	}
	sessions, err := customerweb.NewPostgresSessionStore(pool, time.Now, tokenCipher)
	if err != nil || sessions.Ready(startup) != nil {
		logger.Error("check customer web session repository", "error", "customer web session schema is unavailable")
		return 1
	}
	httpClient := &http.Client{Timeout: runtime.requestTimeout, Transport: hardenedTransport()}
	identityClient, err := customerweb.NewHTTPIdentityClient(customerweb.HTTPIdentityClientConfig{
		BaseURL: runtime.identityURL, HTTPClient: httpClient, GuestSecret: guestSecret, Clock: time.Now, MaxBytes: runtime.maxRequestBytes,
	})
	if err != nil {
		logger.Error("configure identity client", "error", err)
		return 2
	}
	platformProxy := httputil.NewSingleHostReverseProxy(runtime.platformURL)
	platformProxy.Transport = hardenedTransport()
	platformProxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":{"code":"CUSTOMER_PLATFORM_UNAVAILABLE","message":"The platform is temporarily unavailable.","retryable":true}}`))
	}
	platformProxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		response.Header.Del("Access-Control-Allow-Origin")
		response.Header.Del("Access-Control-Allow-Credentials")
		return nil
	}
	application, err := customerweb.NewHandler(customerweb.Config{
		Sessions: sessions, Identity: identityClient, Platform: platformProxy, Clock: time.Now,
		AllowedOrigins: runtime.allowedOrigins, SessionTTL: runtime.sessionTTL, MaxRequestBytes: runtime.maxRequestBytes,
	})
	if err != nil {
		logger.Error("configure customer web handler", "error", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sessionCleanupLoop(ctx, logger, sessions, runtime.cleanupInterval)
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
		shutdown, cancel := context.WithTimeout(context.Background(), runtime.service.ShutdownTimeout)
		defer cancel()
		if err := observability.Shutdown(shutdown); err != nil {
			logger.Warn("flush telemetry", "error", err)
		}
	}()
	readiness := func(ctx context.Context) error { return sessions.Ready(ctx) }
	httpServer := server.New(runtime.service, logger, version,
		server.WithTelemetry(observability), server.WithApplication(application, customerWebRoute), server.WithReadiness(readiness))
	logger.Info("customer web service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("customer web service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("customer web service stopped")
	return 0
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	wrapped := func(key string) (string, bool) {
		switch key {
		case "SERVICE_NAME":
			if value := requiredValue(lookup, key); value != "" {
				return value, true
			}
			return "planext4u-customer-web", true
		case "HTTP_ADDRESS":
			if value := requiredValue(lookup, key); value != "" {
				return value, true
			}
			return ":8091", true
		default:
			return lookup(key)
		}
	}
	base, err := platformconfig.LoadFrom(wrapped)
	if err != nil {
		return runtimeConfig{}, err
	}
	identityURL, err := parseInternalURL(requiredValue(lookup, "IDENTITY_BASE_URL"))
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("IDENTITY_BASE_URL: %w", err)
	}
	platformURL, err := parseInternalURL(requiredValue(lookup, "PLATFORM_GATEWAY_URL"))
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("PLATFORM_GATEWAY_URL: %w", err)
	}
	result := runtimeConfig{
		service: base, databaseURLFile: requiredValue(lookup, "DATABASE_URL_FILE"), databaseURL: requiredValue(lookup, "DATABASE_URL"),
		databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute,
		allowedOrigins: splitCSV(requiredValue(lookup, "CUSTOMER_WEB_ALLOWED_ORIGINS")), identityURL: identityURL, platformURL: platformURL,
		guestSecretFile: requiredValue(lookup, "GUEST_SESSION_HMAC_KEY_FILE"), tokenKeyFile: requiredValue(lookup, "CUSTOMER_WEB_TOKEN_KEY_FILE"),
		sessionTTL: 7 * 24 * time.Hour, requestTimeout: 15 * time.Second, cleanupInterval: 5 * time.Minute, maxRequestBytes: 1 << 20,
	}
	for key, destination := range map[string]*time.Duration{
		"DATABASE_MAX_LIFETIME": &result.databaseMaxLifetime, "CUSTOMER_WEB_SESSION_TTL": &result.sessionTTL,
		"CUSTOMER_WEB_REQUEST_TIMEOUT": &result.requestTimeout, "CUSTOMER_WEB_CLEANUP_INTERVAL": &result.cleanupInterval,
	} {
		if value := requiredValue(lookup, key); value != "" {
			parsed, parseErr := time.ParseDuration(value)
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
			}
			*destination = parsed
		}
	}
	for key, destination := range map[string]*int32{"DATABASE_MAX_CONNS": &result.databaseMaxConns, "DATABASE_MIN_CONNS": &result.databaseMinConns} {
		if value := requiredValue(lookup, key); value != "" {
			parsed, parseErr := strconv.ParseInt(value, 10, 32)
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
			*destination = int32(parsed)
		}
	}
	if value := requiredValue(lookup, "CUSTOMER_WEB_MAX_REQUEST_BYTES"); value != "" {
		result.maxRequestBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return runtimeConfig{}, errors.New("CUSTOMER_WEB_MAX_REQUEST_BYTES must be an integer")
		}
	}
	if (result.databaseURLFile == "") == (result.databaseURL == "") || len(result.allowedOrigins) == 0 || result.guestSecretFile == "" || result.tokenKeyFile == "" ||
		result.databaseMaxConns < 1 || result.databaseMaxConns > 500 || result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns ||
		result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour || result.sessionTTL < 5*time.Minute || result.sessionTTL > 30*24*time.Hour ||
		result.requestTimeout < time.Second || result.requestTimeout > time.Minute || result.cleanupInterval < time.Minute || result.cleanupInterval > time.Hour ||
		result.maxRequestBytes < 1024 || result.maxRequestBytes > 2<<20 {
		return runtimeConfig{}, errors.New("required customer web service settings are missing or outside their safe range")
	}
	for _, origin := range result.allowedOrigins {
		parsed, parseErr := url.Parse(origin)
		loopbackHTTP := base.Environment == platformconfig.EnvironmentDevelopment && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
		if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "https" && !loopbackHTTP) || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return runtimeConfig{}, errors.New("CUSTOMER_WEB_ALLOWED_ORIGINS must contain HTTPS origins")
		}
	}
	return result, nil
}

func parseInternalURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return parsed, nil
}

func hardenedTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return transport
}

func loadDatabaseURL(runtime runtimeConfig) (string, error) {
	if runtime.databaseURL != "" {
		return runtime.databaseURL, nil
	}
	value, err := readSecret(runtime.databaseURLFile, 4096)
	return string(value), err
}

func readSecret(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxBytes {
		return nil, errors.New("secret file is unavailable or invalid")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(value), nil
}

func decodeTokenKey(value []byte) ([]byte, error) {
	if len(value) == 32 {
		return append([]byte(nil), value...), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(string(value))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(string(value))
	}
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("session-token encryption key must contain 32 raw or base64-encoded bytes")
	}
	return decoded, nil
}

func requiredValue(lookup func(string) (string, bool), key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func sessionCleanupLoop(ctx context.Context, logger *slog.Logger, sessions *customerweb.PostgresSessionStore, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if purged, err := sessions.PurgeExpired(ctx, 1000); err != nil && ctx.Err() == nil {
			logger.Warn("purge expired customer web sessions", "error", err)
		} else if purged > 0 {
			logger.Info("purged expired customer web sessions", "count", purged)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func customerWebRoute(request *http.Request) string {
	switch {
	case request.URL.Path == "/web/v1/customer/session":
		return "/web/v1/customer/session"
	case request.URL.Path == "/web/v1/customer/session/refresh":
		return "/web/v1/customer/session/refresh"
	case strings.HasPrefix(request.URL.Path, "/platform-api/"):
		return "/platform-api/*"
	default:
		return "unmatched"
	}
}
