package fulfillment

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
	RegisterRider(Actor, string, RiderRegistrationRequest) (RiderProfile, bool, error)
	Rider(Actor) (RiderProfile, error)
	ReviewRider(Actor, string, string, int64, bool, string) (RiderProfile, bool, error)
	StartDuty(Actor, string, string) (DutySession, bool, error)
	EndDuty(Actor, string, int64) (DutySession, bool, error)
	Duty(Actor) (DutySession, error)
	SeedTask(Actor, TaskSeed) (DeliveryTask, error)
	OfferTask(Actor, string, string, int64) (DeliveryTask, bool, error)
	Offers(Actor) ([]DeliveryTask, error)
	AcceptOffer(Actor, string, string, int64) (DeliveryTask, bool, error)
	DeclineOffer(Actor, string, string, int64, OfferDeclineRequest) (OfferDecline, bool, error)
	UpdateLocation(Actor, string, LocationUpdate) (RiderLocation, bool, error)
	Location(Actor, string) (RiderLocation, error)
	MarkPickedUp(Actor, string, string, int64) (DeliveryTask, bool, error)
	CompleteDelivery(Actor, string, string, int64, CompletionRequest) (DeliveryTask, bool, error)
	Tasks(Actor) ([]DeliveryTask, error)
	Reassign(Actor, string, string, int64, string) (DeliveryTask, bool, error)
	RecoverOffline(Actor, []OfflineCommand) ([]OfflineResult, error)
	Conversation(Actor, string) (Conversation, error)
	SendMessage(Actor, string, string, string) (ChatMessage, bool, error)
	MessageReceipt(Actor, string, string, string, string) (ChatMessage, bool, error)
	BlockConversation(Actor, string, string, string) (Conversation, bool, error)
	SeedSettlement(Actor, string, string, string, Money) (LedgerEntry, error)
	Ledger(Actor, string) ([]LedgerEntry, error)
	RequestPayout(Actor, string, []string) (Payout, bool, error)
	ApprovePayout(Actor, string, string, int64, string) (Payout, bool, error)
	ExecutePayout(Actor, string, string, int64, string, bool) (Payout, bool, error)
	Payouts(Actor, string) ([]Payout, error)
	Reconcile(Actor, string) (Reconciliation, error)
	Territories(Actor) ([]Territory, error)
	FieldCheckIn(Actor, string, string, Point) (FieldCheckIn, bool, error)
	Attendance(Actor, string) ([]AttendanceEntry, error)
	RegionalDashboard(Actor, string) (RegionalDashboard, error)
	Audits(Actor) ([]AuditEvent, error)
	SweepStaleAssignments(Actor, string, string) (int, error)
}

type Handler struct{ service Application }

