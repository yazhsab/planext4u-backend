package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
)

type runtimeConfig struct {
	environment         string
	httpAddress         string
	databaseURLFile     string
	providerBaseURL     *url.URL
	providerTimeout     time.Duration
	tenantID            string
	allowedCountries    []string
	issuer              string
	audience            string
	keyID               string
	privateKeyFile      string
	refreshHMACKeyFile  string
	accessTTL           time.Duration
	sessionTTL          time.Duration
	shutdownTimeout     time.Duration
	logLevel            string
	databaseMaxConns    int32
	databaseMinConns    int32
	databaseMaxLifetime time.Duration
}

func main() {
	os.Exit(run())
}

func run() int {
	config, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid identity configuration", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, config.logLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	privateKeyPEM, err := readSecretFile(config.privateKeyFile, 64*1024)
	if err != nil {
		logger.Error("load signing key", "error", "signing key file is unavailable or invalid")
		return 2
	}
	privateKey, err := identity.ParseRSAPrivateKeyPEM(privateKeyPEM)
	if err != nil {
		logger.Error("load signing key", "error", "signing key is invalid")
		return 2
	}
	refreshHMACKey, err := readSecretFile(config.refreshHMACKeyFile, 1024)
	if err != nil {
		logger.Error("load refresh hashing key", "error", "refresh hashing key file is unavailable or invalid")
		return 2
	}
	refreshHasher, err := identity.NewHMACRefreshHasher(refreshHMACKey)
	if err != nil {
		logger.Error("configure refresh hashing", "error", err)
		return 2
	}
	databaseURL, err := readSecretFile(config.databaseURLFile, 4096)
	if err != nil {
		logger.Error("load database configuration", "error", "database URL file is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(strings.TrimSpace(string(databaseURL)), config)
	if err != nil {
		logger.Error("configure identity database", "error", err)
		return 2
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		logger.Error("create identity database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStartup()
	if err := pool.Ping(startupContext); err != nil {
		logger.Error("connect identity database", "error", "database is unavailable")
		return 1
	}
	repository, err := identity.NewPostgresRepository(pool)
	if err != nil {
		logger.Error("configure identity repository", "error", err)
		return 2
	}
	if err := repository.Ready(startupContext); err != nil {
		logger.Error("check identity schema", "error", "identity schema is unavailable")
		return 1
	}
	providerVerifier, err := identity.NewHTTPProviderVerifier(identity.HTTPProviderVerifierConfig{
		BaseURL:          config.providerBaseURL,
		AllowedProviders: providersForEnvironment(config.environment),
		Timeout:          config.providerTimeout,
	})
	if err != nil {
		logger.Error("configure identity provider", "error", err)
		return 2
	}
	defer providerVerifier.CloseIdleConnections()
	tokenIssuer, err := identity.NewJWTIssuer(identity.JWTIssuerConfig{
		Issuer:    config.issuer,
		Audience:  config.audience,
		KeyID:     config.keyID,
		Key:       privateKey,
		AccessTTL: config.accessTTL,
	})
	if err != nil {
		logger.Error("configure access-token issuer", "error", err)
		return 2
	}
	service, err := identity.NewService(identity.ServiceConfig{
		TenantID:         config.tenantID,
		AllowedCountries: config.allowedCountries,
		SessionTTL:       config.sessionTTL,
		Repository:       repository,
		ProviderVerifier: providerVerifier,
		TokenIssuer:      tokenIssuer,
		RefreshFactory:   identity.RandomRefreshTokenFactory{},
		RefreshHasher:    refreshHasher,
	})
	if err != nil {
		logger.Error("configure identity service", "error", err)
		return 2
	}
	handler, err := identity.NewHandler(service, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return repository.Ready(ctx) == nil
	})
	if err != nil {
		logger.Error("configure identity HTTP handler", "error", err)
		return 2
	}
	server := &http.Server{
		Addr:              config.httpAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- server.ListenAndServe() }()
	logger.Info("identity service starting", "environment", config.environment, "address", config.httpAddress)
	select {
	case err := <-errorsChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("identity service stopped unexpectedly", "error", err)
			return 1
		}
	case <-ctx.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), config.shutdownTimeout)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("identity service shutdown failed", "error", err)
			return 1
		}
		if err := <-errorsChannel; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("identity service failed during shutdown", "error", err)
			return 1
		}
	}
	logger.Info("identity service stopped")
	return 0
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	config := runtimeConfig{
		environment:         valueOrDefault(lookup, "APP_ENV", "development"),
		httpAddress:         valueOrDefault(lookup, "HTTP_ADDRESS", ":8083"),
		databaseURLFile:     requiredValue(lookup, "DATABASE_URL_FILE"),
		providerTimeout:     3 * time.Second,
		tenantID:            requiredValue(lookup, "TENANT_ID"),
		allowedCountries:    splitValues(valueOrDefault(lookup, "ALLOWED_COUNTRIES", "IN")),
		issuer:              requiredValue(lookup, "JWT_ISSUER"),
		audience:            requiredValue(lookup, "JWT_AUDIENCE"),
		keyID:               requiredValue(lookup, "JWT_KEY_ID"),
		privateKeyFile:      requiredValue(lookup, "JWT_PRIVATE_KEY_FILE"),
		refreshHMACKeyFile:  requiredValue(lookup, "REFRESH_HMAC_KEY_FILE"),
		accessTTL:           10 * time.Minute,
		sessionTTL:          30 * 24 * time.Hour,
		shutdownTimeout:     10 * time.Second,
		logLevel:            valueOrDefault(lookup, "LOG_LEVEL", "info"),
		databaseMaxConns:    50,
		databaseMinConns:    5,
		databaseMaxLifetime: 30 * time.Minute,
	}
	providerURL, err := url.Parse(requiredValue(lookup, "PROVIDER_BASE_URL"))
	if err != nil || !providerURL.IsAbs() || providerURL.Host == "" ||
		(providerURL.Scheme != "http" && providerURL.Scheme != "https") ||
		providerURL.User != nil || providerURL.RawQuery != "" || providerURL.Fragment != "" {
		return runtimeConfig{}, fmt.Errorf("PROVIDER_BASE_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	config.providerBaseURL = providerURL
	durationValues := map[string]*time.Duration{
		"PROVIDER_TIMEOUT":      &config.providerTimeout,
		"ACCESS_TTL":            &config.accessTTL,
		"SESSION_TTL":           &config.sessionTTL,
		"SHUTDOWN_TIMEOUT":      &config.shutdownTimeout,
		"DATABASE_MAX_LIFETIME": &config.databaseMaxLifetime,
	}
	for key, destination := range durationValues {
		if value, exists := lookup(key); exists && strings.TrimSpace(value) != "" {
			parsed, parseErr := time.ParseDuration(strings.TrimSpace(value))
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
			}
			*destination = parsed
		}
	}
	for key, destination := range map[string]*int32{
		"DATABASE_MAX_CONNS": &config.databaseMaxConns,
		"DATABASE_MIN_CONNS": &config.databaseMinConns,
	} {
		if value, exists := lookup(key); exists && strings.TrimSpace(value) != "" {
			parsed, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
			*destination = int32(parsed)
		}
	}
	if config.environment != "development" && config.environment != "staging" && config.environment != "production" {
		return runtimeConfig{}, fmt.Errorf("APP_ENV must be development, staging, or production")
	}
	if config.environment != "development" && (providerURL.Scheme != "https" || !strings.HasPrefix(config.issuer, "https://")) {
		return runtimeConfig{}, fmt.Errorf("provider and issuer must use HTTPS outside development")
	}
	for _, country := range config.allowedCountries {
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return runtimeConfig{}, fmt.Errorf("ALLOWED_COUNTRIES must contain ISO 3166-1 alpha-2 values")
		}
	}
	if _, _, err := net.SplitHostPort(config.httpAddress); err != nil {
		return runtimeConfig{}, fmt.Errorf("HTTP_ADDRESS must contain a valid host and port")
	}
	if config.databaseURLFile == "" || config.tenantID == "" || config.issuer == "" ||
		config.audience == "" || config.keyID == "" || config.privateKeyFile == "" ||
		config.refreshHMACKeyFile == "" || len(config.allowedCountries) == 0 ||
		strings.ContainsAny(config.tenantID, " \t\r\n") || len(config.tenantID) > 128 ||
		strings.ContainsAny(config.keyID, " \t\r\n") || len(config.keyID) > 128 ||
		config.providerTimeout <= 0 || config.providerTimeout > 15*time.Second ||
		config.accessTTL < time.Minute || config.accessTTL > 15*time.Minute ||
		config.sessionTTL < time.Hour || config.sessionTTL > 90*24*time.Hour ||
		config.shutdownTimeout <= 0 || config.shutdownTimeout > 2*time.Minute ||
		config.databaseMaxConns < 1 || config.databaseMaxConns > 500 ||
		config.databaseMinConns < 0 || config.databaseMinConns > config.databaseMaxConns ||
		config.databaseMaxLifetime < time.Minute || config.databaseMaxLifetime > 24*time.Hour {
		return runtimeConfig{}, fmt.Errorf("required identity configuration is missing or outside its safe range")
	}
	return config, nil
}

