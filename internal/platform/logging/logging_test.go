package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewEmitsJSONAndRedactsSensitiveAttributes(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := New(&output, "info")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	logger.Info(
		"authentication event",
		"user_email", "person@example.com",
		"access-token", "secret-value",
		"device_id", "device-private-value",
		"refresh_digest", "digest-private-value",
		"provider_subject", "provider-private-value",
		"order_id", "order-123",
	)

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; output = %q", err, output.String())
	}

	if event["user_email"] != redactedValue {
		t.Errorf("user_email = %q, want redacted", event["user_email"])
	}
	if event["access-token"] != redactedValue {
		t.Errorf("access-token = %q, want redacted", event["access-token"])
	}
	for _, key := range []string{"device_id", "refresh_digest", "provider_subject"} {
		if event[key] != redactedValue {
			t.Errorf("%s = %q, want redacted", key, event[key])
		}
	}
	if event["order_id"] != "order-123" {
		t.Errorf("order_id = %q, want order-123", event["order_id"])
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	t.Parallel()

	if _, err := New(&bytes.Buffer{}, "trace"); err == nil {
		t.Fatal("New() error = nil, want an unsupported-level error")
	}
}
