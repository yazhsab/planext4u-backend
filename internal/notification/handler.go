package notification

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/yazhsab/planext4u-backend/internal/order"
)

type Handler struct {
	service        *Service
	internalSecret []byte
	orderNotifier  *OrderNotifier
}

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	handler.registerRoutes(mux)
	return mux, nil
}

func NewHandlerWithInternalOrderNotifications(service *Service, secret []byte, templateVersion int64) (http.Handler, error) {
	if service == nil || len(secret) < 32 || len(secret) > 4096 {
		return nil, ErrInvalidRequest
	}
	notifier, err := NewOrderNotifier(service, templateVersion)
	if err != nil {
		return nil, err
	}
	handler := &Handler{service: service, internalSecret: append([]byte(nil), secret...), orderNotifier: notifier}
	mux := http.NewServeMux()
	handler.registerRoutes(mux)
	mux.HandleFunc("POST /internal/v1/order-notifications", handler.orderNotification)
	return mux, nil
}

func (handler *Handler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/notification/preferences/{purpose}/{channel}", handler.preference)
	mux.HandleFunc("PUT /v1/notification/preferences/{purpose}/{channel}", handler.updatePreference)
	mux.HandleFunc("PUT /v1/notifications/devices/current", handler.register)
	mux.HandleFunc("DELETE /v1/notifications/devices/current", handler.unregister)
}

func (handler *Handler) preference(writer http.ResponseWriter, request *http.Request) {
	tenant, subject, ok := notificationActorScope(writer, request, "PREFERENCE_ACCESS_DENIED", "These notification preferences are not available.")
	if !ok {
		return
	}
	value, err := handler.service.Preference(request.Context(), tenant, subject, Purpose(request.PathValue("purpose")), Channel(request.PathValue("channel")))
	if errors.Is(err, ErrInvalidRequest) {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "PREFERENCE_INVALID", "The notification preference is invalid.")
		return
	}
	if err != nil {
		writeNotificationProblem(writer, http.StatusServiceUnavailable, "PREFERENCE_UNAVAILABLE", "Notification preferences are temporarily unavailable.")
		return
	}
	writeNotificationJSON(writer, http.StatusOK, value)
}

