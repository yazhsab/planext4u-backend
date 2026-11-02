package adminshell

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/yazhsab/planext4u-backend/internal/configcms"
)

func (handler *Handler) listCMSPages(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityConfigManage)
	if !ok {
		return
	}
	values, err := handler.cms.List(request.Context(), session.Principal.TenantID, session.Principal.SelectedCountry)
	if err != nil {
		writeCMSProblem(writer, request, err)
		return
	}
	revision, err := handler.cmsPublishedRevision(request, session.Principal.TenantID, session.Principal.SelectedCountry)
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "CMS_PUBLICATION_UNAVAILABLE", "Publication details could not be loaded. Refresh and retry.")
		return
	}
	result := map[string]any{"items": values}
	if revision != nil {
		result["published_revision"] = *revision
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) saveCMSPageDraft(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityConfigManage)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	var input struct {
		ExpectedRevision int64          `json:"expected_revision"`
		Page             configcms.Page `json:"page"`
	}
	if !decodeCMSJSON(request, &input) || input.Page.ID != request.PathValue("page_id") {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CMS_DRAFT_INVALID", "Check the page blocks and revision.")
		return
	}
	value, err := handler.cms.Save(request.Context(), session.Principal.TenantID, session.Principal.SelectedCountry, session.Principal.SubjectID, input.Page, input.ExpectedRevision)
	if err != nil {
		writeCMSProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) getCMSWorkspace(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityConfigManage)
	if !ok {
		return
	}
	value, err := handler.cmsWorkspace.Get(request.Context(), session.Principal.TenantID, session.Principal.SelectedCountry)
	if err != nil {
		writeCMSProblem(writer, request, err)
		return
	}
	revision, err := handler.cmsPublishedRevision(request, session.Principal.TenantID, session.Principal.SelectedCountry)
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "CMS_PUBLICATION_UNAVAILABLE", "Publication details could not be loaded. Refresh and retry.")
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		configcms.WorkspaceDraft
		PublishedRevision *int64 `json:"published_revision,omitempty"`
	}{value, revision})
}

func (handler *Handler) cmsPublishedRevision(request *http.Request, tenant, country string) (*int64, error) {
	// Legacy in-process authoring adapters can omit publication metadata. The
	// canonical runtime always wires the owning repository; the UI then freezes
	// both observed revisions in the independently approved publication command.
	if handler.cmsPublished == nil {
		return nil, nil
	}
	snapshot, err := handler.cmsPublished.Get(request.Context(), tenant, country)
	if errors.Is(err, configcms.ErrNotFound) {
		zero := int64(0)
		return &zero, nil
	}
	if err != nil {
		return nil, err
	}
	if snapshot.TenantID != tenant || snapshot.Country != country || snapshot.Revision < 1 {
		return nil, configcms.ErrInvalidRepository
	}
	return &snapshot.Revision, nil
}

func (handler *Handler) saveCMSWorkspaceDraft(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityConfigManage)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	var input struct {
		ExpectedRevision int64               `json:"expected_revision"`
		Workspace        configcms.Workspace `json:"workspace"`
	}
	if !decodeCMSJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CMS_WORKSPACE_DRAFT_INVALID", "Check the global app configuration and revision.")
		return
	}
	value, err := handler.cmsWorkspace.Save(request.Context(), session.Principal.TenantID, session.Principal.SelectedCountry, session.Principal.SubjectID, input.Workspace, input.ExpectedRevision)
	if err != nil {
		writeCMSProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func decodeCMSJSON(request *http.Request, target any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 256*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func writeCMSProblem(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, configcms.ErrRevisionConflict):
		writeProblem(writer, request, http.StatusConflict, "CMS_REVISION_CONFLICT", "The page draft changed. Refresh it and retry.")
	case errors.Is(err, configcms.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "CMS_PAGE_NOT_FOUND", "The requested page draft was not found.")
	default:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CMS_DRAFT_INVALID", "Check the page blocks and revision.")
	}
}