func databaseConfig(value string, runtime runtimeConfig) (*pgxpool.Config, error) {
	parsedURL, parseErr := url.Parse(value)
	if parseErr != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") || parsedURL.Host == "" {
		return nil, fmt.Errorf("database URL file is invalid")
	}
	if runtime.environment != "development" && parsedURL.Query().Get("sslmode") != "verify-full" {
		return nil, fmt.Errorf("database URL must use sslmode=verify-full outside development")
	}
	config, err := pgxpool.ParseConfig(value)
	if err != nil {
		return nil, fmt.Errorf("database URL file is invalid")
	}
	if runtime.environment != "development" && config.ConnConfig.TLSConfig == nil {
		return nil, fmt.Errorf("database URL must require TLS outside development")
	}
	config.MaxConns = runtime.databaseMaxConns
	config.MinConns = runtime.databaseMinConns
	config.MaxConnLifetime = runtime.databaseMaxLifetime
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	return config, nil
}

func providersForEnvironment(environment string) []string {
	if environment == "development" {
		return []string{"firebase", "oidc", "local"}
	}
	return []string{"firebase", "oidc"}
}

func readSecretFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(contents)) > maxBytes || len(contents) == 0 {
		return nil, errors.New("secret file unavailable or outside size limit")
	}
	return contents, nil
}

func splitValues(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if _, exists := seen[part]; !exists {
			seen[part] = struct{}{}
			result = append(result, part)
		}
	}
	return result
}

func requiredValue(lookup func(string) (string, bool), key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func valueOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value, exists := lookup(key); exists && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
