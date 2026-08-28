package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/notification"
	"github.com/yazhsab/planext4u-backend/internal/platform/config"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
	"github.com/yazhsab/planext4u-backend/internal/platform/server"
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
	"github.com/yazhsab/planext4u-backend/internal/verticalslice"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() { os.Exit(run()) }

func run() int {
	cfg, signingKey, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid staging vertical-slice configuration", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, cfg.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	observability, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: cfg.ServiceName, ServiceVersion: version, Environment: string(cfg.Environment), TraceRatio: 1,
	})
	if err != nil {
		logger.Error("initialize telemetry", "error", err)
		return 1
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := observability.Shutdown(shutdownContext); err != nil {
			logger.Warn("flush telemetry", "error", err)
		}
	}()

	providerFactory, err := notificationProviderFactory(os.LookupEnv)
	if err != nil {
		logger.Error("initialize notification provider", "error", err)
		return 1
	}
	application, err := verticalslice.New(verticalslice.Config{SigningKey: signingKey, Logger: logger, NotificationProviderFactory: providerFactory})
	if err != nil {
		logger.Error("initialize staging vertical slice", "error", err)
		return 1
	}
	service := server.New(cfg, logger, version, server.WithTelemetry(observability), server.WithApplication(application, verticalslice.Route))
	logger.Info("staging vertical slice starting", "service", cfg.ServiceName, "environment", cfg.Environment, "address", cfg.HTTPAddress, "version", version, "commit", commit)
	if err := service.Serve(ctx); err != nil {
		logger.Error("staging vertical slice stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("staging vertical slice stopped", "service", cfg.ServiceName)
	return 0
}

func notificationProviderFactory(lookup func(string) (string, bool)) (func(notification.DeviceResolver) (map[notification.Channel]notification.Provider, error), error) {
	path, configured := lookup("FCM_SERVICE_ACCOUNT_FILE")
	path = strings.TrimSpace(path)
	if !configured || path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("FCM service account file is unavailable")
	}
	defer file.Close()
	credentials, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(credentials) > 64*1024 {
		return nil, errors.New("FCM service account file is invalid")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("provider redirects are disabled")
		},
	}
	tokenSource, err := notification.NewServiceAccountTokenSource(credentials, client, time.Now)
	if err != nil {
		return nil, errors.New("FCM service account file is invalid")
	}
	return func(resolver notification.DeviceResolver) (map[notification.Channel]notification.Provider, error) {
		provider, providerErr := notification.NewFCMProvider(tokenSource.ProjectID(), resolver, tokenSource, client)
		if providerErr != nil {
			return nil, providerErr
		}
		return map[notification.Channel]notification.Provider{notification.ChannelPush: provider}, nil
	}, nil
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (config.Config, []byte, error) {
	cfg, err := config.LoadFrom(lookup)
	if err != nil {
		return config.Config{}, nil, err
	}
	if cfg.Environment != config.EnvironmentStaging {
		return config.Config{}, nil, errors.New("staging vertical slice requires APP_ENV=staging")
	}
	enabled, _ := lookup("SYNTHETIC_SLICE_ENABLED")
	if strings.TrimSpace(enabled) != "true" {
		return config.Config{}, nil, errors.New("staging vertical slice is disabled")
	}
	secret, ok := lookup("SYNTHETIC_SLICE_SIGNING_KEY")
	if !ok || len(secret) < 32 || len(secret) > 1024 || strings.TrimSpace(secret) != secret {
		return config.Config{}, nil, errors.New("SYNTHETIC_SLICE_SIGNING_KEY must contain 32 to 1024 bytes")
	}
	return cfg, []byte(secret), nil
}
