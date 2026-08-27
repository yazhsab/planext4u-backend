package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

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

	application, err := verticalslice.New(verticalslice.Config{SigningKey: signingKey, Logger: logger})
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
