package adminshell

import (
	"context"
	"net/http"
	"strconv"

	"github.com/yazhsab/planext4u-backend/internal/organization"
)

type MasterService interface {
	Masters(context.Context, organization.Actor, organization.MasterFilter) (organization.MasterPage, error)
	SaveMaster(context.Context, organization.Actor, organization.MasterChange) (organization.Master, error)
	MasterHistory(context.Context, organization.Actor, string, int64) (organization.MasterHistory, error)
}

func (handler *Handler) masterRequest(writer http.ResponseWriter, request *http.Request, write bool) (organization.Actor, bool) {
	session, _, ok := handler.authorize(writer, request, CapabilityShellRead)
	if !ok {
		return organization.Actor{}, false
	}
	allowed := containsRole(session.Principal.Roles, RoleSuperAdmin) || containsRole(session.Principal.Roles, RoleCountryAdmin) || !write && containsRole(session.Principal.Roles, RoleAuditor)
	if !allowed {
		writeProblem(writer, request, 403, "MASTERS_FORBIDDEN", "Your current role cannot access this master data.")
		return organization.Actor{}, false
	}
	if write && (!handler.csrfAllowed(request, session.CSRFToken) || !assuranceFor(session.Principal, handler.clock()).FreshAuth || !principalHasMFA(session.Principal)) {
		writeProblem(writer, request, 403, "ADMIN_FRESH_AUTH_REQUIRED", "Use a fresh multi-factor sign-in and retry.")
		return organization.Actor{}, false
	}
	return organizationActor(session), true
}
func (handler *Handler) listMasters(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.masterRequest(writer, request, false)
	if !ok {
		return
	}
	query := request.URL.Query()
	for key, values := range query {
		if len(values) != 1 || key != "kind" && key != "search" && key != "status" && key != "parent_id" && key != "after" {
			handler.organizationError(writer, request, organization.ErrInvalid)
			return
		}
	}
	page, err := handler.masters.Masters(request.Context(), actor, organization.MasterFilter{Kind: query.Get("kind"), Search: query.Get("search"), Status: query.Get("status"), ParentID: query.Get("parent_id"), After: query.Get("after")})
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 200, page)
}
func (handler *Handler) saveMaster(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.masterRequest(writer, request, true)
	if !ok {
		return
	}
	var input organization.MasterChange
	if request.URL.RawQuery != "" || !decodeJSON(request, &input) || input.ID != request.PathValue("master_id") {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	value, err := handler.masters.SaveMaster(request.Context(), actor, input)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 200, value)
}
func (handler *Handler) masterHistory(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.masterRequest(writer, request, false)
	if !ok {
		return
	}
	query := request.URL.Query()
	before := int64(0)
	for key, values := range query {
		if key != "before" || len(values) != 1 {
			handler.organizationError(writer, request, organization.ErrInvalid)
			return
		}
	}
	if query.Has("before") {
		var err error
		before, err = strconv.ParseInt(query.Get("before"), 10, 64)
		if err != nil {
			handler.organizationError(writer, request, organization.ErrInvalid)
			return
		}
	}
	page, err := handler.masters.MasterHistory(request.Context(), actor, request.PathValue("master_id"), before)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 200, page)
}
