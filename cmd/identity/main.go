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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
)

type runtimeConfig struct {
	environment         string
	httpAddress         string
	databaseURLFile     string
	providerBaseURL     *url.URL
	providerMode        string
	firebaseProject     string
	firebaseAuthTenant  string
	firebaseCredentials string
	providerTimeout     time.Duration
	tenantID            string
	allowedCountries    []string
	issuer              string
	audience            string
	keyID               string
	privateKeyFile      string
	refreshHMACKeyFile  string
	contactHMACKeyFile  string
	contactKeyID        string
	contactSnapshotTTL  time.Duration
	contactMaxContacts  int
	guestHMACKeyFile    string
	accessTTL           time.Duration
	sessionTTL          time.Duration
	guestMaxSkew        time.Duration
	guestRateWindow     time.Duration
	guestRateLimit      int
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
	guestHMACKey, err := readSecretFile(config.guestHMACKeyFile, 1024)
	if err != nil {
		logger.Error("load guest-session exchange key", "error", "guest-session exchange key file is unavailable or invalid")
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
	if err := repository.DeletionReady(startupContext); err != nil {
		logger.Error("check deletion coordination schema", "error", "deletion coordination schema is unavailable")
		return 1
	}
	if config.contactHMACKeyFile != "" {
		if err := repository.ContactReady(startupContext); err != nil {
			logger.Error("check contact matching schema", "error", "contact matching schema is unavailable")
			return 1
		}
	}
	var providerVerifier identity.ProviderVerifier
	if config.providerMode == "firebase" {
		providerVerifier, err = identity.OpenFirebaseTenantVerifier(startupContext, config.firebaseProject, config.firebaseCredentials, config.firebaseAuthTenant)
	} else {
		var httpVerifier *identity.HTTPProviderVerifier
		httpVerifier, err = identity.NewHTTPProviderVerifier(identity.HTTPProviderVerifierConfig{
			BaseURL: config.providerBaseURL, AllowedProviders: providersForEnvironment(config.environment), Timeout: config.providerTimeout,
		})
		if err == nil {
			defer httpVerifier.CloseIdleConnections()
		}
		providerVerifier = httpVerifier
	}
	if err != nil {
		logger.Error("configure identity provider", "error", err)
		return 2
	}
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
	var contactSettings *identity.ContactMatchingSettings
	if config.contactHMACKeyFile != "" {
		key, keyErr := readSecretFile(config.contactHMACKeyFile, 64)
		if keyErr != nil {
			logger.Error("configure contact matching", "error", "contact key is unavailable")
			return 2
		}
		contactSettings = &identity.ContactMatchingSettings{HMACKey: key, KeyID: config.contactKeyID, SnapshotTTL: config.contactSnapshotTTL, MaxContacts: config.contactMaxContacts}
	}
	service, err := identity.NewService(identity.ServiceConfig{
		ContactMatching:  contactSettings,
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
	handler, err := identity.NewHandlerWithGuestSessions(service, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if repository.Ready(ctx) != nil {
			return false
		}
		if repository.DeletionReady(ctx) != nil {
			return false
		}
		return config.contactHMACKeyFile == "" || repository.ContactReady(ctx) == nil
	}, identity.GuestSessionHTTPConfig{
		Secret: guestHMACKey, MaxSkew: config.guestMaxSkew,
		RateLimit: config.guestRateLimit, RateWindow: config.guestRateWindow,
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
	go expireContacts(ctx, repository, logger)
	go prepareDeletionTasks(ctx, repository, logger)
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
		providerMode:        valueOrDefault(lookup, "IDENTITY_PROVIDER_MODE", "http"),
		firebaseProject:     requiredValue(lookup, "FIREBASE_PROJECT_ID"),
		firebaseAuthTenant:  requiredValue(lookup, "FIREBASE_AUTH_TENANT_ID"),
		firebaseCredentials: requiredValue(lookup, "FIREBASE_CREDENTIALS_FILE"),
		tenantID:            requiredValue(lookup, "TENANT_ID"),
		allowedCountries:    splitValues(valueOrDefault(lookup, "ALLOWED_COUNTRIES", "IN")),
		issuer:              requiredValue(lookup, "JWT_ISSUER"),
		audience:            requiredValue(lookup, "JWT_AUDIENCE"),
		keyID:               requiredValue(lookup, "JWT_KEY_ID"),
		privateKeyFile:      requiredValue(lookup, "JWT_PRIVATE_KEY_FILE"),
		refreshHMACKeyFile:  requiredValue(lookup, "REFRESH_HMAC_KEY_FILE"),
		contactHMACKeyFile:  requiredValue(lookup, "CONTACT_MATCHING_HMAC_KEY_FILE"),
		contactKeyID:        valueOrDefault(lookup, "CONTACT_MATCHING_KEY_ID", "v1"),
		contactSnapshotTTL:  24 * time.Hour,
		contactMaxContacts:  10000,
		guestHMACKeyFile:    requiredValue(lookup, "GUEST_SESSION_HMAC_KEY_FILE"),
		accessTTL:           10 * time.Minute,
		sessionTTL:          30 * 24 * time.Hour,
		guestMaxSkew:        time.Minute,
		guestRateWindow:     time.Minute,
		guestRateLimit:      120,
		shutdownTimeout:     10 * time.Second,
		logLevel:            valueOrDefault(lookup, "LOG_LEVEL", "info"),
		databaseMaxConns:    50,
		databaseMinConns:    5,
		databaseMaxLifetime: 30 * time.Minute,
	}
	if config.providerMode != "http" && config.providerMode != "firebase" || config.providerMode == "firebase" && config.firebaseProject == "" {
		return runtimeConfig{}, fmt.Errorf("IDENTITY_PROVIDER_MODE must be http or firebase; firebase requires FIREBASE_PROJECT_ID")
	}
	providerAddress := requiredValue(lookup, "PROVIDER_BASE_URL")
	if config.providerMode == "firebase" {
		providerAddress = "https://identitytoolkit.googleapis.com"
	}
	providerURL, err := url.Parse(providerAddress)
	if err != nil || !providerURL.IsAbs() || providerURL.Host == "" ||
		(providerURL.Scheme != "http" && providerURL.Scheme != "https") ||
		providerURL.User != nil || providerURL.RawQuery != "" || providerURL.Fragment != "" {
		return runtimeConfig{}, fmt.Errorf("PROVIDER_BASE_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	config.providerBaseURL = providerURL
	durationValues := map[string]*time.Duration{
		"CONTACT_MATCHING_SNAPSHOT_TTL": &config.contactSnapshotTTL,
		"PROVIDER_TIMEOUT":              &config.providerTimeout,
		"ACCESS_TTL":                    &config.accessTTL,
		"SESSION_TTL":                   &config.sessionTTL,
		"GUEST_SESSION_MAX_SKEW":        &config.guestMaxSkew,
		"GUEST_SESSION_RATE_WINDOW":     &config.guestRateWindow,
		"SHUTDOWN_TIMEOUT":              &config.shutdownTimeout,
		"DATABASE_MAX_LIFETIME":         &config.databaseMaxLifetime,
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
	if value, exists := lookup("GUEST_SESSION_RATE_LIMIT"); exists && strings.TrimSpace(value) != "" {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return runtimeConfig{}, fmt.Errorf("GUEST_SESSION_RATE_LIMIT must be an integer")
		}
		config.guestRateLimit = parsed
	}
	if value, exists := lookup("CONTACT_MATCHING_MAX_CONTACTS"); exists {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return runtimeConfig{}, fmt.Errorf("CONTACT_MATCHING_MAX_CONTACTS must be an integer")
		}
		config.contactMaxContacts = parsed
	}
	if config.contactHMACKeyFile != "" && config.providerMode != "firebase" || config.contactSnapshotTTL < 15*time.Minute || config.contactSnapshotTTL > 30*24*time.Hour || config.contactMaxContacts < 1 || config.contactMaxContacts > 10000 || len(config.contactKeyID) < 1 || len(config.contactKeyID) > 32 || strings.IndexFunc(config.contactKeyID, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-')
	}) >= 0 {
		return runtimeConfig{}, fmt.Errorf("contact matching requires Firebase, a valid key ID, bounded TTL and contact budget")
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
		config.guestHMACKeyFile == "" ||
		uuid.Validate(config.tenantID) != nil ||
		strings.ContainsAny(config.keyID, " \t\r\n") || len(config.keyID) > 128 ||
		config.providerTimeout <= 0 || config.providerTimeout > 15*time.Second ||
		config.accessTTL < time.Minute || config.accessTTL > 15*time.Minute ||
		config.sessionTTL < time.Hour || config.sessionTTL > 90*24*time.Hour ||
		config.guestMaxSkew < time.Second || config.guestMaxSkew > 5*time.Minute ||
		config.guestRateWindow < time.Second || config.guestRateWindow > time.Hour ||
		config.guestRateLimit < 1 || config.guestRateLimit > 100_000 ||
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