func NewHandler(service Application) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/rider/applications", handler.registerRider)
	mux.HandleFunc("GET /v1/rider/profile", handler.rider)
	mux.HandleFunc("POST /v1/rider/applications/{rider_id}/review", handler.reviewRider)
	mux.HandleFunc("POST /v1/rider/duty/start", handler.startDuty)
	mux.HandleFunc("GET /v1/rider/duty", handler.duty)
	mux.HandleFunc("POST /v1/rider/duty/end", handler.endDuty)
	mux.HandleFunc("GET /v1/rider/offers", handler.offers)
	mux.HandleFunc("POST /v1/rider/offers/{offer_id}/decline", handler.decline)
	mux.HandleFunc("GET /v1/rider/tasks", handler.tasks)
	mux.HandleFunc("POST /v1/rider/tasks/{task_id}/accept", handler.accept)
	mux.HandleFunc("POST /v1/rider/tasks/{task_id}/pickup", handler.pickup)
	mux.HandleFunc("POST /v1/rider/tasks/{task_id}/completion", handler.complete)
	mux.HandleFunc("POST /v1/rider/location", handler.location)
	mux.HandleFunc("GET /v1/rider/locations/{rider_id}", handler.getLocation)
	mux.HandleFunc("POST /v1/rider/offline-recovery", handler.offlineRecovery)
	mux.HandleFunc("POST /v1/dispatch/tasks", handler.seedTask)
	mux.HandleFunc("POST /v1/dispatch/tasks/{task_id}/offer", handler.offerTask)
	mux.HandleFunc("POST /v1/dispatch/tasks/{task_id}/reassign", handler.reassign)
	mux.HandleFunc("POST /v1/dispatch/stale-assignment-sweep", handler.sweep)
	mux.HandleFunc("GET /v1/order-chats/{order_id}", handler.conversation)
	mux.HandleFunc("POST /v1/order-chats/{conversation_id}/messages", handler.sendMessage)
	mux.HandleFunc("POST /v1/order-chats/{conversation_id}/messages/{message_id}/receipts", handler.receipt)
	mux.HandleFunc("POST /v1/order-chats/{conversation_id}/block", handler.blockConversation)
	mux.HandleFunc("GET /v1/settlements/ledger", handler.ledger)
	mux.HandleFunc("POST /v1/settlements/seed", handler.seedSettlement)
	mux.HandleFunc("GET /v1/payouts", handler.payouts)
	mux.HandleFunc("POST /v1/payouts", handler.requestPayout)
	mux.HandleFunc("POST /v1/payouts/{payout_id}/approve", handler.approvePayout)
	mux.HandleFunc("POST /v1/payouts/{payout_id}/execute", handler.executePayout)
	mux.HandleFunc("GET /v1/settlements/reconciliation", handler.reconcile)
	mux.HandleFunc("GET /v1/operations/territories", handler.territories)
	mux.HandleFunc("POST /v1/operations/territories/{territory_id}/field-check-in", handler.fieldCheckIn)
	mux.HandleFunc("GET /v1/operations/attendance", handler.attendance)
	mux.HandleFunc("GET /v1/operations/regions/{region_id}/dashboard", handler.regionalDashboard)
	mux.HandleFunc("GET /v1/operations/audit", handler.audits)
	return mux, nil
}

