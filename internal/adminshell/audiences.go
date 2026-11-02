package adminshell

import (
	"net/http"
	"net/url"

	"github.com/yazhsab/planext4u-backend/internal/organization"
)

func (handler *Handler) organizationAudienceGroups(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, false)
	if !ok {
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) > 1 || (len(query) == 1 && len(query["after"]) != 1) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	groups, err := handler.organization.AudienceGroups(request.Context(), actor, query.Get("after"))
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, groups)
}

func (handler *Handler) organizationSaveAudienceGroup(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		NodeID             string   `json:"node_id"`
		Name               string   `json:"name"`
		RoleIDs            []string `json:"role_ids"`
		IncludeDescendants *bool    `json:"include_descendants"`
		Active             *bool    `json:"active"`
		Revision           *int64   `json:"revision"`
	}
	if request.URL.RawQuery != "" || !decodeJSON(request, &input) || input.IncludeDescendants == nil || input.Active == nil || input.Revision == nil {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	group := organization.AudienceGroup{ID: request.PathValue("audience_id"), NodeID: input.NodeID, Name: input.Name, RoleIDs: input.RoleIDs, IncludeDescendants: *input.IncludeDescendants, Active: *input.Active, Revision: *input.Revision}
	result, err := handler.organization.SaveAudienceGroup(request.Context(), actor, group)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	status := http.StatusOK
	if request.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(writer, status, result)
}

func (handler *Handler) organizationPreviewAudience(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		SubjectID string `json:"subject_id"`
	}
	if request.URL.RawQuery != "" || !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	member, err := handler.organization.PreviewAudienceMember(request.Context(), actor, request.PathValue("audience_id"), input.SubjectID)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"member": member})
}
