package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testAsset = "df9e6f1d-94e3-46bc-8c7d-dfa6862a3812"

func testCleanupEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, value string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return map[string]string{
		"MEDIA_CLEANUP_DATABASE_URL_FILE":  write("database", "postgres://planext4u_media_cleanup_login:synthetic@db.example:5432/planext4u?sslmode=verify-full"),
		"MEDIA_CLEANUP_S3_ACCESS_KEY_FILE": write("access", "synthetic-cleanup-access"),
		"MEDIA_CLEANUP_S3_SECRET_KEY_FILE": write("secret", "synthetic-cleanup-secret"),
		"MEDIA_CLEANUP_S3_REGION":          "ap-south-1",
		"MEDIA_CLEANUP_S3_BUCKET":          "private-versioned-media",
	}
}

func TestLoadConfigRequiresExactExplicitScopeAndSeparateCredentials(t *testing.T) {
	env := testCleanupEnv(t)
	lookup := func(name string) string { return env[name] }
	base := []string{"--request-id", "due-request-1", "--asset-id", testAsset, "--execute"}
	if _, err := loadConfig(base, lookup); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		key  string
		val  string
	}{
		{"no execute", base[:4], "", ""},
		{"invalid request", []string{"--request-id", "bad/request", "--asset-id", testAsset, "--execute"}, "", ""},
		{"invalid asset", []string{"--request-id", "due-request-1", "--asset-id", "neighbor", "--execute"}, "", ""},
		{"extra argument", append(append([]string{}, base...), "neighbor"), "", ""},
		{"missing database file", base, "MEDIA_CLEANUP_DATABASE_URL_FILE", ""},
		{"shared login", base, "MEDIA_CLEANUP_DATABASE_URL_FILE", filepath.Join(t.TempDir(), "missing")},
		{"missing access file", base, "MEDIA_CLEANUP_S3_ACCESS_KEY_FILE", ""},
		{"missing secret file", base, "MEDIA_CLEANUP_S3_SECRET_KEY_FILE", ""},
		{"insecure remote S3", base, "MEDIA_CLEANUP_S3_ENDPOINT", "http://s3.example:9000"},
		{"endpoint with credentials", base, "MEDIA_CLEANUP_S3_ENDPOINT", "https://user:pass@s3.example"},
		{"endpoint with path", base, "MEDIA_CLEANUP_S3_ENDPOINT", "https://s3.example/private"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyEnv := make(map[string]string, len(env))
			for key, value := range env {
				copyEnv[key] = value
			}
			if test.key != "" {
				copyEnv[test.key] = test.val
			}
			if _, err := loadConfig(test.args, func(name string) string { return copyEnv[name] }); err == nil {
				t.Fatal("invalid cleanup configuration was accepted")
			}
		})
	}
	for _, endpoint := range []string{"http://localhost:9000/", "http://127.0.0.1:9000", "https://s3.example"} {
		env["MEDIA_CLEANUP_S3_ENDPOINT"] = endpoint
		if _, err := loadConfig(base, lookup); err != nil {
			t.Fatalf("safe endpoint %s was rejected: %v", endpoint, err)
		}
	}
}

func TestLoadConfigRejectsOrdinaryLoginAndUnverifiedTLS(t *testing.T) {
	env := testCleanupEnv(t)
	path := env["MEDIA_CLEANUP_DATABASE_URL_FILE"]
	args := []string{"--request-id", "due-request-1", "--asset-id", testAsset, "--execute"}
	for _, value := range []string{
		"postgres://planext4u_media_runtime:synthetic@db.example:5432/planext4u?sslmode=verify-full",
		"postgres://planext4u_media_cleanup_login:synthetic@db.example:5432/planext4u?sslmode=disable",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(args, func(name string) string { return env[name] }); err == nil {
			t.Fatal("shared login or unverified database TLS was accepted")
		}
	}
}

func TestRunValidationDoesNotEchoSecrets(t *testing.T) {
	env := testCleanupEnv(t)
	var out, errOut bytes.Buffer
	code := run([]string{"--request-id", "due-request-1", "--asset-id", testAsset},
		func(name string) string { return env[name] }, &out, &errOut)
	if code != 2 || out.Len() != 0 || strings.Contains(errOut.String(), "synthetic-cleanup") ||
		strings.Contains(errOut.String(), "db.example") {
		t.Fatalf("validation output leaked a secret or accepted an implicit purge: code=%d output=%q", code, errOut.String())
	}
}
