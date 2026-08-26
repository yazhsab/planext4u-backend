package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment identifies an isolated deployment tier.
type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentStaging     Environment = "staging"
	EnvironmentProduction  Environment = "production"
)

const (
	defaultServiceName     = "planext4u-platform"
	defaultHTTPAddress     = ":8080"
	defaultShutdownTimeout = 10 * time.Second
	defaultLogLevel        = "info"
)

// Config contains the non-secret process configuration needed by the service
// template. Secrets belong in a dedicated secret provider and must not be
// represented by this type or emitted to logs.
type Config struct {
	ServiceName     string
	Environment     Environment
	HTTPAddress     string
	ShutdownTimeout time.Duration
	LogLevel        string
}

// Load reads and validates process configuration from the environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom allows configuration sources to be tested without mutating the
// process environment.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		ServiceName:     valueOrDefault(lookup, "SERVICE_NAME", defaultServiceName),
		Environment:     Environment(valueOrDefault(lookup, "APP_ENV", string(EnvironmentDevelopment))),
		HTTPAddress:     valueOrDefault(lookup, "HTTP_ADDRESS", defaultHTTPAddress),
		ShutdownTimeout: defaultShutdownTimeout,
		LogLevel:        strings.ToLower(valueOrDefault(lookup, "LOG_LEVEL", defaultLogLevel)),
	}

	if raw, ok := lookup("SHUTDOWN_TIMEOUT"); ok && strings.TrimSpace(raw) != "" {
		timeout, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a duration: %w", err)
		}
		cfg.ShutdownTimeout = timeout
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate rejects ambiguous or unsafe process settings before the service
// begins accepting traffic.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ServiceName) == "" {
		return fmt.Errorf("SERVICE_NAME must not be empty")
	}

	switch c.Environment {
	case EnvironmentDevelopment, EnvironmentStaging, EnvironmentProduction:
	default:
		return fmt.Errorf("APP_ENV must be development, staging, or production")
	}

	_, port, err := net.SplitHostPort(c.HTTPAddress)
	if err != nil {
		return fmt.Errorf("HTTP_ADDRESS must contain a valid host and port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("HTTP_ADDRESS port must be between 1 and 65535")
	}

	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout > 2*time.Minute {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be greater than zero and at most 2m")
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}

	return nil
}

func valueOrDefault(
	lookup func(string) (string, bool),
	key string,
	fallback string,
) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
