package identity

import (
	"net/http"
	"strconv"
)

func (handler *Handler) handleContactState(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err == nil && (request.URL.RawQuery != "" || request.URL.ForceQuery) {
		err = ErrInvalidInput
	}
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	value, err := handler.service.ContactState(request.Context(), trusted)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) handleContactSync(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err == nil && (request.URL.RawQuery != "" || request.URL.ForceQuery) {
		err = ErrInvalidInput
	}
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	var input ContactSnapshotRequest
	if err = decodeJSON(request, &input); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	value, err := handler.service.SyncContacts(request.Context(), trusted, input)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) handleContactMatches(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	query := request.URL.Query()
	for key, values := range query {
		if len(values) != 1 || (key != "revision" && key != "cursor" && key != "limit") {
			err = ErrInvalidInput
		}
	}
	revision, parseErr := strconv.ParseInt(query.Get("revision"), 10, 64)
	if parseErr != nil {
		err = ErrInvalidInput
	}
	limit := 50
	if value := query.Get("limit"); value != "" {
		limit, parseErr = strconv.Atoi(value)
		if parseErr != nil {
			err = ErrInvalidInput
		}
	}
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	value, err := handler.service.MatchContacts(request.Context(), trusted, revision, query.Get("cursor"), limit)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}
