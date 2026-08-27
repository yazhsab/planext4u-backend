package audit

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, errors.New("audit service is required")
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/audit/events", handler.record)
	mux.HandleFunc("GET /v1/admin/audit/events", handler.search)
	mux.HandleFunc("POST /v1/admin/audit/exports", handler.export)
	return mux, nil
}

func (handler *Handler) record(writer http.ResponseWriter, request *http.Request) {
	principal, ok := parsePrincipal(writer, request)
	if !ok {
		return
	}
	var input RecordRequest
	if !decodeBody(request, &input) {
		problem(writer, request, 422, "AUDIT_REQUEST_INVALID", "The audit request is invalid.")
		return
	}
	entry, err := handler.service.Record(request.Context(), principal, input)
	if err != nil {
		handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, entry)
}

func (handler *Handler) search(writer http.ResponseWriter, request *http.Request) {
	principal, ok := parsePrincipal(writer, request)
	if !ok {
		return
	}
	filter, err := filterFromRequest(request)
	if err != nil {
		handleError(writer, request, err)
		return
	}
	page, err := handler.service.Search(request.Context(), principal, filter)
	if err != nil {
		handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) export(writer http.ResponseWriter, request *http.Request) {
	principal, ok := parsePrincipal(writer, request)
	if !ok {
		return
	}
	var input struct {
		Reason  string `json:"reason"`
		Country string `json:"country,omitempty"`
		Action  string `json:"action,omitempty"`
	}
	if !decodeBody(request, &input) {
		problem(writer, request, 422, "AUDIT_REQUEST_INVALID", "The audit request is invalid.")
		return
	}
	result, err := handler.service.Export(request.Context(), principal, input.Reason, SearchFilter{Country: input.Country, Action: input.Action})
	if err != nil {
		handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func parsePrincipal(writer http.ResponseWriter, request *http.Request) (Principal, bool) {
	principal := Principal{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), SubjectID: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Capabilities: map[string]bool{}}
	for _, capability := range strings.Split(request.Header.Get("X-Planext4u-Capabilities"), ",") {
		if value := strings.TrimSpace(capability); value != "" {
			principal.Capabilities[value] = true
		}
	}
	if raw := request.Header.Get("X-Planext4u-Authenticated-At"); raw != "" {
		principal.AuthenticatedAt, _ = time.Parse(time.RFC3339, raw)
	}
	if !safeID(principal.TenantID) || !safeID(principal.SubjectID) {
		problem(writer, request, 401, "REQUEST_SCOPE_INVALID", "The authenticated request scope is invalid.")
		return Principal{}, false
	}
	return principal, true
}

func filterFromRequest(request *http.Request) (SearchFilter, error) {
	query := request.URL.Query()
	filter := SearchFilter{Country: query.Get("country"), ActorID: query.Get("actor_id"), Action: query.Get("action"), TargetID: query.Get("target_id")}
	var err error
	if filter.After, err = DecodeCursor(query.Get("cursor")); err != nil {
		return SearchFilter{}, err
	}
	if raw := query.Get("limit"); raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil {
			return SearchFilter{}, ErrInvalidRequest
		}
	}
	if raw := query.Get("from"); raw != "" {
		filter.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return SearchFilter{}, ErrInvalidRequest
		}
	}
	if raw := query.Get("to"); raw != "" {
		filter.To, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return SearchFilter{}, ErrInvalidRequest
		}
	}
	return filter, nil
}

func decodeBody(request *http.Request, value any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil
}

func handleError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, ErrForbidden) {
		problem(writer, request, 403, "AUDIT_FORBIDDEN", "The audit operation is not permitted.")
		return
	}
	if errors.Is(err, ErrConflict) {
		problem(writer, request, 409, "AUDIT_APPEND_CONFLICT", "The audit stream changed; retry safely.")
		return
	}
	problem(writer, request, 422, "AUDIT_REQUEST_INVALID", "The audit request is invalid.")
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func problem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlation := request.Header.Get("X-Correlation-ID")
	if correlation == "" {
		correlation = "unavailable"
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlation,
		"retryable": status == 409, "field_errors": []any{}, "details": map[string]any{}}})
}
