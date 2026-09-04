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
	mux.HandleFunc("POST /v1/catalog/items/{item_id}/questions", handler.askQuestion)
	mux.HandleFunc("GET /v1/catalog/search", handler.search)
	mux.HandleFunc("GET /v1/catalog/suggestions", handler.suggestions)
	mux.HandleFunc("GET /v1/geocoding/search", handler.geocode)
	mux.HandleFunc("POST /v1/serviceability/check", handler.serviceability)
	return mux, nil
}

func (handler *Handler) askQuestion(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	subject := strings.TrimSpace(request.Header.Get("X-Planext4u-Subject"))
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	roles := "," + strings.ToUpper(strings.TrimSpace(request.Header.Get("X-Planext4u-Roles"))) + ","
	if !safeID(subject) || !strings.Contains(roles, ",CUSTOMER,") {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_REQUIRED", "A customer session is required.", false)
		return
	}
	defer request.Body.Close()
	var input AskQuestionInput
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "QUESTION_INVALID", "Enter a question between 5 and 500 characters.", false)
		return
	}
	value, err := handler.service.AskQuestion(request.Context(), tenant, country, subject, request.PathValue("item_id"), idempotencyKey, input.Question)
	switch {
	case errors.Is(err, ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "CATALOG_ITEM_NOT_FOUND", "The catalog item was not found.", false)
	case errors.Is(err, ErrQuestionLimit):
		writeProblem(writer, request, http.StatusTooManyRequests, "QUESTION_LIMIT_REACHED", "Wait for the seller to answer your existing questions.", true)
	case errors.Is(err, ErrIdempotencyConflict):
		writeProblem(writer, request, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "This request key was already used for a different question.", false)
	case err != nil:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "QUESTION_INVALID", "Enter a question between 5 and 500 characters.", false)
	default:
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(value)
	}
}

func (handler *Handler) suggestions(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	values, status, err := handler.service.Suggestions(request.Context(), tenant, country, request.URL.Query().Get("q"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CATALOG_REQUEST_INVALID", "The suggestion request is invalid.", false)
		return
	}
	writeProjection(writer, status, map[string]any{"items": values})
}

func (handler *Handler) geocode(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Geocode(tenant, country, request.URL.Query().Get("q"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "GEOCODING_REQUEST_INVALID", "Enter at least two locality or pincode characters.", false)
		return
	}
	writeProjection(writer, ProjectionFresh, map[string]any{"items": values})
}

func (handler *Handler) home(writer http.ResponseWriter, request *http.Request) {
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	result, err := handler.service.Home(request.Context(), tenant, country, request.URL.Query().Get("postal_code"))
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			writeProblem(writer, request, 422, "CATALOG_REQUEST_INVALID", "The service location is invalid.", false)
			return
		}
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
	handler.itemPage(writer, request, request.URL.Query().Get("category_id"), request.URL.Query().Get("q"))
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
	tenant, country, ok := requestScope(writer, request)
	if !ok {
		return
	}
	result, err := handler.service.CheckServiceability(tenant, country, point)
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
