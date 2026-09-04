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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/media"
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
	region              string
	bucket              string
	s3Endpoint          *url.URL
	s3ForcePathStyle    bool
	s3AccessKeyFile     string
	s3SecretKeyFile     string
	scannerURL          *url.URL
	scannerTokenFile    string
	scannerTimeout      time.Duration
	uploadTTL           time.Duration
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid media service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load media database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure media database", "error", err)
		return 2
	}
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startupContext, poolConfig)
	if err != nil {
		logger.Error("create media database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	if err := pool.Ping(startupContext); err != nil {
		logger.Error("connect media database", "error", "database is unavailable")
		return 1
	}
	repository, err := media.NewPostgresRepository(pool)
	if err != nil || repository.Ready(startupContext) != nil {
		logger.Error("check media repository", "error", "media schema is unavailable")
		return 1
	}
	objectStore, err := configureObjectStore(startupContext, runtime)
	if err != nil {
		logger.Error("configure media object store", "error", "object-store configuration is unavailable or invalid")
		return 2
	}
	if err := objectStore.Ready(startupContext); err != nil {
		logger.Error("check media object store", "error", "media bucket is unavailable")
		return 1
	}
	scannerToken, err := readRegularFile(runtime.scannerTokenFile, 4096)
	if err != nil {
		logger.Error("load malware scanner credential", "error", "scanner credential is unavailable or invalid")
		return 2
	}
	scanner, err := media.NewHTTPMalwareScanner(runtime.scannerURL, &http.Client{Timeout: runtime.scannerTimeout}, strings.TrimSpace(string(scannerToken)))
	if err != nil {
		logger.Error("configure malware scanner", "error", err)
		return 2
	}
	service, err := media.NewService(repository, objectStore, objectStore, scanner, runtime.uploadTTL, time.Now)
	if err != nil {
		logger.Error("configure media service", "error", err)
		return 2
	}
	application, err := media.NewHandler(service)
	if err != nil {
		logger.Error("configure media handler", "error", err)
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
	readiness := func(ctx context.Context) error {
		if err := repository.Ready(ctx); err != nil {
			return err
		}
		return objectStore.Ready(ctx)
	}
	httpServer := server.New(runtime.service, logger, version,
		server.WithTelemetry(observability), server.WithApplication(application, mediaRoute), server.WithReadiness(readiness))
	logger.Info("media service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("media service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("media service stopped")
	return 0
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	wrappedLookup := func(key string) (string, bool) {
		switch key {
		case "SERVICE_NAME":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return "planext4u-media", true
		case "HTTP_ADDRESS":
			if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
				return value, true
			}
			return ":8087", true
		default:
			return lookup(key)
		}
	}
	base, err := platformconfig.LoadFrom(wrappedLookup)
	if err != nil {
		return runtimeConfig{}, err
	}
	result := runtimeConfig{
		service: base, databaseURLFile: requiredValue(lookup, "DATABASE_URL_FILE"),
		databaseURL: requiredValue(lookup, "DATABASE_URL"), region: requiredValue(lookup, "AWS_REGION"),
		bucket: requiredValue(lookup, "MEDIA_BUCKET"), s3AccessKeyFile: requiredValue(lookup, "S3_ACCESS_KEY_ID_FILE"),
		s3SecretKeyFile:  requiredValue(lookup, "S3_SECRET_ACCESS_KEY_FILE"),
		scannerTokenFile: requiredValue(lookup, "MALWARE_SCANNER_TOKEN_FILE"), scannerTimeout: 10 * time.Second,
		uploadTTL: 10 * time.Minute, databaseMaxConns: 50, databaseMinConns: 5, databaseMaxLifetime: 30 * time.Minute,
	}
	if raw := requiredValue(lookup, "S3_ENDPOINT"); raw != "" {
		result.s3Endpoint, err = parseEndpoint(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("S3_ENDPOINT is invalid")
		}
	}
	result.scannerURL, err = parseEndpoint(requiredValue(lookup, "MALWARE_SCANNER_URL"))
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("MALWARE_SCANNER_URL is invalid")
	}
	if raw := requiredValue(lookup, "S3_FORCE_PATH_STYLE"); raw != "" {
		result.s3ForcePathStyle, err = strconv.ParseBool(raw)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("S3_FORCE_PATH_STYLE must be boolean")
		}
	}
	for key, destination := range map[string]*time.Duration{
		"DATABASE_MAX_LIFETIME":   &result.databaseMaxLifetime,
		"MALWARE_SCANNER_TIMEOUT": &result.scannerTimeout,
		"MEDIA_UPLOAD_TTL":        &result.uploadTTL,
	} {
		if raw := requiredValue(lookup, key); raw != "" {
			*destination, err = time.ParseDuration(raw)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
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
	staticCredentialFilesValid := (result.s3AccessKeyFile == "") == (result.s3SecretKeyFile == "")
	if (result.databaseURLFile == "") == (result.databaseURL == "") || !staticCredentialFilesValid ||
		!regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]+$`).MatchString(result.region) || result.bucket == "" ||
		result.scannerTokenFile == "" || result.scannerTimeout < time.Second || result.scannerTimeout > time.Minute ||
		result.uploadTTL < time.Minute || result.uploadTTL > 30*time.Minute || result.databaseMaxConns < 1 || result.databaseMaxConns > 500 ||
		result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns || result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour {
		return runtimeConfig{}, errors.New("required media service settings are missing or outside their safe range")
	}
	if base.Environment != platformconfig.EnvironmentDevelopment &&
		((result.s3Endpoint != nil && result.s3Endpoint.Scheme != "https") || result.scannerURL.Scheme != "https") {
		return runtimeConfig{}, errors.New("media dependencies must use HTTPS outside development")
	}
	return result, nil
}

func configureObjectStore(ctx context.Context, runtime runtimeConfig) (*media.S3Adapter, error) {
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(runtime.region),
		awsconfig.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
	}
	if runtime.s3AccessKeyFile != "" {
		accessKey, err := readRegularFile(runtime.s3AccessKeyFile, 1024)
		if err != nil {
			return nil, err
		}
		secretKey, err := readRegularFile(runtime.s3SecretKeyFile, 4096)
		if err != nil {
			return nil, err
		}
		access, secret := strings.TrimSpace(string(accessKey)), strings.TrimSpace(string(secretKey))
		if len(access) < 3 || len(secret) < 8 {
			return nil, errors.New("static object-store credentials are invalid")
		}
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, "")))
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.UsePathStyle = runtime.s3ForcePathStyle
		if runtime.s3Endpoint != nil {
			options.BaseEndpoint = aws.String(runtime.s3Endpoint.String())
		}
	})
	return media.NewS3Adapter(client, runtime.bucket, time.Now)
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

func parseEndpoint(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("endpoint is invalid")
	}
	return parsed, nil
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

func mediaRoute(request *http.Request) string {
	if request.URL.Path == "/v1/media/uploads" {
		return request.URL.Path
	}
	if request.URL.Path == "/v1/media/presentations:resolve" {
		return request.URL.Path
	}
	if strings.HasPrefix(request.URL.Path, "/v1/media/") {
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) == 4 && parts[3] == "complete" {
			return "/v1/media/{asset_id}/complete"
		}
		if len(parts) == 3 {
			return "/v1/media/{asset_id}"
		}
	}
	return "unmatched"
}
