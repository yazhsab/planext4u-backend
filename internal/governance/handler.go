package governance

import (
	"encoding/json"
	"errors"
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
	mux.HandleFunc("GET /v1/governance/dashboard", handler.dashboard)
	mux.HandleFunc("GET /v1/governance/reports", handler.reports)
	mux.HandleFunc("GET /v1/governance/maps", handler.maps)
	mux.HandleFunc("GET /v1/governance/leaderboards", handler.leaderboards)
	mux.HandleFunc("GET /v1/governance/intelligence", handler.intelligence)
	mux.HandleFunc("GET /v1/governance/countries", handler.countries)
	return mux, nil
}
func (handler *Handler) dashboard(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.value(writer, request)
	if ok {
		writeGovernance(writer, http.StatusOK, value)
	}
}
func (handler *Handler) reports(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.value(writer, request)
	if ok {
		writeGovernance(writer, http.StatusOK, map[string]any{"generated_at": value.GeneratedAt, "items": value.Reports, "privacy_mode": value.PrivacyMode})
	}
}
func (handler *Handler) maps(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.value(writer, request)
	if ok {
		writeGovernance(writer, http.StatusOK, map[string]any{"generated_at": value.GeneratedAt, "items": value.MapCells, "privacy_mode": value.PrivacyMode})
	}
}
func (handler *Handler) leaderboards(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.value(writer, request)
	if ok {
		writeGovernance(writer, http.StatusOK, map[string]any{"generated_at": value.GeneratedAt, "items": value.Leaderboard, "privacy_mode": value.PrivacyMode})
	}
}
func (handler *Handler) intelligence(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.value(writer, request)
	if ok {
		writeGovernance(writer, http.StatusOK, map[string]any{"generated_at": value.GeneratedAt, "items": value.Insights, "privacy_mode": value.PrivacyMode})
	}
}
func (handler *Handler) countries(writer http.ResponseWriter, request *http.Request) {
	value, ok := handler.value(writer, request)
	if ok {
		writeGovernance(writer, http.StatusOK, map[string]any{"generated_at": value.GeneratedAt, "items": value.Countries})
	}
}
func (handler *Handler) value(writer http.ResponseWriter, request *http.Request) (Dashboard, bool) {
	actor := Actor{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Roles: strings.Split(request.Header.Get("X-Planext4u-Roles"), ","), MFAVerified: strings.EqualFold(request.Header.Get("X-Planext4u-MFA"), "verified")}
	value, err := handler.service.Dashboard(actor)
	if err != nil {
		status, code, message := http.StatusForbidden, "GOVERNANCE_FORBIDDEN", "This governance workspace is not available."
		if errors.Is(err, ErrMFARequired) {
			code, message = "GOVERNANCE_MFA_REQUIRED", "A verified MFA session is required."
		}
		writeGovernance(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": request.Header.Get("X-Correlation-ID"), "retryable": false, "field_errors": []any{}, "details": map[string]any{}}})
		return Dashboard{}, false
	}
	return value, true
}
func writeGovernance(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
