package adminshell

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yazhsab/planext4u-backend/internal/organization"
)

func organizationActor(session ResolvedSession) organization.Actor {
	admin := false
	for _, role := range session.Principal.Roles {
		if role == RoleSuperAdmin {
			admin = true
		}
	}
	return organization.Actor{TenantID: session.Principal.TenantID, Country: session.Principal.SelectedCountry, Subject: session.Principal.SubjectID, PlatformAdmin: admin, MFA: principalHasMFA(session.Principal), OperatorSessionID: session.Principal.SessionID, OperatorRevision: session.Principal.OperatorRevision, RequireLiveOperator: true}
}

func (handler *Handler) organizationTerritoryBindings(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, false)
	if !ok {
		return
	}
	values, err := handler.organization.TerritoryBindings(request.Context(), actor)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 200, map[string]any{"items": values})
}
func (handler *Handler) organizationBindTerritory(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		NodeID   string `json:"node_id"`
		Revision int64  `json:"revision"`
		Reason   string `json:"reason"`
	}
	if !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	if err := handler.organization.BindTerritory(request.Context(), actor, request.PathValue("territory_id"), input.NodeID, input.Revision, input.Reason); err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) organizationRequest(writer http.ResponseWriter, request *http.Request, mutate bool) (organization.Actor, bool) {
	session, _, ok := handler.authorize(writer, request, CapabilityShellRead)
	if !ok {
		return organization.Actor{}, false
	}
	if mutate && (!handler.csrfAllowed(request, session.CSRFToken) || !assuranceFor(session.Principal, handler.clock()).FreshAuth) {
		writeProblem(writer, request, 403, "ADMIN_FRESH_AUTH_REQUIRED", "Use a fresh sign-in and retry this staff change.")
		return organization.Actor{}, false
	}
	return organizationActor(session), true
}
func (handler *Handler) organizationError(writer http.ResponseWriter, request *http.Request, err error) {
	status, code := 503, "ORGANIZATION_UNAVAILABLE"
	switch {
	case errors.Is(err, organization.ErrForbidden):
		status, code = 403, "ORGANIZATION_FORBIDDEN"
	case errors.Is(err, organization.ErrInvalid):
		status, code = 422, "ORGANIZATION_INVALID"
	case errors.Is(err, organization.ErrConflict):
		status, code = 409, "ORGANIZATION_CONFLICT"
	case errors.Is(err, organization.ErrNotFound):
		status, code = 404, "ORGANIZATION_NOT_FOUND"
	}
	writeProblem(writer, request, status, code, "The organization request could not be completed.")
}
func (handler *Handler) organizationWorkspace(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, false)
	if !ok {
		return
	}
	workspace, err := handler.organization.Workspace(request.Context(), actor, request.URL.Query().Get("search"))
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	if request.URL.Path == "/admin/api/v1/organization/export" {
		// Export uses the same filtered query and requires export authority per node.
		for _, node := range workspace.Nodes {
			if err := handler.organization.Authorize(request.Context(), actor, node.ID, organization.ResourceExport); err != nil {
				handler.organizationError(writer, request, err)
				return
			}
		}
		writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
		writer.Header().Set("Content-Disposition", `attachment; filename="organization.csv"`)
		out := csv.NewWriter(writer)
		_ = out.Write([]string{"ID", "Parent ID", "Kind", "Name", "Active", "Revision"})
		for _, node := range workspace.Nodes {
			name := organizationCSVText(node.Name)
			_ = out.Write([]string{node.ID, node.ParentID, node.Kind, name, strconv.FormatBool(node.Active), strconv.FormatInt(node.Revision, 10)})
		}
		out.Flush()
		return
	}
	writeJSON(writer, 200, workspace)
}

func organizationCSVText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character) || character == '\ufeff'
	})
	if (trimmed != "" && strings.ContainsAny(trimmed[:1], "=+-@")) || strings.ContainsAny(value, "\t\r\n") {
		return "'" + value
	}
	return value
}
func (handler *Handler) organizationCreateNode(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		ParentID string `json:"parent_id"`
		Kind     string `json:"kind"`
		Name     string `json:"name"`
	}
	if !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	value, err := handler.organization.CreateNode(request.Context(), actor, organization.Node{ParentID: input.ParentID, Kind: input.Kind, Name: input.Name})
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 201, value)
}
func (handler *Handler) organizationCreateRole(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		NodeID       string   `json:"node_id"`
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	}
	if !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	value, err := handler.organization.CreateRole(request.Context(), actor, organization.JobRole{Name: input.Name, Capabilities: input.Capabilities}, input.NodeID)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 201, value)
}

func (handler *Handler) organizationNodeStatus(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
		Active   *bool `json:"active"`
	}
	if !decodeJSON(request, &input) || input.Active == nil {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	if err := handler.organization.SetNodeActive(request.Context(), actor, request.PathValue("node_id"), input.Revision, *input.Active); err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
func (handler *Handler) organizationSetStaff(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input organization.Assignment
	if !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	value, err := handler.organization.SetAssignment(request.Context(), actor, input)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 200, value)
}
func (handler *Handler) organizationInvite(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		Subject string `json:"subject_id"`
		Node    string `json:"node_id"`
		Role    string `json:"role_id"`
	}
	if !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	value, err := handler.organization.Invite(request.Context(), actor, input.Subject, input.Node, input.Role, 24*time.Hour)
	if err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writeJSON(writer, 201, value)
}
func (handler *Handler) organizationAccept(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.organizationRequest(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !decodeJSON(request, &input) {
		handler.organizationError(writer, request, organization.ErrInvalid)
		return
	}
	if err := handler.organization.AcceptInvitation(request.Context(), actor, input.Token); err != nil {
		handler.organizationError(writer, request, err)
		return
	}
	writer.WriteHeader(204)
}
