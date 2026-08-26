package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFrom(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.ServiceName != defaultServiceName {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, defaultServiceName)
	}
	if cfg.Environment != EnvironmentDevelopment {
		t.Errorf("Environment = %q, want %q", cfg.Environment, EnvironmentDevelopment)
	}
	if cfg.HTTPAddress != defaultHTTPAddress {
		t.Errorf("HTTPAddress = %q, want %q", cfg.HTTPAddress, defaultHTTPAddress)
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
}

func TestLoadFromOverrides(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"SERVICE_NAME":     "identity-service",
		"APP_ENV":          "staging",
		"HTTP_ADDRESS":     "127.0.0.1:9090",
		"SHUTDOWN_TIMEOUT": "25s",
		"LOG_LEVEL":        "DEBUG",
	}
	cfg, err := LoadFrom(mapLookup(values))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.ServiceName != "identity-service" ||
		cfg.Environment != EnvironmentStaging ||
		cfg.HTTPAddress != "127.0.0.1:9090" ||
		cfg.ShutdownTimeout != 25*time.Second ||
		cfg.LogLevel != "debug" {
		t.Fatalf("LoadFrom() returned unexpected config: %#v", cfg)
	}
}

func TestLoadFromRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{name: "environment", values: map[string]string{"APP_ENV": "qa"}, wantErr: "APP_ENV"},
		{name: "address", values: map[string]string{"HTTP_ADDRESS": "localhost"}, wantErr: "HTTP_ADDRESS"},
		{name: "port", values: map[string]string{"HTTP_ADDRESS": ":0"}, wantErr: "port"},
		{name: "timeout format", values: map[string]string{"SHUTDOWN_TIMEOUT": "soon"}, wantErr: "SHUTDOWN_TIMEOUT"},
		{name: "timeout range", values: map[string]string{"SHUTDOWN_TIMEOUT": "3m"}, wantErr: "SHUTDOWN_TIMEOUT"},
		{name: "log level", values: map[string]string{"LOG_LEVEL": "trace"}, wantErr: "LOG_LEVEL"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadFrom(mapLookup(test.values))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("LoadFrom() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
