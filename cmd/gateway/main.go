package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/redis/go-redis/v9"
	"github.com/yazhsab/planext4u-backend/internal/gateway"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
)

type runtimeConfig struct {
	environment        string
	httpAddress        string
	upstreamURL        *url.URL
	upstreamRoutes     map[string]*url.URL
	anonymousRoutes    map[string]map[string]struct{}
	issuer             string
	audience           string
	keyID              string
	publicKeyFile      string
	redisURLFile       string
	requestTimeout     time.Duration
	shutdownTimeout    time.Duration
	maxRequestBytes    int64
	IPRateLimit        int64
	guestRateLimit     int64
	principalRateLimit int64
	rateWindow         time.Duration
	logLevel           string
}

func main() {
	os.Exit(run())
}

func run() int {
	config, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid gateway configuration", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, config.logLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}

	publicKeyPEM, err := os.ReadFile(config.publicKeyFile)
	if err != nil || len(publicKeyPEM) > 64*1024 {
		logger.Error("load JWT public key", "error", "public key file is unavailable or too large")
		return 2
	}
	publicKey, err := gateway.ParseRSAPublicKeyPEM(publicKeyPEM)
	if err != nil {
		logger.Error("load JWT public key", "error", err)
		return 2
	}
	verifier, err := gateway.NewJWTVerifier(gateway.JWTVerifierConfig{
		Issuer:    config.issuer,
		Audience:  config.audience,
		Keys:      map[string]*rsa.PublicKey{config.keyID: publicKey},
		ClockSkew: 30 * time.Second,
	})
	if err != nil {
		logger.Error("configure JWT verifier", "error", err)
		return 2
	}

	redisURL, err := os.ReadFile(config.redisURLFile)
	if err != nil || len(redisURL) > 4096 {
		logger.Error("load Redis configuration", "error", "Redis URL file is unavailable or too large")
		return 2
	}
	redisOptions, err := redisOptionsForEnvironment(strings.TrimSpace(string(redisURL)), config.environment)
	if err != nil {
		logger.Error("load Redis configuration", "error", err)
		return 2
	}
	redisOptions.DialTimeout = 3 * time.Second
	redisOptions.ReadTimeout = 2 * time.Second
	redisOptions.WriteTimeout = 2 * time.Second
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStartup()
	if err := redisClient.Ping(startupContext).Err(); err != nil {
		logger.Error("connect rate-limit store", "error", "Redis is unavailable")
		return 1
	}

	anonymousLimiter, err := gateway.NewRedisLimiter(
		redisClient,
		"planext4u:"+config.environment+":gateway:ip",
		config.IPRateLimit,
		config.rateWindow,
	)
	if err != nil {
		logger.Error("configure anonymous limiter", "error", err)
		return 2
	}
	principalLimiter, err := gateway.NewRedisLimiter(
		redisClient,
		"planext4u:"+config.environment+":gateway:principal",
		config.principalRateLimit,
		config.rateWindow,
	)
	if err != nil {
		logger.Error("configure principal limiter", "error", err)
		return 2
	}
	guestLimiter, err := gateway.NewRedisLimiter(
		redisClient,
		"planext4u:"+config.environment+":gateway:guest",
		config.guestRateLimit,
		config.rateWindow,
	)
	if err != nil {
		logger.Error("configure guest limiter", "error", err)
		return 2
	}

	handlerConfig := gateway.DefaultConfig(config.upstreamURL)
	handlerConfig.UpstreamRoutes = config.upstreamRoutes
	for path, methods := range config.anonymousRoutes {
		handlerConfig.AnonymousRoutes[path] = methods
	}
	handlerConfig.RequestTimeout = config.requestTimeout
	handlerConfig.MaxRequestBytes = config.maxRequestBytes
	handlerConfig.AnonymousLimiter = anonymousLimiter
	handlerConfig.GuestLimiter = guestLimiter
	handlerConfig.PrincipalLimiter = principalLimiter
	handlerConfig.Readiness = func(ctx context.Context) error {
		return redisClient.Ping(ctx).Err()
	}
	handler, err := gateway.NewHandler(handlerConfig, verifier, logger)
	if err != nil {
		logger.Error("configure gateway handler", "error", err)
		return 2
	}
	defer handler.CloseIdleConnections()

	server := &http.Server{
		Addr:              config.httpAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      config.requestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsChannel := make(chan error, 1)
	go func() {
		errorsChannel <- server.ListenAndServe()
	}()
	logger.Info("gateway starting", "environment", config.environment, "address", config.httpAddress)

	select {
	case err := <-errorsChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway stopped unexpectedly", "error", err)
			return 1
		}
	case <-ctx.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), config.shutdownTimeout)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("gateway shutdown failed", "error", err)
			return 1
		}
		if err := <-errorsChannel; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway serve failed during shutdown", "error", err)
			return 1
		}
	}
	logger.Info("gateway stopped")
	return 0
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	config := runtimeConfig{
		environment:        valueOrDefault(lookup, "APP_ENV", "development"),
		httpAddress:        valueOrDefault(lookup, "HTTP_ADDRESS", ":8082"),
		issuer:             requiredValue(lookup, "JWT_ISSUER"),
		audience:           requiredValue(lookup, "JWT_AUDIENCE"),
		keyID:              requiredValue(lookup, "JWT_KEY_ID"),
		publicKeyFile:      requiredValue(lookup, "JWT_PUBLIC_KEY_FILE"),
		redisURLFile:       requiredValue(lookup, "REDIS_URL_FILE"),
		requestTimeout:     10 * time.Second,
		shutdownTimeout:    10 * time.Second,
		maxRequestBytes:    1 << 20,
		IPRateLimit:        600,
		guestRateLimit:     120,
		principalRateLimit: 300,
		rateWindow:         time.Minute,
		logLevel:           valueOrDefault(lookup, "LOG_LEVEL", "info"),
	}
	upstreamValue := requiredValue(lookup, "UPSTREAM_URL")
	parsedUpstream, err := serviceURL(upstreamValue, config.environment)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("UPSTREAM_URL %w", err)
	}
	config.upstreamURL = parsedUpstream
	config.upstreamRoutes, err = upstreamRoutes(requiredValue(lookup, "UPSTREAM_ROUTES"), config.environment)
	if err != nil {
		return runtimeConfig{}, err
	}
	config.anonymousRoutes, err = anonymousRoutes(requiredValue(lookup, "ANONYMOUS_ROUTES"))
	if err != nil {
		return runtimeConfig{}, err
	}

	if value, exists := lookup("REQUEST_TIMEOUT"); exists {
		config.requestTimeout, err = time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("REQUEST_TIMEOUT must be a duration")
		}
	}
	if value, exists := lookup("SHUTDOWN_TIMEOUT"); exists {
		config.shutdownTimeout, err = time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a duration")
		}
	}
	if value, exists := lookup("MAX_REQUEST_BYTES"); exists {
		config.maxRequestBytes, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("MAX_REQUEST_BYTES must be an integer")
		}
	}
	if value, exists := lookup("IP_RATE_LIMIT"); exists {
		config.IPRateLimit, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("IP_RATE_LIMIT must be an integer")
		}
	}
	if value, exists := lookup("PRINCIPAL_RATE_LIMIT"); exists {
		config.principalRateLimit, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("PRINCIPAL_RATE_LIMIT must be an integer")
		}
	}
	if value, exists := lookup("GUEST_RATE_LIMIT"); exists {
		config.guestRateLimit, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("GUEST_RATE_LIMIT must be an integer")
		}
	}
	if value, exists := lookup("RATE_WINDOW"); exists {
		config.rateWindow, err = time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("RATE_WINDOW must be a duration")
		}
	}
	if config.environment != "development" && !strings.HasPrefix(config.issuer, "https://") {
		return runtimeConfig{}, fmt.Errorf("JWT_ISSUER must use HTTPS outside development")
	}
	if config.environment != "development" && strings.HasSuffix(strings.ToLower(config.redisURLFile), ".env") {
		return runtimeConfig{}, fmt.Errorf("REDIS_URL_FILE must reference a mounted secret, not an environment file")
	}
	if config.environment != "development" && config.environment != "staging" && config.environment != "production" {
		return runtimeConfig{}, fmt.Errorf("APP_ENV must be development, staging, or production")
	}
	if _, _, err := net.SplitHostPort(config.httpAddress); err != nil {
		return runtimeConfig{}, fmt.Errorf("HTTP_ADDRESS must contain a valid host and port")
	}
	if config.issuer == "" || config.audience == "" || config.keyID == "" ||
		config.publicKeyFile == "" || config.redisURLFile == "" ||
		config.requestTimeout <= 0 || config.requestTimeout > time.Minute ||
		config.shutdownTimeout <= 0 || config.shutdownTimeout > 2*time.Minute ||
		config.maxRequestBytes < 1 || config.maxRequestBytes > 16<<20 ||
		config.IPRateLimit < 1 || config.IPRateLimit > 1_000_000 ||
		config.guestRateLimit < 1 || config.guestRateLimit > 1_000_000 ||
		config.principalRateLimit < 1 || config.principalRateLimit > 1_000_000 ||
		config.rateWindow < time.Second || config.rateWindow > 24*time.Hour {
		return runtimeConfig{}, fmt.Errorf("required gateway configuration is missing or outside its safe range")
	}
	return config, nil
}

