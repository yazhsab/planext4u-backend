package emergency

import (
	"net/http"
)

// RiderDutyVerifier is the narrow cross-domain boundary used by the emergency
// API. It deliberately exposes no fulfillment data beyond current duty status.
type RiderDutyVerifier interface {
	OnDuty(Actor) (bool, error)
}

type RiderDutyVerifierFunc func(Actor) (bool, error)

func (verify RiderDutyVerifierFunc) OnDuty(actor Actor) (bool, error) {
	return verify(actor)
}

type RiderHandler struct {
	service Application
	duty    RiderDutyVerifier
}

func NewRiderHandler(service Application, duty RiderDutyVerifier) (http.Handler, error) {
	if service == nil || duty == nil {
		return nil, ErrInvalidRequest
	}
	handler := &RiderHandler{service: service, duty: duty}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/rider/emergency-incidents", handler.create)
	mux.HandleFunc("GET /v1/rider/emergency-incidents/{incident_id}", handler.get)
	mux.HandleFunc("POST /v1/rider/emergency-incidents/{incident_id}/location", handler.location)
	return mux, nil
}

func (handler *RiderHandler) create(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.actor(writer, request)
	if !ok {
		return
	}
	onDuty, err := handler.duty.OnDuty(actor)
	if err != nil {
		(&Handler{}).problem(writer, request, ErrDutyUnavailable)
		return
	}
	if !onDuty {
		(&Handler{}).problem(writer, request, ErrRiderOffDuty)
		return
	}
	var input CreateRequest
	if !decodeEmergency(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Create(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		(&Handler{}).problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusCreated, value, value.Revision, replay)
}

func (handler *RiderHandler) get(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.actor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Get(actor, request.PathValue("incident_id"))
	if err != nil {
		(&Handler{}).problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusOK, value, value.Revision, false)
}

func (handler *RiderHandler) location(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.actor(writer, request)
	if !ok {
		return
	}
	var input LocationRequest
	if !decodeEmergency(writer, request, &input) {
		return
	}
	value, err := handler.service.UpdateLocation(actor, request.PathValue("incident_id"), input)
	if err != nil {
		(&Handler{}).problem(writer, request, err)
		return
	}
	writeEmergencyResource(writer, http.StatusOK, value, value.Revision, false)
}

func (handler *RiderHandler) actor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor, ok := emergencyActor(writer, request)
	if !ok {
		return Actor{}, false
	}
	if !hasRole(actor, "RIDER") {
		(&Handler{}).problem(writer, request, ErrForbidden)
		return Actor{}, false
	}
	return actor, true
}
