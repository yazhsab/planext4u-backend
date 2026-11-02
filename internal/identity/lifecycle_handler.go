package identity

import "net/http"

func (handler *Handler) handleLifecycle(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	value, err := handler.service.Lifecycle(request.Context(), trusted)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, 200, value)
}
func (handler *Handler) handleDeactivate(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if decodeJSON(request, &input) != nil || input.Confirmation != "DEACTIVATE MY ACCOUNT" {
		handler.writeFailure(writer, request, ErrInvalidInput)
		return
	}
	if err = handler.service.Deactivate(request.Context(), trusted); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
func (handler *Handler) handleCancelDeletion(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if decodeJSON(request, &input) != nil || input.Confirmation != "CANCEL DELETION" {
		handler.writeFailure(writer, request, ErrInvalidInput)
		return
	}
	if err = handler.service.CancelDeletion(request.Context(), trusted, request.PathValue("request_id")); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
