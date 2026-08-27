package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yazhsab/planext4u-backend/internal/platform/config"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
	"github.com/yazhsab/planext4u-backend/internal/platform/server"
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		// Configuration may originate from secrets, so log only the validation
		// error and never the raw environment.
		slog.Error("invalid service configuration", "error", err)
		return 2
	}

	logger, err := logging.New(os.Stdout, cfg.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	traceRatio := 1.0
	if cfg.Environment == config.EnvironmentProduction {
		traceRatio = 0.10
	}
	observability, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: cfg.ServiceName, ServiceVersion: version, Environment: string(cfg.Environment), TraceRatio: traceRatio})
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

	service := server.New(cfg, logger, version, server.WithTelemetry(observability))
	logger.Info(
		"service starting",
		"service", cfg.ServiceName,
		"environment", cfg.Environment,
		"address", cfg.HTTPAddress,
		"version", version,
		"commit", commit,
	)

	if err := service.Serve(ctx); err != nil {
		logger.Error("service stopped unexpectedly", "error", err)
		return 1
	}

	logger.Info("service stopped", "service", cfg.ServiceName)
	return 0
}
