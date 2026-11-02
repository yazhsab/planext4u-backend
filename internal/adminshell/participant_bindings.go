package adminshell

import (
	"github.com/yazhsab/planext4u-backend/internal/organization"
	"net/http"
)

const CapabilityParticipantBind = "admin.participants.bind"

func (handler *Handler) participantBindingActor(w http.ResponseWriter, r *http.Request, write bool) (organization.Actor, bool) {
	session, _, ok := handler.authorize(w, r, CapabilityParticipantBind)
	if !ok {
		return organization.Actor{}, false
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	if !assurance.FreshAuth || !assurance.MFASatisfied || write && !handler.csrfAllowed(r, session.CSRFToken) {
		writeProblem(w, r, 403, "ADMIN_FRESH_AUTH_REQUIRED", "Use a fresh multi-factor sign-in and retry.")
		return organization.Actor{}, false
	}
	a := organizationActor(session)
	a.PlatformAdmin = true
	return a, true
}
func (handler *Handler) participantTerritories(w http.ResponseWriter, r *http.Request) {
	a, ok := handler.participantBindingActor(w, r, false)
	if !ok {
		return
	}
	values, err := handler.organization.ParticipantTerritories(r.Context(), a)
	if err != nil {
		handler.organizationError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": values})
}
func (handler *Handler) participantBinding(w http.ResponseWriter, r *http.Request) {
	a, ok := handler.participantBindingActor(w, r, false)
	if !ok {
		return
	}
	value, err := handler.organization.ParticipantBinding(r.Context(), a, r.PathValue("kind"), r.PathValue("participant_id"))
	if err != nil {
		handler.organizationError(w, r, err)
		return
	}
	writeJSON(w, 200, value)
}
func (handler *Handler) participantBind(w http.ResponseWriter, r *http.Request) {
	a, ok := handler.participantBindingActor(w, r, true)
	if !ok {
		return
	}
	var input struct {
		NodeID   string `json:"node_id"`
		Active   *bool  `json:"active"`
		Revision int64  `json:"revision"`
		Reason   string `json:"reason"`
	}
	if !decodeJSON(r, &input) {
		handler.organizationError(w, r, organization.ErrInvalid)
		return
	}
	if input.Active == nil {
		handler.organizationError(w, r, organization.ErrInvalid)
		return
	}
	value := organization.ParticipantBinding{Kind: r.PathValue("kind"), ParticipantID: r.PathValue("participant_id"), NodeID: input.NodeID, Active: *input.Active, Revision: input.Revision}
	if err := handler.organization.BindParticipant(r.Context(), a, value, input.Reason); err != nil {
		handler.organizationError(w, r, err)
		return
	}
	value.Revision++
	writeJSON(w, 200, value)
}
