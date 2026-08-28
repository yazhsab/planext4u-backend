package booking

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/services", handler.listOfferings)
	mux.HandleFunc("GET /v1/services/{service_id}", handler.getOffering)
	mux.HandleFunc("GET /v1/services/{service_id}/slots", handler.listSlots)
	mux.HandleFunc("POST /v1/service-slot-holds", handler.hold)
	mux.HandleFunc("DELETE /v1/service-slot-holds/{hold_id}", handler.releaseHold)
	mux.HandleFunc("GET /v1/service-bookings", handler.listBookings)
	mux.HandleFunc("POST /v1/service-bookings", handler.createBooking)
	mux.HandleFunc("GET /v1/service-bookings/{booking_id}", handler.getBooking)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/payment-confirmation", handler.confirmPayment)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/reschedule", handler.reschedule)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/cancel", handler.cancel)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/provider-status", handler.providerStatus)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/start", handler.start)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/completion", handler.complete)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/confirm-completion", handler.confirmCompletion)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/no-show", handler.noShow)
	mux.HandleFunc("POST /v1/service-bookings/{booking_id}/disputes", handler.dispute)
	return mux, nil
}

func (handler *Handler) listOfferings(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Offerings(scope, request.URL.Query().Get("postal_code"), request.URL.Query().Get("category_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) getOffering(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Offering(scope, request.PathValue("service_id"), request.URL.Query().Get("postal_code"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) listSlots(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	now := handler.service.clock().UTC()
	from, err := parseInstant(request.URL.Query().Get("from"), now)
	if err != nil {
		handler.problem(writer, request, ErrInvalidRequest)
		return
	}
	to, err := parseInstant(request.URL.Query().Get("to"), from.Add(7*24*time.Hour))
	if err != nil {
		handler.problem(writer, request, ErrInvalidRequest)
		return
	}
	values, err := handler.service.Slots(scope, request.PathValue("service_id"), from, to)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) hold(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		SlotID     string `json:"slot_id"`
		PostalCode string `json:"postal_code"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Hold(scope, request.Header.Get("Idempotency-Key"), input.SlotID, input.PostalCode)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) releaseHold(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	if err := handler.service.ReleaseHold(scope, request.PathValue("hold_id")); err != nil {
		handler.problem(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) listBookings(writer http.ResponseWriter, request *http.Request) {
	actor, ok := requestActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.List(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createBooking(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input CreateBookingRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Create(request.Context(), scope, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeBooking(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) getBooking(writer http.ResponseWriter, request *http.Request) {
	actor, ok := requestActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Get(actor, request.PathValue("booking_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeBooking(writer, http.StatusOK, value, false)
}

func (handler *Handler) confirmPayment(writer http.ResponseWriter, request *http.Request) {
	handler.customerMutation(writer, request, func(scope Scope, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.ConfirmPayment(scope, key, id, revision)
	})
}

func (handler *Handler) reschedule(writer http.ResponseWriter, request *http.Request) {
	var input RescheduleRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.customerMutation(writer, request, func(scope Scope, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.Reschedule(scope, key, id, revision, input)
	})
}

func (handler *Handler) cancel(writer http.ResponseWriter, request *http.Request) {
	var input ReasonRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.customerMutation(writer, request, func(scope Scope, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.Cancel(scope, key, id, revision, input.Reason)
	})
}

func (handler *Handler) confirmCompletion(writer http.ResponseWriter, request *http.Request) {
	handler.customerMutation(writer, request, func(scope Scope, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.ConfirmCompletion(scope, key, id, revision)
	})
}

func (handler *Handler) providerStatus(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Status Status `json:"status"`
		Reason string `json:"reason"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.actorMutation(writer, request, func(actor Actor, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.ProviderTransition(actor, key, id, revision, input.Status, input.Reason)
	})
}

func (handler *Handler) start(writer http.ResponseWriter, request *http.Request) {
	var input StartRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.actorMutation(writer, request, func(actor Actor, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.Start(actor, key, id, revision, input.OTP)
	})
}

func (handler *Handler) complete(writer http.ResponseWriter, request *http.Request) {
	var input CompletionRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.actorMutation(writer, request, func(actor Actor, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.Complete(actor, key, id, revision, input.PhotoAssetID)
	})
}

func (handler *Handler) noShow(writer http.ResponseWriter, request *http.Request) {
	var input ReasonRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.actorMutation(writer, request, func(actor Actor, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.NoShow(actor, key, id, revision, input.Reason)
	})
}

func (handler *Handler) dispute(writer http.ResponseWriter, request *http.Request) {
	var input ReasonRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	handler.actorMutation(writer, request, func(actor Actor, key, id string, revision int64) (Booking, bool, error) {
		return handler.service.Dispute(actor, key, id, revision, input.Reason)
	})
}

type customerMutation func(Scope, string, string, int64) (Booking, bool, error)

func (handler *Handler) customerMutation(writer http.ResponseWriter, request *http.Request, mutate customerMutation) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		handler.problem(writer, request, ErrInvalidRequest)
		return
	}
	value, replay, err := mutate(scope, request.Header.Get("Idempotency-Key"), request.PathValue("booking_id"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeBooking(writer, http.StatusOK, value, replay)
}

type actorMutation func(Actor, string, string, int64) (Booking, bool, error)

func (handler *Handler) actorMutation(writer http.ResponseWriter, request *http.Request, mutate actorMutation) {
	actor, ok := requestActor(writer, request)
	if !ok {
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		handler.problem(writer, request, ErrInvalidRequest)
		return
	}
	value, replay, err := mutate(actor, request.Header.Get("Idempotency-Key"), request.PathValue("booking_id"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeBooking(writer, http.StatusOK, value, replay)
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, message := http.StatusUnprocessableEntity, "BOOKING_REQUEST_INVALID", "The booking request is invalid."
	switch {
	case errors.Is(err, ErrOfferingNotFound), errors.Is(err, ErrSlotNotFound), errors.Is(err, ErrHoldNotFound), errors.Is(err, ErrBookingNotFound):
		status, code, message = http.StatusNotFound, "BOOKING_RESOURCE_NOT_FOUND", "The requested service booking resource was not found."
	case errors.Is(err, ErrForbidden):
		status, code, message = http.StatusForbidden, "BOOKING_ACTION_FORBIDDEN", "This account cannot perform that booking action."
	case errors.Is(err, ErrSlotUnavailable), errors.Is(err, ErrHoldExpired), errors.Is(err, ErrPolicyDenied), errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrRevisionConflict), errors.Is(err, ErrIdempotencyConflict):
		status, code, message = http.StatusConflict, "BOOKING_CONFLICT", "The booking changed or that action is no longer available. Refresh and try again."
	case errors.Is(err, ErrOTPInvalid):
		status, code, message = http.StatusUnprocessableEntity, "BOOKING_START_OTP_INVALID", "The start code is invalid or expired."
	case errors.Is(err, ErrEvidenceRequired):
		status, code, message = http.StatusConflict, "BOOKING_EVIDENCE_REQUIRED", "Completion evidence is required before confirmation."
	}
	writeProblem(writer, request, status, code, message)
}

func customerScope(writer http.ResponseWriter, request *http.Request) (Scope, bool) {
	actor, ok := requestActor(writer, request)
	if !ok {
		return Scope{}, false
	}
	if !hasRole(actor, "CUSTOMER") {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_ROLE_REQUIRED", "A customer account is required.")
		return Scope{}, false
	}
	return Scope{TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject}, true
}

func requestActor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{
		TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")),
		Country:  strings.TrimSpace(request.Header.Get("X-Planext4u-Country")),
		Subject:  strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")),
		Roles:    strings.Split(request.Header.Get("X-Planext4u-Roles"), ","),
	}
	if !validActor(actor) {
		writeProblem(writer, request, http.StatusForbidden, "BOOKING_SCOPE_REQUIRED", "The authenticated booking scope is incomplete.")
		return Actor{}, false
	}
	return actor, true
}

func parseRevision(value string) (int64, error) {
	value = strings.TrimSpace(strings.Trim(value, `"`))
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 {
		return 0, ErrInvalidRequest
	}
	return revision, nil
}

func parseInstant(value string, fallback time.Time) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return fallback.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "BOOKING_REQUEST_INVALID", "The booking request is invalid.")
		return false
	}
	return true
}

func writeBooking(writer http.ResponseWriter, status int, value Booking, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeReplay(writer, status, value, replay)
}

func writeReplay(writer http.ResponseWriter, status int, value any, replay bool) {
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(writer, status, value)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = "corr-unavailable"
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "correlation_id": correlationID,
		"retryable": status >= 500, "field_errors": []any{}, "details": map[string]any{},
	}})
}
