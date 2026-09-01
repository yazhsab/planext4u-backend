package fakeprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

const maxRequestBytes = 64 * 1024

type identityRequest struct {
	Provider string `json:"provider"`
	Token    string `json:"token"`
}

type notificationRequest struct {
	Channel   string `json:"channel"`
	Recipient string `json:"recipient"`
	Template  string `json:"template"`
}

type malwareScanRequest struct {
	ObjectKey string `json:"object_key"`
}

// NewHandler returns deterministic local-only provider adapters. The handler
// rejects non-synthetic recipients and never makes an outbound provider call.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("POST /v1/identity/verify", handleIdentity)
	mux.HandleFunc("POST /v1/notifications/send", handleNotification)
	mux.HandleFunc("POST /v1/media/scan", handleMalwareScan)
	mux.HandleFunc("GET /v1/maps/geocode", handleGeocode)
	return limitBody(mux)
}

func handleMalwareScan(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer local-media-scanner-token" {
		writeProblem(writer, http.StatusUnauthorized, "SCANNER_CREDENTIAL_INVALID")
		return
	}
	var input malwareScanRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !strings.HasPrefix(input.ObjectKey, "tenants/") ||
		len(input.ObjectKey) > 1024 || strings.Contains(input.ObjectKey, "..") {
		writeProblem(writer, http.StatusBadRequest, "OBJECT_KEY_INVALID")
		return
	}
	if strings.Contains(strings.ToLower(input.ObjectKey), "infected") {
		writeJSON(writer, http.StatusOK, map[string]any{"clean": false, "reason_code": "MALWARE_DETECTED"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"clean": true, "reason_code": ""})
}

func handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "synthetic-provider",
	})
}

func handleIdentity(writer http.ResponseWriter, request *http.Request) {
	var input identityRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeProblem(writer, http.StatusBadRequest, "INVALID_JSON")
		return
	}

	identities := map[string]map[string]string{
		"synthetic-customer": {
			"subject": "customer-synthetic-001",
			"role":    "CUSTOMER",
		},
		"synthetic-rider": {
			"subject": "rider-synthetic-001",
			"role":    "RIDER",
		},
		"synthetic-vendor": {
			"subject": "vendor-synthetic-001",
			"role":    "VENDOR",
		},
	}
	identity, exists := identities[input.Token]
	if !exists || (input.Provider != "local" && input.Provider != "oidc") {
		writeProblem(writer, http.StatusUnauthorized, "PROVIDER_TOKEN_INVALID")
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"active":   true,
		"provider": input.Provider,
		"subject":  identity["subject"],
		"roles":    []string{identity["role"]},
	})
}

func handleNotification(writer http.ResponseWriter, request *http.Request) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 16 {
		writeProblem(writer, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED")
		return
	}

	var input notificationRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeProblem(writer, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	if !strings.HasPrefix(input.Recipient, "synthetic-") {
		writeProblem(writer, http.StatusForbidden, "NON_SYNTHETIC_RECIPIENT_DENIED")
		return
	}
	switch input.Channel {
	case "email", "push", "sms":
	default:
		writeProblem(writer, http.StatusUnprocessableEntity, "CHANNEL_UNSUPPORTED")
		return
	}
	if strings.TrimSpace(input.Template) == "" {
		writeProblem(writer, http.StatusUnprocessableEntity, "TEMPLATE_REQUIRED")
		return
	}

	digest := sha256.Sum256([]byte(idempotencyKey + "\x00" + input.Channel + "\x00" + input.Recipient + "\x00" + input.Template))
	writeJSON(writer, http.StatusAccepted, map[string]string{
		"receipt_id": "receipt-" + hex.EncodeToString(digest[:8]),
		"status":     "accepted",
	})
}

func handleGeocode(writer http.ResponseWriter, request *http.Request) {
	query := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("q")))
	locations := map[string]map[string]any{
		"chennai": {
			"label":     "Chennai, Tamil Nadu",
			"latitude":  13.0827,
			"longitude": 80.2707,
			"country":   "IN",
		},
		"madurai": {
			"label":     "Madurai, Tamil Nadu",
			"latitude":  9.9252,
			"longitude": 78.1198,
			"country":   "IN",
		},
	}
	location, exists := locations[query]
	if !exists {
		writeJSON(writer, http.StatusOK, map[string]any{"results": []any{}})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"results": []any{location}})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

func writeProblem(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]any{
			"code":         code,
			"message":      "The synthetic provider rejected the request.",
			"retryable":    false,
			"field_errors": []any{},
			"details":      map[string]any{},
		},
	})
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
