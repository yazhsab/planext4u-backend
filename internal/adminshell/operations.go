package adminshell

import (
	"errors"
	"net/http"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
)

func (handler *Handler) listOperations(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, CapabilityOperationsRead)
	if !ok {
		return
	}
	changes, err := handler.operations.List(operationPrincipal(session.Principal, capabilities))
	if err != nil {
		handler.writeOperationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"changes": changes})
}

func (handler *Handler) submitOperation(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, CapabilityOperationsRead)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	var input struct {
		Domain   adminops.Domain `json:"domain"`
		Action   string          `json:"action"`
		TargetID string          `json:"target_id"`
		Reason   string          `json:"reason"`
		Payload  map[string]any  `json:"payload"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_OPERATION_INVALID", "Check the operation details and try again.")
		return
	}
	change, err := handler.operations.Submit(operationPrincipal(session.Principal, capabilities), adminops.Command{
		Domain: input.Domain, Action: input.Action, TargetID: input.TargetID, Reason: input.Reason,
		Payload: input.Payload, CorrelationID: request.Header.Get("X-Correlation-ID"),
	})
	if err != nil {
		handler.writeOperationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, change)
}

func (handler *Handler) approveOperation(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, CapabilityOperationsRead)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_OPERATION_INVALID", "Check the approval details and try again.")
		return
	}
	change, err := handler.operations.Approve(operationPrincipal(session.Principal, capabilities), request.PathValue("change_id"), input.ExpectedRevision)
	if err != nil {
		handler.writeOperationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, change)
}

func (handler *Handler) rejectOperation(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, CapabilityOperationsRead)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	var input struct {
		ExpectedRevision int64  `json:"expected_revision"`
		Reason           string `json:"reason"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_OPERATION_INVALID", "Check the rejection details and try again.")
		return
	}
	change, err := handler.operations.Reject(operationPrincipal(session.Principal, capabilities), request.PathValue("change_id"), input.ExpectedRevision, input.Reason)
	if err != nil {
		handler.writeOperationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, change)
}

func (handler *Handler) operationsAudit(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, adminops.CapabilityReporting)
	if !ok {
		return
	}
	events, err := handler.operations.Audit(operationPrincipal(session.Principal, capabilities))
	if err != nil {
		handler.writeOperationError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"events": events})
}

func operationPrincipal(principal Principal, capabilities map[string]bool) adminops.Principal {
	return adminops.Principal{
		TenantID: principal.TenantID, Country: principal.SelectedCountry, SubjectID: principal.SubjectID,
		Capabilities: capabilities, AuthMethods: append([]string(nil), principal.AuthMethods...),
		AuthenticatedAt: principal.AuthenticatedAt,
	}
}

func (handler *Handler) writeOperationError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, adminops.ErrForbidden):
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_OPERATION_FORBIDDEN", "This administrator cannot perform the requested operation.")
	case errors.Is(err, adminops.ErrFreshMFARequired):
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_FRESH_MFA_REQUIRED", "Re-authenticate with multi-factor authentication to continue.")
	case errors.Is(err, adminops.ErrFourEyesRequired):
		writeProblem(writer, request, http.StatusConflict, "ADMIN_FOUR_EYES_REQUIRED", "A different authorized administrator must approve this change.")
	case errors.Is(err, adminops.ErrChangeNotFound):
		writeProblem(writer, request, http.StatusNotFound, "ADMIN_CHANGE_NOT_FOUND", "The requested change is not available in this country context.")
	case errors.Is(err, adminops.ErrRevisionConflict):
		writeProblem(writer, request, http.StatusConflict, "ADMIN_CHANGE_STALE", "The change was updated by another administrator. Refresh and try again.")
	case errors.Is(err, adminops.ErrInvalidState):
		writeProblem(writer, request, http.StatusConflict, "ADMIN_CHANGE_STATE_INVALID", "The change is no longer awaiting approval.")
	default:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_OPERATION_INVALID", "Check the operation details and try again.")
	}
}
