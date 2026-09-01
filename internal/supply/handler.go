package supply

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type ApplicationService interface {
	Register(Actor, string, RegisterRequest) (Application, bool, error)
	Application(Actor) (Application, error)
	SubmitDocuments(Actor, string, int64, DocumentsRequest) (Application, bool, error)
	TransitionForVendor(Actor, string, string, int64, TransitionRequest) (Application, bool, error)
	ScheduleVisit(Actor, string, int64, VisitRequest) (Application, bool, error)
	FieldCheckIn(Actor, string, string, int64, CheckInRequest) (Application, bool, error)
	SetZones(Actor, string, int64, []ServiceZone) (Application, bool, error)
	SubmitBank(Actor, string, int64, BankAccount) (Application, bool, error)
	VerifyBank(Actor, string, string, int64, string) (Application, bool, error)
	Dashboard(Actor) (Dashboard, error)
	Catalog(Actor) ([]CatalogItem, error)
	UpsertCatalog(Actor, string, string, int64, CatalogRequest) (CatalogItem, bool, error)
	SetInventory(Actor, string, string, int64, int) (CatalogItem, bool, error)
	SetSchedule(Actor, string, string, int64, []ScheduleWindow) (CatalogItem, bool, error)
	ApproveCatalog(Actor, string, string, int64, bool, string) (CatalogItem, bool, error)
	Work(Actor) ([]WorkItem, error)
	TransitionWork(Actor, string, string, string) (WorkItem, bool, error)
	UpsertPromotion(Actor, string, Promotion) (Promotion, bool, error)
}

type Handler struct{ service ApplicationService }

func NewHandler(service ApplicationService) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/vendor/applications", handler.register)
	mux.HandleFunc("GET /v1/vendor/application", handler.application)
	mux.HandleFunc("POST /v1/vendor/application/documents", handler.documents)
	mux.HandleFunc("POST /v1/vendor/application/field-visit", handler.scheduleVisit)
	mux.HandleFunc("POST /v1/vendor/application/zones", handler.zones)
	mux.HandleFunc("POST /v1/vendor/application/bank", handler.bank)
	mux.HandleFunc("POST /v1/vendor/applications/{vendor_id}/transitions", handler.transition)
	mux.HandleFunc("POST /v1/vendor/applications/{vendor_id}/field-check-in", handler.fieldCheckIn)
	mux.HandleFunc("POST /v1/vendor/applications/{vendor_id}/bank-verification", handler.bankVerification)
	mux.HandleFunc("GET /v1/vendor/dashboard", handler.dashboard)
	mux.HandleFunc("GET /v1/vendor/catalog", handler.catalog)
	mux.HandleFunc("POST /v1/vendor/catalog", handler.createCatalog)
	mux.HandleFunc("PUT /v1/vendor/catalog/{item_id}", handler.updateCatalog)
	mux.HandleFunc("POST /v1/vendor/catalog/{item_id}/inventory", handler.inventory)
	mux.HandleFunc("POST /v1/vendor/catalog/{item_id}/schedule", handler.schedule)
	mux.HandleFunc("POST /v1/vendor/catalog/{item_id}/approval", handler.catalogApproval)
	mux.HandleFunc("GET /v1/vendor/work", handler.work)
	mux.HandleFunc("POST /v1/vendor/work/{work_id}/transition", handler.workTransition)
	mux.HandleFunc("POST /v1/vendor/promotions", handler.promotion)
	return mux, nil
}