func (handler *Handler) updatePreference(writer http.ResponseWriter, request *http.Request) {
	tenant, subject, ok := notificationActorScope(writer, request, "PREFERENCE_ACCESS_DENIED", "These notification preferences are not available.")
	if !ok {
		return
	}
	var input struct {
		Enabled         *bool  `json:"enabled"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decodeNotificationJSON(request, &input) || input.Enabled == nil || input.ExpectedVersion == nil {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "PREFERENCE_INVALID", "The notification preference update is invalid.")
		return
	}
	value, err := handler.service.UpdatePreference(request.Context(), tenant, subject, Purpose(request.PathValue("purpose")), Channel(request.PathValue("channel")), *input.Enabled, *input.ExpectedVersion)
	switch {
	case errors.Is(err, ErrConflict):
		writeNotificationProblem(writer, http.StatusConflict, "PREFERENCE_VERSION_CONFLICT", "Refresh notification preferences before trying again.")
	case errors.Is(err, ErrInvalidRequest):
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "PREFERENCE_INVALID", "The notification preference update is invalid.")
	case err != nil:
		writeNotificationProblem(writer, http.StatusServiceUnavailable, "PREFERENCE_UNAVAILABLE", "Notification preferences are temporarily unavailable.")
	default:
		writeNotificationJSON(writer, http.StatusOK, value)
	}
}

func (handler *Handler) orderNotification(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, 32*1024+1))
	if err != nil || len(body) == 0 || len(body) > 32*1024 {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "ORDER_NOTIFICATION_INVALID", "The order notification is invalid.")
		return
	}
	provided, err := hex.DecodeString(strings.TrimSpace(request.Header.Get("X-Planext4u-Internal-Signature")))
	mac := hmac.New(sha256.New, handler.internalSecret)
	_, _ = mac.Write(body)
	if err != nil || !hmac.Equal(provided, mac.Sum(nil)) {
		writeNotificationProblem(writer, http.StatusUnauthorized, "ORDER_NOTIFICATION_UNAUTHORIZED", "The order notification signature is invalid.")
		return
	}
	var value order.Notification
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "ORDER_NOTIFICATION_INVALID", "The order notification is invalid.")
		return
	}
	if err := handler.orderNotifier.Send(request.Context(), value); err != nil {
		writeNotificationProblem(writer, http.StatusServiceUnavailable, "ORDER_NOTIFICATION_PENDING", "The order notification will be retried.")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) register(writer http.ResponseWriter, request *http.Request) {
	tenant, country, subject, device, ok := notificationScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		Platform DevicePlatform `json:"platform"`
		Locale   string         `json:"locale"`
		Token    string         `json:"token"`
	}
	if !decodeNotificationJSON(request, &input) {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "DEVICE_REGISTRATION_INVALID", "Check notification permission and try again.")
		return
	}
	value, err := handler.service.RegisterDevice(request.Context(), tenant, country, subject, device, input.Platform, input.Locale, input.Token)
	if errors.Is(err, ErrInvalidRequest) {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "DEVICE_REGISTRATION_INVALID", "Check notification permission and try again.")
		return
	}
	if err != nil {
		writeNotificationProblem(writer, http.StatusServiceUnavailable, "DEVICE_REGISTRATION_UNAVAILABLE", "Device registration is temporarily unavailable.")
		return
	}
	writeNotificationJSON(writer, http.StatusOK, value)
}

func (handler *Handler) unregister(writer http.ResponseWriter, request *http.Request) {
	tenant, _, subject, device, ok := notificationScope(writer, request)
	if !ok {
		return
	}
	_, err := handler.service.UnregisterDevice(request.Context(), tenant, subject, device)
	if errors.Is(err, ErrInvalidRequest) {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "DEVICE_UNREGISTER_INVALID", "The device could not be unregistered.")
		return
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeNotificationProblem(writer, http.StatusServiceUnavailable, "DEVICE_UNREGISTER_UNAVAILABLE", "Device registration is temporarily unavailable.")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func notificationScope(writer http.ResponseWriter, request *http.Request) (string, string, string, string, bool) {
	tenant, subject, ok := notificationActorScope(writer, request, "DEVICE_ACCESS_DENIED", "This device resource is not available.")
	if !ok {
		return "", "", "", "", false
	}
	country, device := strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), strings.TrimSpace(request.Header.Get("X-Planext4u-Device"))
	if !safeID(device) {
		writeNotificationProblem(writer, http.StatusForbidden, "DEVICE_ACCESS_DENIED", "This device resource is not available.")
		return "", "", "", "", false
	}
	return tenant, country, subject, device, true
}

func notificationActorScope(writer http.ResponseWriter, request *http.Request, code, message string) (string, string, bool) {
	tenant := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant"))
	subject := strings.TrimSpace(request.Header.Get("X-Planext4u-Subject"))
	if !uuidPattern.MatchString(tenant) || !safeID(subject) || !hasNotificationRole(request.Header.Get("X-Planext4u-Roles")) {
		writeNotificationProblem(writer, http.StatusForbidden, code, message)
		return "", "", false
	}
	return tenant, subject, true
}

func hasNotificationRole(value string) bool {
	for _, role := range strings.Split(value, ",") {
		switch strings.TrimSpace(role) {
		case "CUSTOMER", "VENDOR", "RIDER":
			return true
		}
	}
	return false
}

func decodeNotificationJSON(request *http.Request, target any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func writeNotificationJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeNotificationProblem(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": "unavailable", "retryable": status >= http.StatusInternalServerError, "field_errors": []any{}, "details": map[string]any{}}})
}
