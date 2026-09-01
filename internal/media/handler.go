package media

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
		return nil, errors.New("media service is required")
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/media/uploads", handler.presign)
	mux.HandleFunc("POST /v1/media/{asset_id}/complete", handler.complete)
	mux.HandleFunc("GET /v1/media/{asset_id}", handler.get)
	mux.HandleFunc("DELETE /v1/media/{asset_id}", handler.delete)
	return mux, nil
}

func (handler *Handler) presign(writer http.ResponseWriter, request *http.Request) {
	tenant, country, owner, ok := scope(writer, request)
	if !ok {
		return
	}
	defer request.Body.Close()
	var input PresignRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		writeProblem(writer, request, 422, "MEDIA_REQUEST_INVALID", "The media request is invalid.", false)
		return
	}
	grant, err := handler.service.Presign(request.Context(), tenant, country, owner, input)
	if err != nil {
		handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, grant)
}

func (handler *Handler) complete(writer http.ResponseWriter, request *http.Request) {
	tenant, _, owner, ok := scope(writer, request)
	if !ok {
		return
	}
	asset, err := handler.service.Complete(request.Context(), tenant, owner, request.PathValue("asset_id"))
	if err != nil {
		handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, asset)
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	tenant, _, owner, ok := scope(writer, request)
	if !ok {
		return
	}
	asset, err := handler.service.Get(request.Context(), tenant, owner, request.PathValue("asset_id"))
	if err != nil {
		handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, asset)
}

func (handler *Handler) delete(writer http.ResponseWriter, request *http.Request) {
	tenant, _, owner, ok := scope(writer, request)
	if !ok {
		return
	}
	if err := handler.service.Delete(request.Context(), tenant, owner, request.PathValue("asset_id")); err != nil {
		handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func scope(writer http.ResponseWriter, request *http.Request) (string, string, string, bool) {
	tenant := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant"))
	country := strings.TrimSpace(request.Header.Get("X-Planext4u-Country"))
	owner := strings.TrimSpace(request.Header.Get("X-Planext4u-Subject"))
	if !safeID(tenant) || !validCountry(country) || !safeID(owner) {
		writeProblem(writer, request, 401, "REQUEST_SCOPE_INVALID", "The authenticated request scope is invalid.", false)
		return "", "", "", false
	}
	return tenant, country, owner, true
}

func handleError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeProblem(writer, request, 404, "MEDIA_NOT_FOUND", "The media asset was not found.", false)
	case errors.Is(err, ErrExpired):
		writeProblem(writer, request, 410, "MEDIA_UPLOAD_EXPIRED", "The media upload has expired.", false)
	case errors.Is(err, ErrConflict):
		writeProblem(writer, request, 409, "MEDIA_STATE_CONFLICT", "The media asset state has changed.", true)
	case errors.Is(err, ErrDependency):
		writeProblem(writer, request, 503, "MEDIA_DEPENDENCY_UNAVAILABLE", "Media processing is temporarily unavailable.", true)
	default:
		writeProblem(writer, request, 422, "MEDIA_REQUEST_INVALID", "The media request is invalid.", false)
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string, retryable bool) {
	correlation := request.Header.Get("X-Correlation-ID")
	if correlation == "" {
		correlation = "unavailable"
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlation,
		"retryable": retryable, "field_errors": []any{}, "details": map[string]any{}}})
}