func (handler *Handler) register(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	var input RegisterRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Register(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) application(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Application(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, false)
}

func (handler *Handler) documents(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input DocumentsRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SubmitDocuments(actor, request.Header.Get("Idempotency-Key"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) transition(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input TransitionRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.TransitionForVendor(actor, request.Header.Get("Idempotency-Key"), request.PathValue("vendor_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) scheduleVisit(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input VisitRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ScheduleVisit(actor, request.Header.Get("Idempotency-Key"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) fieldCheckIn(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input CheckInRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.FieldCheckIn(actor, request.Header.Get("Idempotency-Key"), request.PathValue("vendor_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) zones(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Zones []ServiceZone `json:"zones"`
	}
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetZones(actor, request.Header.Get("Idempotency-Key"), revision, input.Zones)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) bank(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input BankAccount
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SubmitBank(actor, request.Header.Get("Idempotency-Key"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) bankVerification(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.VerifyBank(actor, request.Header.Get("Idempotency-Key"), request.PathValue("vendor_id"), revision, input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeApplication(writer, http.StatusOK, value, replay)
}

func (handler *Handler) dashboard(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Dashboard(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSupplyJSON(writer, http.StatusOK, value)
}

func (handler *Handler) catalog(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Catalog(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSupplyJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createCatalog(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	var input CatalogRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.UpsertCatalog(actor, request.Header.Get("Idempotency-Key"), "", 0, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeCatalog(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) updateCatalog(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input CatalogRequest
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.UpsertCatalog(actor, request.Header.Get("Idempotency-Key"), request.PathValue("item_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeCatalog(writer, http.StatusOK, value, replay)
}

func (handler *Handler) inventory(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Stock int `json:"stock"`
	}
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetInventory(actor, request.Header.Get("Idempotency-Key"), request.PathValue("item_id"), revision, input.Stock)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeCatalog(writer, http.StatusOK, value, replay)
}

func (handler *Handler) schedule(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Windows []ScheduleWindow `json:"windows"`
	}
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetSchedule(actor, request.Header.Get("Idempotency-Key"), request.PathValue("item_id"), revision, input.Windows)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeCatalog(writer, http.StatusOK, value, replay)
}

func (handler *Handler) catalogApproval(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := supplyMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ApproveCatalog(actor, request.Header.Get("Idempotency-Key"), request.PathValue("item_id"), revision, input.Approved, input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeCatalog(writer, http.StatusOK, value, replay)
}

func (handler *Handler) work(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Work(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSupplyJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) workTransition(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.TransitionWork(actor, request.Header.Get("Idempotency-Key"), request.PathValue("work_id"), input.Status)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSupplyReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) promotion(writer http.ResponseWriter, request *http.Request) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return
	}
	var input Promotion
	if !decodeSupplyJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.UpsertPromotion(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSupplyReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "SUPPLY_INTERNAL", "The supply request could not be completed."
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, detail = http.StatusUnprocessableEntity, "SUPPLY_REQUEST_INVALID", "The supply request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, detail = http.StatusNotFound, "SUPPLY_NOT_FOUND", "The supply resource was not found."
	case errors.Is(err, ErrForbidden):
		status, code, detail = http.StatusForbidden, "SUPPLY_FORBIDDEN", "The actor cannot access this supply resource."
	case errors.Is(err, ErrConflict):
		status, code, detail = http.StatusConflict, "SUPPLY_REVISION_CONFLICT", "The supply resource changed; refresh before retrying."
	case errors.Is(err, ErrInvalidTransition):
		status, code, detail = http.StatusConflict, "SUPPLY_TRANSITION_INVALID", "The requested supply lifecycle transition is not allowed."
	case errors.Is(err, ErrIdempotencyConflict):
		status, code, detail = http.StatusConflict, "SUPPLY_IDEMPOTENCY_CONFLICT", "The idempotency key was already used for another command."
	}
	writeSupplyProblem(writer, request, status, code, detail)
}

func supplyMutation(writer http.ResponseWriter, request *http.Request) (Actor, int64, bool) {
	actor, ok := supplyActor(writer, request)
	if !ok {
		return Actor{}, 0, false
	}
	revision, err := supplyRevision(request.Header.Get("If-Match"))
	if err != nil || !validKey(request.Header.Get("Idempotency-Key")) {
		writeSupplyProblem(writer, request, http.StatusUnprocessableEntity, "SUPPLY_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return Actor{}, 0, false
	}
	return actor, revision, true
}

func supplyActor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{
		TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")),
		Country:  strings.TrimSpace(request.Header.Get("X-Planext4u-Country")),
		Subject:  strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")),
		Roles:    strings.Split(request.Header.Get("X-Planext4u-Roles"), ","),
	}
	if !validActor(actor) {
		writeSupplyProblem(writer, request, http.StatusForbidden, "SUPPLY_SCOPE_REQUIRED", "The authenticated supply scope is incomplete.")
		return Actor{}, false
	}
	return actor, true
}

func supplyRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(strings.TrimSpace(strings.Trim(value, `"`)), 10, 64)
	if err != nil || revision < 1 {
		return 0, ErrInvalidRequest
	}
	return revision, nil
}

func decodeSupplyJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeSupplyProblem(writer, request, http.StatusUnprocessableEntity, "SUPPLY_REQUEST_INVALID", "The supply request is invalid.")
		return false
	}
	return true
}

func writeApplication(writer http.ResponseWriter, status int, value Application, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeSupplyReplay(writer, status, value, replay)
}

func writeCatalog(writer http.ResponseWriter, status int, value CatalogItem, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeSupplyReplay(writer, status, value, replay)
}

func writeSupplyReplay(writer http.ResponseWriter, status int, value any, replay bool) {
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeSupplyJSON(writer, status, value)
}

func writeSupplyJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeSupplyProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = "corr-unavailable"
	}
	writeSupplyJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlationID, "retryable": status >= 500, "field_errors": []any{}, "details": map[string]any{}}})
}
