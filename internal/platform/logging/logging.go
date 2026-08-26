package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

const redactedValue = "[REDACTED]"

var sensitiveKeyFragments = []string{
	"api_key",
	"apikey",
	"authorization",
	"cookie",
	"email",
	"passcode",
	"password",
	"phone",
	"secret",
	"token",
}

// New creates a structured JSON logger with a central defensive redaction
// policy. Callers should still avoid attaching sensitive values to logs.
func New(writer io.Writer, levelName string) (*slog.Logger, error) {
	level, err := parseLevel(levelName)
	if err != nil {
		return nil, err
	}

	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if isSensitiveKey(attr.Key) {
				return slog.String(attr.Key, redactedValue)
			}
			return attr
		},
	})

	return slog.New(handler), nil
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}

func isSensitiveKey(key string) bool {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(key))
	for _, fragment := range sensitiveKeyFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
