package support

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
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/support/tickets", handler.createTicket)
	mux.HandleFunc("GET /v1/support/tickets", handler.listTickets)
	mux.HandleFunc("GET /v1/support/tickets/{ticket_id}", handler.getTicket)
	mux.HandleFunc("POST /v1/support/tickets/{ticket_id}/messages", handler.addMessage)
	mux.HandleFunc("GET /internal/v1/support/tickets", handler.adminTickets)
	return supportSecurityHeaders(mux), nil
}

func (handler *Handler) createTicket(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supportActor(writer, request)
	if !ok {
		return
	}
	var input CreateTicketRequest
	if !decodeSupportJSON(request, &input) {
		writeSupportProblem(writer, request, http.StatusUnprocessableEntity, "SUPPORT_REQUEST_INVALID", "Check the ticket details and try again.")
		return
	}
	ticket, created, err := handler.service.Create(request.Context(), actor, input, request.Header.Get("Idempotency-Key"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeSupportJSON(writer, status, ticket)
}

func (handler *Handler) listTickets(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supportActor(writer, request)
	if !ok {
		return
	}
	limit, valid := supportLimit(request.URL.Query().Get("limit"), 20, 50)
	if !valid {
		writeSupportProblem(writer, request, http.StatusUnprocessableEntity, "SUPPORT_REQUEST_INVALID", "Check the ticket filters and try again.")
		return
	}
	page, err := handler.service.List(request.Context(), actor, Role(strings.TrimSpace(request.URL.Query().Get("owner_role"))), limit, request.URL.Query().Get("cursor"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeSupportJSON(writer, http.StatusOK, page)
}

func (handler *Handler) getTicket(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supportActor(writer, request)
	if !ok {
		return
	}
	ticket, err := handler.service.Get(request.Context(), actor, request.PathValue("ticket_id"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeSupportJSON(writer, http.StatusOK, ticket)
}

func (handler *Handler) addMessage(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supportActor(writer, request)
	if !ok {
		return
	}
	var input AddMessageRequest
	if !decodeSupportJSON(request, &input) {
		writeSupportProblem(writer, request, http.StatusUnprocessableEntity, "SUPPORT_REQUEST_INVALID", "Check the message and try again.")
		return
	}
	ticket, created, err := handler.service.AddMessage(request.Context(), actor, request.PathValue("ticket_id"), input, request.Header.Get("Idempotency-Key"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeSupportJSON(writer, status, ticket)
}

func (handler *Handler) adminTickets(writer http.ResponseWriter, request *http.Request) {
	principal := AdminPrincipal{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Capabilities: map[string]bool{}}
	for _, capability := range strings.Split(request.Header.Get("X-Planext4u-Capabilities"), ",") {
		principal.Capabilities[strings.TrimSpace(capability)] = true
	}
	limit, valid := supportLimit(request.URL.Query().Get("limit"), 50, 100)
	if !valid {
		writeSupportProblem(writer, request, http.StatusUnprocessableEntity, "SUPPORT_REQUEST_INVALID", "Check the ticket filters and try again.")
		return
	}
	page, err := handler.service.AdminList(request.Context(), principal, AdminListFilter{OwnerRole: Role(request.URL.Query().Get("owner_role")), Status: Status(request.URL.Query().Get("status")), Limit: limit, Cursor: request.URL.Query().Get("cursor")})
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeSupportJSON(writer, http.StatusOK, page)
}

func supportActor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject"))}
	for _, role := range strings.Split(request.Header.Get("X-Planext4u-Roles"), ",") {
		actor.Roles = append(actor.Roles, Role(strings.TrimSpace(role)))
	}
	if !validActor(actor) {
		writeSupportProblem(writer, request, http.StatusForbidden, "SUPPORT_SCOPE_REQUIRED", "The authenticated support scope is incomplete.")
		return Actor{}, false
	}
	return actor, true
}

func supportLimit(raw string, fallback, maximum int) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil && value >= 1 && value <= maximum
}

func decodeSupportJSON(request *http.Request, target any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 32*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func (handler *Handler) writeError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		writeSupportProblem(writer, request, http.StatusForbidden, "SUPPORT_ACCESS_DENIED", "This support resource is not available for the active role.")
	case errors.Is(err, ErrNotFound):
		writeSupportProblem(writer, request, http.StatusNotFound, "SUPPORT_TICKET_NOT_FOUND", "The support ticket was not found.")
	case errors.Is(err, ErrIdempotencyConflict):
		writeSupportProblem(writer, request, http.StatusConflict, "SUPPORT_IDEMPOTENCY_CONFLICT", "The idempotency key was already used for different content.")
	case errors.Is(err, ErrInvalidRequest):
		writeSupportProblem(writer, request, http.StatusUnprocessableEntity, "SUPPORT_REQUEST_INVALID", "Check the support request and try again.")
	default:
		writeSupportProblem(writer, request, http.StatusServiceUnavailable, "SUPPORT_UNAVAILABLE", "Support is temporarily unavailable. Try again.")
	}
}

func supportSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

func writeSupportJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeSupportProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := strings.TrimSpace(request.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = "unavailable"
	}
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlationID, "retryable": status >= 500, "field_errors": []any{}, "details": map[string]any{}}})
}
