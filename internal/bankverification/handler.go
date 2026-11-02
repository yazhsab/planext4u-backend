package bankverification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

type Application interface {
	Start(context.Context, Actor, string, string) (LinkView, error)
	Current(context.Context, Actor, string) (LinkView, error)
	Resume(context.Context, Actor, string, string) (LinkView, error)
	Cancel(context.Context, Actor, string, string) (LinkView, error)
}

func NewHandler(service Application) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	mux := http.NewServeMux()
	for _, route := range []struct{ path, role string }{{"vendor", "VENDOR"}, {"rider", "RIDER"}} {
		prefix, role := "/v1/"+route.path+"/bank-links", route.role
		bind := func(operation string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				actor := Actor{Scope: Scope{TenantID: r.Header.Get("X-Planext4u-Tenant"), Country: r.Header.Get("X-Planext4u-Country"), OwnerID: r.Header.Get("X-Planext4u-Subject")}, SessionID: r.Header.Get("X-Planext4u-Session")}
				if !validActor(actor, role) {
					writeBankProblem(w, r, ErrForbidden)
					return
				}
				if r.URL.RawQuery != "" || r.URL.ForceQuery {
					writeBankProblem(w, r, ErrInvalidRequest)
					return
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead && !idempotencyPattern.MatchString(r.Header.Get("Idempotency-Key")) {
					writeBankProblem(w, r, ErrInvalidRequest)
					return
				}
				if r.Method == http.MethodPost {
					contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
					if err != nil || contentType != "application/json" {
						writeBankProblem(w, r, ErrInvalidRequest)
						return
					}
					var empty struct{}
					decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
					decoder.DisallowUnknownFields()
					// null is not an explicit empty command.
					var command *struct{}
					if decoder.Decode(&command) != nil || command == nil || decoder.Decode(&empty) != io.EOF {
						writeBankProblem(w, r, ErrInvalidRequest)
						return
					}
				} else if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
					writeBankProblem(w, r, ErrInvalidRequest)
					return
				}
				var value LinkView
				var err error
				switch operation {
				case "start":
					value, err = service.Start(r.Context(), actor, role, r.Header.Get("Idempotency-Key"))
				case "current":
					value, err = service.Current(r.Context(), actor, role)
				case "resume":
					value, err = service.Resume(r.Context(), actor, role, r.PathValue("attempt_id"))
				case "cancel":
					value, err = service.Cancel(r.Context(), actor, role, r.PathValue("attempt_id"))
				}
				if err != nil {
					writeBankProblem(w, r, err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(value)
			}
		}
		mux.HandleFunc("POST "+prefix, bind("start"))
		mux.HandleFunc("GET "+prefix+"/current", bind("current"))
		mux.HandleFunc("POST "+prefix+"/{attempt_id}/resume", bind("resume"))
		mux.HandleFunc("DELETE "+prefix+"/{attempt_id}", bind("cancel"))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	}), nil
}

func writeBankProblem(w http.ResponseWriter, request *http.Request, err error) {
	status, code, title := http.StatusServiceUnavailable, "BANK_PROVIDER_UNAVAILABLE", "Bank verification is temporarily unavailable. Try again."
	switch {
	case errors.Is(err, ErrForbidden):
		status, code, title = http.StatusForbidden, "BANK_LINK_AUTHORIZATION_REQUIRED", "Sign in again before linking a bank account."
	case errors.Is(err, ErrInvalidRequest):
		status, code, title = http.StatusBadRequest, "INVALID_BANK_LINK_REQUEST", "The bank link request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, title = http.StatusNotFound, "BANK_LINK_NOT_FOUND", "The bank link was not found."
	case errors.Is(err, ErrRejected):
		status, code, title = http.StatusConflict, "BANK_PROVIDER_REJECTED", "The bank provider could not verify this link."
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	correlation := request.Header.Get("X-Correlation-ID")
	if correlation == "" {
		correlation = "corr-unavailable"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": title, "correlation_id": correlation,
		"retryable": status == http.StatusServiceUnavailable, "field_errors": []any{}, "details": map[string]any{},
	}})
}
