package configcms

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, errors.New("configuration service is required")
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/bootstrap", handler.bootstrap)
	return mux, nil
}

func (handler *Handler) bootstrap(writer http.ResponseWriter, request *http.Request) {
	tenantID := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant"))
	country := strings.TrimSpace(request.Header.Get("X-Planext4u-Country"))
	result, err := handler.service.Bootstrap(
		request.Context(), tenantID, country, Platform(strings.ToUpper(request.URL.Query().Get("platform"))),
		request.URL.Query().Get("app_version"), request.URL.Query().Get("locale"),
	)
	if err != nil {
		status, code, message := http.StatusUnprocessableEntity, "BOOTSTRAP_REQUEST_INVALID", "The bootstrap request is invalid."
		if errors.Is(err, ErrNotFound) {
			status, code, message = http.StatusNotFound, "CONFIGURATION_NOT_FOUND", "Configuration is unavailable for this region."
		}
		writeProblem(writer, request, status, code, message)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, max-age=60, stale-if-error=300")
	writer.Header().Set("ETag", `"config-`+strconv.FormatInt(result.Revision, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = "unavailable"
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "correlation_id": correlationID, "retryable": false,
		"field_errors": []any{}, "details": map[string]any{},
	}})
}