func (handler *Handler) registerRider(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input RiderRegistrationRequest
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.RegisterRider(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeRider(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) rider(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Rider(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeRider(writer, http.StatusOK, value, false)
}

func (handler *Handler) reviewRider(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ReviewRider(actor, request.Header.Get("Idempotency-Key"), request.PathValue("rider_id"), revision, input.Approved, input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeRider(writer, http.StatusOK, value, replay)
}

func (handler *Handler) startDuty(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		ZoneID string `json:"zone_id"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.StartDuty(actor, request.Header.Get("Idempotency-Key"), input.ZoneID)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeDuty(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) duty(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Duty(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeDuty(writer, http.StatusOK, value, false)
}

func (handler *Handler) endDuty(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.EndDuty(actor, request.Header.Get("Idempotency-Key"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeDuty(writer, http.StatusOK, value, replay)
}

func (handler *Handler) offers(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Offers(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) tasks(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Tasks(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) accept(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.AcceptOffer(actor, request.Header.Get("Idempotency-Key"), request.PathValue("task_id"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeTask(writer, http.StatusOK, value, replay)
}

func (handler *Handler) decline(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	var input OfferDeclineRequest
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.DeclineOffer(actor, request.Header.Get("Idempotency-Key"), request.PathValue("offer_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) pickup(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.MarkPickedUp(actor, request.Header.Get("Idempotency-Key"), request.PathValue("task_id"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeTask(writer, http.StatusOK, value, replay)
}

func (handler *Handler) complete(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	var input CompletionRequest
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CompleteDelivery(actor, request.Header.Get("Idempotency-Key"), request.PathValue("task_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeTask(writer, http.StatusOK, value, replay)
}

func (handler *Handler) location(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input LocationUpdate
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.UpdateLocation(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) getLocation(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Location(actor, request.PathValue("rider_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, value)
}

func (handler *Handler) offlineRecovery(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		Commands []OfflineCommand `json:"commands"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	values, err := handler.service.RecoverOffline(actor, input.Commands)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) seedTask(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input TaskSeed
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, err := handler.service.SeedTask(actor, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeTask(writer, http.StatusCreated, value, false)
}

func (handler *Handler) offerTask(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.OfferTask(actor, request.Header.Get("Idempotency-Key"), request.PathValue("task_id"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeTask(writer, http.StatusOK, value, replay)
}

func (handler *Handler) reassign(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Reassign(actor, request.Header.Get("Idempotency-Key"), request.PathValue("task_id"), revision, input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeTask(writer, http.StatusOK, value, replay)
}

func (handler *Handler) sweep(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		RegionID string `json:"region_id"`
		Reason   string `json:"reason"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	count, err := handler.service.SweepStaleAssignments(actor, input.RegionID, input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"flagged": count})
}

func (handler *Handler) conversation(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Conversation(actor, request.PathValue("order_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, value)
}

func (handler *Handler) sendMessage(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SendMessage(actor, request.Header.Get("Idempotency-Key"), request.PathValue("conversation_id"), input.Body)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) receipt(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		State string `json:"state"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.MessageReceipt(actor, request.Header.Get("Idempotency-Key"), request.PathValue("conversation_id"), request.PathValue("message_id"), input.State)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) blockConversation(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.BlockConversation(actor, request.Header.Get("Idempotency-Key"), request.PathValue("conversation_id"), input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) ledger(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	accountID := request.URL.Query().Get("account_id")
	if accountID == "" {
		accountID = actor.Subject
	}
	values, err := handler.service.Ledger(actor, accountID)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) seedSettlement(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		AccountID   string `json:"account_id"`
		ReferenceID string `json:"reference_id"`
		Kind        string `json:"kind"`
		Gross       Money  `json:"gross"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, err := handler.service.SeedSettlement(actor, input.AccountID, input.ReferenceID, input.Kind, input.Gross)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusCreated, value)
}

func (handler *Handler) payouts(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	accountID := request.URL.Query().Get("account_id")
	if accountID == "" {
		accountID = actor.Subject
	}
	values, err := handler.service.Payouts(actor, accountID)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) requestPayout(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		EntryIDs []string `json:"entry_ids"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.RequestPayout(actor, request.Header.Get("Idempotency-Key"), input.EntryIDs)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writePayout(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) approvePayout(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ApprovePayout(actor, request.Header.Get("Idempotency-Key"), request.PathValue("payout_id"), revision, input.Reason)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writePayout(writer, http.StatusOK, value, replay)
}

func (handler *Handler) executePayout(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := fulfillmentMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		ProviderReference string `json:"provider_reference"`
		Success           bool   `json:"success"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ExecutePayout(actor, request.Header.Get("Idempotency-Key"), request.PathValue("payout_id"), revision, input.ProviderReference, input.Success)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writePayout(writer, http.StatusOK, value, replay)
}

func (handler *Handler) reconcile(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Reconcile(actor, request.URL.Query().Get("account_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, value)
}

func (handler *Handler) territories(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Territories(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) fieldCheckIn(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	var input struct {
		Point Point `json:"point"`
	}
	if !decodeFulfillmentJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.FieldCheckIn(actor, request.Header.Get("Idempotency-Key"), request.PathValue("territory_id"), input.Point)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) attendance(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	subjectID := request.URL.Query().Get("subject_id")
	if subjectID == "" {
		subjectID = actor.Subject
	}
	values, err := handler.service.Attendance(actor, subjectID)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) regionalDashboard(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.RegionalDashboard(actor, request.PathValue("region_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, value)
}

func (handler *Handler) audits(writer http.ResponseWriter, request *http.Request) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Audits(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFulfillmentJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "FULFILLMENT_INTERNAL", "The fulfillment request could not be completed."
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, detail = http.StatusUnprocessableEntity, "FULFILLMENT_REQUEST_INVALID", "The fulfillment request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, detail = http.StatusNotFound, "FULFILLMENT_NOT_FOUND", "The fulfillment resource was not found."
	case errors.Is(err, ErrForbidden):
		status, code, detail = http.StatusForbidden, "FULFILLMENT_FORBIDDEN", "The actor cannot access this fulfillment resource."
	case errors.Is(err, ErrMFARequired):
		status, code, detail = http.StatusForbidden, "FULFILLMENT_MFA_REQUIRED", "A verified MFA session is required."
	case errors.Is(err, ErrConflict):
		status, code, detail = http.StatusConflict, "FULFILLMENT_REVISION_CONFLICT", "The fulfillment resource changed; refresh before retrying."
	case errors.Is(err, ErrInvalidTransition):
		status, code, detail = http.StatusConflict, "FULFILLMENT_TRANSITION_INVALID", "The requested fulfillment lifecycle transition is not allowed."
	case errors.Is(err, ErrIdempotencyConflict):
		status, code, detail = http.StatusConflict, "FULFILLMENT_IDEMPOTENCY_CONFLICT", "The idempotency key was already used for another command."
	case errors.Is(err, ErrOfferExpired):
		status, code, detail = http.StatusGone, "FULFILLMENT_OFFER_EXPIRED", "The rider offer has expired."
	case errors.Is(err, ErrLocationStale):
		status, code, detail = http.StatusGone, "FULFILLMENT_LOCATION_STALE", "The rider location is stale or out of order."
	case errors.Is(err, ErrChatExpired):
		status, code, detail = http.StatusGone, "FULFILLMENT_CHAT_EXPIRED", "The order chat window has expired."
	}
	writeFulfillmentProblem(writer, request, status, code, detail)
}

func fulfillmentMutation(writer http.ResponseWriter, request *http.Request) (Actor, int64, bool) {
	actor, ok := fulfillmentActorFromRequest(writer, request)
	if !ok {
		return Actor{}, 0, false
	}
	revision, err := fulfillmentRevision(request.Header.Get("If-Match"))
	if err != nil || !validKey(request.Header.Get("Idempotency-Key")) {
		writeFulfillmentProblem(writer, request, http.StatusUnprocessableEntity, "FULFILLMENT_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return Actor{}, 0, false
	}
	return actor, revision, true
}

func fulfillmentActorFromRequest(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Roles: strings.Split(request.Header.Get("X-Planext4u-Roles"), ","), MFAVerified: strings.EqualFold(request.Header.Get("X-Planext4u-MFA"), "verified")}
	if !validActor(actor) {
		writeFulfillmentProblem(writer, request, http.StatusForbidden, "FULFILLMENT_SCOPE_REQUIRED", "The authenticated fulfillment scope is incomplete.")
		return Actor{}, false
	}
	return actor, true
}

func fulfillmentRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(strings.TrimSpace(strings.Trim(value, `"`)), 10, 64)
	if err != nil || revision < 1 {
		return 0, ErrInvalidRequest
	}
	return revision, nil
}
func decodeFulfillmentJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 128*1024))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeFulfillmentProblem(writer, request, http.StatusUnprocessableEntity, "FULFILLMENT_REQUEST_INVALID", "The fulfillment request is invalid.")
		return false
	}
	return true
}
func writeRider(writer http.ResponseWriter, status int, value RiderProfile, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeFulfillmentReplay(writer, status, value, replay)
}
func writeDuty(writer http.ResponseWriter, status int, value DutySession, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeFulfillmentReplay(writer, status, value, replay)
}
func writeTask(writer http.ResponseWriter, status int, value DeliveryTask, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeFulfillmentReplay(writer, status, value, replay)
}
func writePayout(writer http.ResponseWriter, status int, value Payout, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeFulfillmentReplay(writer, status, value, replay)
}
func writeFulfillmentReplay(writer http.ResponseWriter, status int, value any, replay bool) {
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeFulfillmentJSON(writer, status, value)
}
func writeFulfillmentJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeFulfillmentProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = "corr-unavailable"
	}
	writeFulfillmentJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlationID, "retryable": status >= 500, "field_errors": []any{}, "details": map[string]any{}}})
}
