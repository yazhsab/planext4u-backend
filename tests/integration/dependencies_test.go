//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPlatformDependenciesStartInIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	tests := []struct {
		name        string
		image       string
		dockerfile  *testcontainers.FromDockerfile
		ports       []string
		environment map[string]string
		command     []string
		waitingFor  wait.Strategy
	}{
		{
			name:  "postgres",
			image: "postgres:17.10-alpine3.22",
			ports: []string{"5432/tcp"},
			environment: map[string]string{
				"POSTGRES_DB":       "planext4u_test",
				"POSTGRES_USER":     "planext4u_test",
				"POSTGRES_PASSWORD": "synthetic-test-password",
			},
			waitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2 * time.Minute),
		},
		{
			name:       "redis",
			image:      "redis:8.10.0-alpine3.23",
			ports:      []string{"6379/tcp"},
			waitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(2 * time.Minute),
		},
		{
			name:       "nats",
			image:      "nats:2.14.5-alpine3.22",
			ports:      []string{"4222/tcp", "8222/tcp"},
			command:    []string{"--jetstream", "--store_dir", "/data", "--http_port", "8222"},
			waitingFor: wait.ForHTTP("/healthz").WithPort("8222/tcp").WithStartupTimeout(2 * time.Minute),
		},
		{
			name: "object-store",
			dockerfile: &testcontainers.FromDockerfile{
				Context: "../../deploy/local", Dockerfile: "minio.Dockerfile",
				Repo: "planext4u-local-minio", Tag: "9e49d5e7a648", KeepImage: true,
			},
			ports: []string{"9000/tcp"},
			environment: map[string]string{
				"MINIO_ROOT_USER":     "planext4u-test",
				"MINIO_ROOT_PASSWORD": "synthetic-object-password",
			},
			command: []string{"server", "/data"},
			waitingFor: wait.ForHTTP("/minio/health/live").
				WithPort("9000/tcp").
				WithStatusCodeMatcher(func(status int) bool { return status == http.StatusOK }).
				WithStartupTimeout(2 * time.Minute),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			options := []testcontainers.ContainerCustomizer{
				testcontainers.WithExposedPorts(test.ports...),
				testcontainers.WithEnv(test.environment),
				testcontainers.WithCmd(test.command...),
				testcontainers.WithWaitStrategy(test.waitingFor),
			}
			if test.dockerfile != nil {
				options = append(options, testcontainers.WithDockerfile(*test.dockerfile))
			}
			container, err := testcontainers.Run(ctx, test.image, options...)
			if container != nil {
				testcontainers.CleanupContainer(t, container)
			}
			if err != nil {
				t.Fatalf("start %s container: %v", test.name, err)
			}

			for _, port := range test.ports {
				if _, err := container.MappedPort(ctx, port); err != nil {
					t.Errorf("resolve %s mapped port %s: %v", test.name, port, err)
				}
			}
		})
	}
}