func anonymousRoutes(value string) (map[string]map[string]struct{}, error) {
	result := map[string]map[string]struct{}{}
	if value == "" {
		return result, nil
	}
	if len(value) > 32*1024 {
		return nil, fmt.Errorf("ANONYMOUS_ROUTES is too large")
	}
	var encoded map[string][]string
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&encoded) != nil || len(encoded) == 0 || len(encoded) > 32 {
		return nil, fmt.Errorf("ANONYMOUS_ROUTES must be a JSON object with at most 32 routes")
	}
	for path, methods := range encoded {
		if !safeRoutePrefix(path) || len(methods) == 0 || len(methods) > 4 {
			return nil, fmt.Errorf("ANONYMOUS_ROUTES contains an invalid route")
		}
		allowed := map[string]struct{}{}
		for _, method := range methods {
			method = strings.ToUpper(strings.TrimSpace(method))
			if method != http.MethodPost {
				return nil, fmt.Errorf("ANONYMOUS_ROUTES only permits POST routes")
			}
			allowed[method] = struct{}{}
		}
		result[path] = allowed
	}
	return result, nil
}

func upstreamRoutes(value, environment string) (map[string]*url.URL, error) {
	result := map[string]*url.URL{}
	if value == "" {
		return result, nil
	}
	if len(value) > 64*1024 {
		return nil, fmt.Errorf("UPSTREAM_ROUTES is too large")
	}
	var encoded map[string]string
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&encoded) != nil || len(encoded) == 0 || len(encoded) > 64 {
		return nil, fmt.Errorf("UPSTREAM_ROUTES must be a JSON object with at most 64 routes")
	}
	for prefix, rawURL := range encoded {
		if !safeRoutePrefix(prefix) {
			return nil, fmt.Errorf("UPSTREAM_ROUTES contains an invalid path prefix")
		}
		parsed, err := serviceURL(rawURL, environment)
		if err != nil {
			return nil, fmt.Errorf("UPSTREAM_ROUTES %s %w", prefix, err)
		}
		result[prefix] = parsed
	}
	return result, nil
}

