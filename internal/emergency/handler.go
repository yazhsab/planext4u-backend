package emergency

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Application interface {
	Create(Actor, string, CreateRequest) (Request, bool, error)
	Get(Actor, string) (Request, error)
	List(Actor) ([]Request, error)
	Accept(Actor, string, string) (Request, bool, error)
	UpdateLocation(Actor, string, LocationRequest) (Request, error)
	Transition(Actor, string, string, int64, TransitionRequest) (Request, bool, error)
	Messages(Actor, string) ([]Message, error)
	SendMessage(Actor, string, string, MessageRequest) (Message, bool, error)
	RunEscalations(Actor) (int, error)
	SLA(Actor) (SLAReport, error)
}

type Handler struct{ service Application }

func NewHandler(service Application) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/emergency/requests", handler.requests)
	mux.HandleFunc("POST /v1/emergency/requests", handler.create)
	mux.HandleFunc("GET /v1/emergency/requests/{request_id}", handler.get)
	mux.HandleFunc("POST /v1/emergency/requests/{request_id}/accept", handler.accept)
	mux.HandleFunc("PUT /v1/emergency/requests/{request_id}/location", handler.location)
	mux.HandleFunc("GET /v1/emergency/requests/{request_id}/communications", handler.messages)
	mux.HandleFunc("POST /v1/emergency/requests/{request_id}/communications", handler.sendMessage)
	mux.HandleFunc("POST /v1/emergency/requests/{request_id}/transition", handler.transition)
	mux.HandleFunc("POST /v1/emergency/escalations/run", handler.escalate)
	mux.HandleFunc("GET /v1/emergency/reports/sla", handler.sla)
	return mux, nil
}

func (handler *Handler) requests(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.List(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergency(writer, http.StatusOK, map[string]any{"items": values})
}
func (handler *Handler) create(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	if !customer(actor) {
		handler.problem(writer, request, ErrForbidden)
		return
	}
	var input CreateRequest
	if !decodeEmergency(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Create(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusCreated, value, value.Revision, replay)
}
func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Get(actor, request.PathValue("request_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusOK, value, value.Revision, false)
}
func (handler *Handler) accept(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.Accept(actor, request.Header.Get("Idempotency-Key"), request.PathValue("request_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusOK, value, value.Revision, replay)
}
func (handler *Handler) location(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	var input LocationRequest
	if !decodeEmergency(writer, request, &input) {
		return
	}
	value, err := handler.service.UpdateLocation(actor, request.PathValue("request_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusOK, value, value.Revision, false)
}
func (handler *Handler) transition(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := emergencyMutation(writer, request)
	if !ok {
		return
	}
	var input TransitionRequest
	if !decodeEmergency(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Transition(actor, request.Header.Get("Idempotency-Key"), request.PathValue("request_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusOK, value, value.Revision, replay)
}
func (handler *Handler) messages(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Messages(actor, request.PathValue("request_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergency(writer, http.StatusOK, map[string]any{"items": values})
}
func (handler *Handler) sendMessage(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	var input MessageRequest
	if !decodeEmergency(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SendMessage(actor, request.Header.Get("Idempotency-Key"), request.PathValue("request_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeEmergency(writer, http.StatusCreated, value)
}
func (handler *Handler) escalate(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	count, err := handler.service.RunEscalations(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergency(writer, http.StatusOK, map[string]int{"escalated": count})
}
func (handler *Handler) sla(writer http.ResponseWriter, request *http.Request) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.SLA(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeEmergency(writer, http.StatusOK, value)
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "EMERGENCY_INTERNAL", "The emergency request could not be completed."
	retryable := false
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, message = http.StatusUnprocessableEntity, "EMERGENCY_REQUEST_INVALID", "The emergency request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "EMERGENCY_NOT_FOUND", "The emergency request was not found."
	case errors.Is(err, ErrForbidden):
		status, code, message = http.StatusForbidden, "EMERGENCY_FORBIDDEN", "This emergency action is not permitted."
	case errors.Is(err, ErrMFARequired):
		status, code, message = http.StatusForbidden, "EMERGENCY_MFA_REQUIRED", "A verified MFA session is required."
	case errors.Is(err, ErrRiderOffDuty):
		status, code, message = http.StatusForbidden, "RIDER_EMERGENCY_OFF_DUTY", "Start duty before creating a rider emergency incident."
	case errors.Is(err, ErrDutyUnavailable):
		status, code, message = http.StatusServiceUnavailable, "RIDER_DUTY_UNAVAILABLE", "Rider duty status could not be verified."
		retryable = true
	case errors.Is(err, ErrConflict):
		status, code, message = http.StatusConflict, "EMERGENCY_ASSIGNMENT_CONFLICT", "Another responder already changed this request."
	case errors.Is(err, ErrIdempotencyConflict):
		status, code, message = http.StatusConflict, "EMERGENCY_IDEMPOTENCY_CONFLICT", "The idempotency key was reused for another command."
	}
	writeEmergency(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": request.Header.Get("X-Correlation-ID"), "retryable": retryable, "field_errors": []any{}, "details": map[string]any{}}})
}
func emergencyActor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Roles: strings.Split(request.Header.Get("X-Planext4u-Roles"), ","), MFAVerified: strings.EqualFold(request.Header.Get("X-Planext4u-MFA"), "verified")}
	if !validActor(actor) {
		writeEmergency(writer, http.StatusForbidden, map[string]any{"error": map[string]any{"code": "EMERGENCY_SCOPE_REQUIRED", "message": "The authenticated emergency scope is incomplete."}})
		return Actor{}, false
	}
	return actor, true
}
func emergencyMutation(writer http.ResponseWriter, request *http.Request) (Actor, int64, bool) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return Actor{}, 0, false
	}
	revision, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(request.Header.Get("If-Match")), `"`), 10, 64)
	if err != nil || revision < 1 || !validKey(request.Header.Get("Idempotency-Key")) {
		writeEmergency(writer, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "EMERGENCY_REQUEST_INVALID", "message": "Idempotency-Key and If-Match are required."}})
		return Actor{}, 0, false
	}
	return actor, revision, true
}
func decodeEmergency(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeEmergency(writer, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "EMERGENCY_REQUEST_INVALID", "message": "The request body is invalid."}})
		return false
	}
	return true
}
func writeEmergencyResource(writer http.ResponseWriter, status int, value any, revision int64, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, revision))
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeEmergency(writer, status, value)
}
func writeEmergency(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
