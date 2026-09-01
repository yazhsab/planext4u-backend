package commerce

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct{ service CartService }

func NewHandler(service CartService) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/cart", handler.get)
	mux.HandleFunc("PUT /v1/cart/items/{variant_id}", handler.put)
	mux.HandleFunc("DELETE /v1/cart/items/{variant_id}", handler.remove)
	return mux, nil
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Get(scope)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CART_REQUEST_INVALID", "The cart request is invalid.")
		return
	}
	writeCart(writer, http.StatusOK, value, false)
}

func (handler *Handler) put(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var input struct {
		Quantity int `json:"quantity"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CART_REQUEST_INVALID", "The cart request is invalid.")
		return
	}
	handler.change(writer, request, input.Quantity)
}

func (handler *Handler) remove(writer http.ResponseWriter, request *http.Request) {
	handler.change(writer, request, 0)
}

func (handler *Handler) change(writer http.ResponseWriter, request *http.Request, quantity int) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	expectedRevision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil || !safeID(idempotencyKey) || len(idempotencyKey) < 16 {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CART_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return
	}
	value, replay, err := handler.service.Change(request.Context(), scope, idempotencyKey, expectedRevision, request.PathValue("variant_id"), quantity)
	switch {
	case err == nil:
		writeCart(writer, http.StatusOK, value, replay)
	case errors.Is(err, ErrRevisionConflict):
		writeProblem(writer, request, http.StatusConflict, "CART_REVISION_CONFLICT", "The cart changed. Refresh it and try again.")
	case errors.Is(err, ErrIdempotencyConflict):
		writeProblem(writer, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "The idempotency key was already used for a different command.")
	case errors.Is(err, ErrVariantNotFound):
		writeProblem(writer, request, http.StatusNotFound, "VARIANT_NOT_FOUND", "The selected variant was not found.")
	case errors.Is(err, ErrVariantUnavailable), errors.Is(err, ErrQuantityUnavailable):
		writeProblem(writer, request, http.StatusConflict, "VARIANT_UNAVAILABLE", "The selected quantity is no longer available.")
	default:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CART_REQUEST_INVALID", "The cart request is invalid.")
	}
}

func customerScope(writer http.ResponseWriter, request *http.Request) (Scope, bool) {
	scope := Scope{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), CustomerID: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject"))}
	roles := strings.Split(request.Header.Get("X-Planext4u-Roles"), ",")
	customer := false
	for _, role := range roles {
		if strings.TrimSpace(role) == "CUSTOMER" {
			customer = true
		}
	}
	if !validScope(scope) || !customer {
		writeProblem(writer, request, http.StatusForbidden, "CART_ACCESS_DENIED", "The customer cart is not available for this identity.")
		return Scope{}, false
	}
	return scope, true
}

func parseRevision(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, ErrInvalidRequest
	}
	revision, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || revision < 0 {
		return 0, ErrInvalidRequest
	}
	return revision, nil
}

func writeCart(writer http.ResponseWriter, status int, value Cart, replay bool) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(value.Revision, 10)))
	if replay {
		writer.Header().Set("X-Idempotent-Replay", "true")
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlation := request.Header.Get("X-Correlation-ID")
	if correlation == "" {
		correlation = "unavailable"
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlation,
		"retryable": false, "field_errors": []any{}, "details": map[string]any{}}})
}