func serviceURL(value, environment string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("must be an absolute HTTP(S) service URL without credentials, query, fragment, or path")
	}
	if environment != "development" && parsed.Scheme != "https" && !privateServiceHost(parsed.Hostname()) {
		return nil, fmt.Errorf("must use HTTPS unless it targets a private service-discovery host")
	}
	return parsed, nil
}

func privateServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || net.ParseIP(host) != nil || host == "localhost" {
		return false
	}
	if strings.HasSuffix(host, ".internal") {
		return true
	}
	if strings.Contains(host, ".") {
		return false
	}
	for _, character := range host {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return host[0] != '-' && host[len(host)-1] != '-'
}

func safeRoutePrefix(value string) bool {
	if value == "" || value == "/" || value[0] != '/' || strings.HasSuffix(value, "/") || strings.ContainsAny(value, "?#") {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character >= 0x7f {
			return false
		}
	}
	return true
}

func redisOptionsForEnvironment(value, environment string) (*redis.Options, error) {
	options, err := redis.ParseURL(value)
	if err != nil {
		return nil, fmt.Errorf("Redis URL file is invalid")
	}
	if environment != "development" && options.TLSConfig == nil {
		return nil, fmt.Errorf("Redis URL must use TLS outside development")
	}
	return options, nil
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
