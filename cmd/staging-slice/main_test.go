package main

import (
	"strings"
	"testing"
)

func TestStagingSliceRequiresExplicitStagingGuardAndSecret(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"APP_ENV": "staging", "SYNTHETIC_SLICE_ENABLED": "true",
		"SYNTHETIC_SLICE_SIGNING_KEY": "synthetic-staging-key-32-bytes-minimum-value",
	}
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	}
	if _, secret, err := loadRuntimeConfig(lookup(valid)); err != nil || len(secret) < 32 {
		t.Fatalf("valid staging configuration: secret length=%d error=%v", len(secret), err)
	}
	for _, mutation := range []map[string]string{
		{"APP_ENV": "production", "SYNTHETIC_SLICE_ENABLED": "true", "SYNTHETIC_SLICE_SIGNING_KEY": valid["SYNTHETIC_SLICE_SIGNING_KEY"]},
		{"APP_ENV": "staging", "SYNTHETIC_SLICE_ENABLED": "false", "SYNTHETIC_SLICE_SIGNING_KEY": valid["SYNTHETIC_SLICE_SIGNING_KEY"]},
		{"APP_ENV": "staging", "SYNTHETIC_SLICE_ENABLED": "true", "SYNTHETIC_SLICE_SIGNING_KEY": "short"},
	} {
		if _, _, err := loadRuntimeConfig(lookup(mutation)); err == nil || strings.Contains(err.Error(), valid["SYNTHETIC_SLICE_SIGNING_KEY"]) {
			t.Fatalf("unsafe configuration error = %v", err)
		}
	}
}
