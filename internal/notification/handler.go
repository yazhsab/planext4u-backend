package notification

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/notifications/devices/current", handler.register)
	mux.HandleFunc("DELETE /v1/notifications/devices/current", handler.unregister)
	return mux, nil
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
	if err != nil {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "DEVICE_REGISTRATION_INVALID", "Check notification permission and try again.")
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
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeNotificationProblem(writer, http.StatusUnprocessableEntity, "DEVICE_UNREGISTER_INVALID", "The device could not be unregistered.")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func notificationScope(writer http.ResponseWriter, request *http.Request) (string, string, string, string, bool) {
	tenant, country := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), strings.TrimSpace(request.Header.Get("X-Planext4u-Country"))
	subject, device := strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), strings.TrimSpace(request.Header.Get("X-Planext4u-Device"))
	roles := "," + strings.ReplaceAll(request.Header.Get("X-Planext4u-Roles"), " ", "") + ","
	if !uuidPattern.MatchString(tenant) || !strings.Contains(roles, ",CUSTOMER,") || !safeID(subject) || !safeID(device) {
		writeNotificationProblem(writer, http.StatusForbidden, "DEVICE_ACCESS_DENIED", "This device resource is not available.")
		return "", "", "", "", false
	}
	return tenant, country, subject, device, true
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
	writeNotificationJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": "unavailable", "retryable": false, "field_errors": []any{}, "details": map[string]any{}}})
}
