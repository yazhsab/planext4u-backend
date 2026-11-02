package adminshell

import (
	"errors"
	"net/http"
	"strconv"
)

func (handler *Handler) operatorRequest(writer http.ResponseWriter, request *http.Request, mutate bool) (Principal, bool) {
	session, _, ok := handler.authorize(writer, request, CapabilityOperatorsManage)
	if !ok {
		return Principal{}, false
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	if !assurance.MFASatisfied {
		writeProblem(writer, request, 403, "ADMIN_MFA_REQUIRED", "Complete multi-factor sign-in.")
		return Principal{}, false
	}
	if mutate && (!handler.csrfAllowed(request, session.CSRFToken) || !assurance.FreshAuth) {
		writeProblem(writer, request, 403, "ADMIN_FRESH_AUTH_REQUIRED", "Use a fresh multi-factor sign-in and retry this access change.")
		return Principal{}, false
	}
	return session.Principal, true
}
func (handler *Handler) operatorError(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, message := 503, "OPERATORS_UNAVAILABLE", "Operator administration is temporarily unavailable."
	switch {
	case errors.Is(err, ErrForbidden):
		status, code, message = 403, "OPERATORS_FORBIDDEN", "You cannot administer these operator accounts."
	case errors.Is(err, ErrInvalidRequest):
		status, code, message = 422, "OPERATORS_INVALID", "Check the account details, country scope and reason."
	case errors.Is(err, ErrOperatorNotFound):
		status, code, message = 404, "OPERATOR_NOT_FOUND", "This operator is outside your directory or no longer exists."
	case errors.Is(err, ErrOperatorConflict):
		status, code, message = 409, "OPERATOR_CONFLICT", "This identity is already provisioned or the account changed. Refresh the directory before retrying."
	}
	writeProblem(writer, request, status, code, message)
}
func (handler *Handler) listOperators(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.operatorRequest(writer, request, false)
	if !ok {
		return
	}
	page, err := handler.operatorAccounts.List(request.Context(), actor, request.URL.Query().Get("search"), request.URL.Query().Get("after"), handler.clock())
	if err != nil {
		handler.operatorError(writer, request, err)
		return
	}
	writeJSON(writer, 200, page)
}
func (handler *Handler) createOperator(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.operatorRequest(writer, request, true)
	if !ok {
		return
	}
	var input OperatorCreate
	if !decodeJSON(request, &input) {
		handler.operatorError(writer, request, ErrInvalidRequest)
		return
	}
	value, err := handler.operatorAccounts.Save(request.Context(), actor, "", input.ProviderSubject, input.OperatorChange, handler.clock())
	if err != nil {
		handler.operatorError(writer, request, err)
		return
	}
	writeJSON(writer, 201, value)
}
func (handler *Handler) updateOperator(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.operatorRequest(writer, request, true)
	if !ok {
		return
	}
	var input OperatorChange
	if !decodeJSON(request, &input) {
		handler.operatorError(writer, request, ErrInvalidRequest)
		return
	}
	value, err := handler.operatorAccounts.Save(request.Context(), actor, request.PathValue("subject_id"), "", input, handler.clock())
	if err != nil {
		handler.operatorError(writer, request, err)
		return
	}
	writeJSON(writer, 200, value)
}
func (handler *Handler) operatorEvents(writer http.ResponseWriter, request *http.Request) {
	actor, ok := handler.operatorRequest(writer, request, false)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(defaultString(request.URL.Query().Get("before"), "0"), 10, 64)
	if err != nil {
		handler.operatorError(writer, request, ErrInvalidRequest)
		return
	}
	page, err := handler.operatorAccounts.Events(request.Context(), actor, request.PathValue("subject_id"), before, handler.clock())
	if err != nil {
		handler.operatorError(writer, request, err)
		return
	}
	writeJSON(writer, 200, page)
}
