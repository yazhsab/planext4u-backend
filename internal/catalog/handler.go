package catalog

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, errors.New("catalog service is required")
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/home", handler.home)
	mux.HandleFunc("GET /v1/catalog/categories", handler.categories)
	mux.HandleFunc("GET /v1/catalog/items", handler.items)
	mux.HandleFunc("GET /v1/catalog/items/{item_id}", handler.item)
	mux.HandleFunc("GET /v1/catalog/search", handler.search)
	mux.HandleFunc("POST /v1/serviceability/check", handler.serviceability)
	return mux, nil
}

func (handler *Handler) home(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	result, err := handler.service.Home(request.Context(), tenant, country)
	if err != nil {
		writeProblem(writer, request, 503, "CATALOG_UNAVAILABLE", "Catalog is temporarily unavailable.", true)
		return
	}
	writeProjection(writer, result.ProjectionStatus, result)
}

func (handler *Handler) categories(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	result := handler.service.Categories(request.Context(), tenant, country)
	writeProjection(writer, result.ProjectionStatus, result)
}

func (handler *Handler) items(writer http.ResponseWriter, request *http.Request) {
	handler.itemPage(writer, request, request.URL.Query().Get("category_id"), "")
}

func (handler *Handler) search(writer http.ResponseWriter, request *http.Request) {
	handler.itemPage(writer, request, "", request.URL.Query().Get("q"))
}

func (handler *Handler) itemPage(writer http.ResponseWriter, request *http.Request, categoryID, query string) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	limit := 20
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeProblem(writer, request, 422, "CATALOG_REQUEST_INVALID", "The catalog request is invalid.", false)
			return
		}
		limit = parsed
	}
	result, err := handler.service.Items(request.Context(), tenant, country, categoryID, query, request.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeProblem(writer, request, 422, "CATALOG_REQUEST_INVALID", "The catalog request is invalid.", false)
		return
	}
	writeProjection(writer, result.ProjectionStatus, result)
}

func (handler *Handler) item(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	item, status, err := handler.service.Item(request.Context(), tenant, country, request.PathValue("item_id"))
	if errors.Is(err, ErrNotFound) {
		writeProblem(writer, request, 404, "CATALOG_ITEM_NOT_FOUND", "The catalog item was not found.", false)
		return
	}
	if err != nil {
		writeProblem(writer, request, 422, "CATALOG_REQUEST_INVALID", "The catalog request is invalid.", false)
		return
	}
	writeProjection(writer, status, item)
}

func (handler *Handler) serviceability(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var point GeoPoint
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&point) != nil {
		writeProblem(writer, request, 422, "LOCATION_INVALID", "The location is invalid.", false)
		return
	}
	_, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	result, err := handler.service.CheckServiceability(country, point)
	if err != nil {
		writeProblem(writer, request, 422, "LOCATION_INVALID", "The location is invalid.", false)
		return
	}
	writeProjection(writer, ProjectionFresh, result)
}

func requestScope(writer http.ResponseWriter, request *http.Request) (string, string, bool) {
	tenant := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant"))
	country := strings.TrimSpace(request.Header.Get("X-Planext4u-Country"))
	if !safeID(tenant) || len(country) != 2 {
		writeProblem(writer, request, http.StatusUnauthorized, "REQUEST_SCOPE_INVALID", "The authenticated request scope is invalid.", false)
		return "", "", false
	}
	return tenant, country, true
}

func writeProjection(writer http.ResponseWriter, status ProjectionStatus, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, max-age=30, stale-if-error=300")
	writer.Header().Set("X-Projection-Status", string(status))
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string, retryable bool) {
	correlation := request.Header.Get("X-Correlation-ID")
	if correlation == "" {
		correlation = "unavailable"
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlation,
		"retryable": retryable, "field_errors": []any{}, "details": map[string]any{}}})
}
