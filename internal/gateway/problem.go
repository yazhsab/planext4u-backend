package gateway

import (
	"encoding/json"
	"net/http"
)

type problemEnvelope struct {
	Error problemBody `json:"error"`
}

type problemBody struct {
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	CorrelationID string         `json:"correlation_id"`
	Retryable     bool           `json:"retryable"`
	FieldErrors   []fieldError   `json:"field_errors"`
	Details       map[string]any `json:"details"`
}

type fieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeProblem(
	writer http.ResponseWriter,
	request *http.Request,
	status int,
	code string,
	message string,
	retryable bool,
) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(problemEnvelope{Error: problemBody{
		Code:          code,
		Message:       message,
		CorrelationID: correlationIDFromContext(request.Context()),
		Retryable:     retryable,
		FieldErrors:   []fieldError{},
		Details:       map[string]any{},
	}})
}
