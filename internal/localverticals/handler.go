package localverticals

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Application interface {
	SearchHomes(Actor, HomeSearch) ([]HomeListing, error)
	Home(Actor, string) (HomeListing, error)
	CreateHome(Actor, string, HomeListingRequest) (HomeListing, bool, error)
	PublishHome(Actor, string, string, int64) (HomeListing, bool, error)
	EstimateHome(Actor, string) (HomeEstimate, error)
	Inquire(Actor, string, string, string) (Inquiry, bool, error)
	ScheduleVisit(Actor, string, string, time.Time) (Visit, bool, error)
	UpgradeHome(Actor, string, string, string) (HomeListing, bool, error)
	BrowseClassifieds(Actor, string, string, string) ([]ClassifiedListing, error)
	Classified(Actor, string) (ClassifiedListing, error)
	CreateClassified(Actor, string, ClassifiedRequest) (ClassifiedListing, bool, error)
	RevealContact(Actor, string, ContactRequest) (ClassifiedListing, error)
	RepostClassified(Actor, string, string) (ClassifiedListing, bool, error)
	ReportClassified(Actor, string, string, ReportRequest) (ClassifiedListing, bool, error)
	UpgradeClassified(Actor, string, string, string) (ClassifiedListing, bool, error)
	ExpireClassifieds(Actor) (int, error)
}

type Handler struct{ service Application }

func NewHandler(service Application) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/homes/listings", handler.homes)
	mux.HandleFunc("POST /v1/homes/listings", handler.createHome)
	mux.HandleFunc("GET /v1/homes/listings/{listing_id}", handler.home)
	mux.HandleFunc("POST /v1/homes/listings/{listing_id}/publish", handler.publishHome)
	mux.HandleFunc("GET /v1/homes/listings/{listing_id}/estimate", handler.homeEstimate)
	mux.HandleFunc("POST /v1/homes/listings/{listing_id}/inquiries", handler.homeInquiry)
	mux.HandleFunc("POST /v1/homes/listings/{listing_id}/visits", handler.homeVisit)
	mux.HandleFunc("POST /v1/homes/listings/{listing_id}/upgrade", handler.homeUpgrade)
	mux.HandleFunc("GET /v1/classifieds/listings", handler.classifieds)
	mux.HandleFunc("POST /v1/classifieds/listings", handler.createClassified)
	mux.HandleFunc("GET /v1/classifieds/listings/{listing_id}", handler.classified)
	mux.HandleFunc("POST /v1/classifieds/listings/{listing_id}/contact", handler.classifiedContact)
	mux.HandleFunc("POST /v1/classifieds/listings/{listing_id}/repost", handler.classifiedRepost)
	mux.HandleFunc("POST /v1/classifieds/listings/{listing_id}/reports", handler.classifiedReport)
	mux.HandleFunc("POST /v1/classifieds/listings/{listing_id}/upgrade", handler.classifiedUpgrade)
	mux.HandleFunc("POST /v1/classifieds/retention/expire", handler.expireClassifieds)
	return mux, nil
}

func (handler *Handler) homes(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	minPrice, minErr := parseInt(request.URL.Query().Get("min_price"))
	maxPrice, maxErr := parseInt(request.URL.Query().Get("max_price"))
	if minErr != nil || maxErr != nil {
		handler.problem(writer, request, ErrInvalidRequest)
		return
	}
	values, err := handler.service.SearchHomes(actor, HomeSearch{Query: request.URL.Query().Get("q"), Locality: request.URL.Query().Get("locality"), PropertyType: request.URL.Query().Get("property_type"), Purpose: request.URL.Query().Get("purpose"), MinPrice: minPrice, MaxPrice: maxPrice})
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createHome(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input HomeListingRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateHome(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusCreated, value.Revision, value, replay)
}

func (handler *Handler) home(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Home(actor, request.PathValue("listing_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusOK, value.Revision, value, false)
}

func (handler *Handler) publishHome(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := mutation(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.PublishHome(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"), revision)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusOK, value.Revision, value, replay)
}

func (handler *Handler) homeEstimate(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.EstimateHome(actor, request.PathValue("listing_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) homeInquiry(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input struct {
		Message string `json:"message"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Inquire(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"), input.Message)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) homeVisit(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input struct {
		ScheduledAt time.Time `json:"scheduled_at"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ScheduleVisit(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"), input.ScheduledAt)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) homeUpgrade(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input struct {
		Plan string `json:"plan"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.UpgradeHome(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"), input.Plan)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusOK, value.Revision, value, replay)
}

func (handler *Handler) classifieds(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.BrowseClassifieds(actor, request.URL.Query().Get("q"), request.URL.Query().Get("category"), request.URL.Query().Get("locality"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createClassified(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input ClassifiedRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateClassified(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusCreated, value.Revision, value, replay)
}

func (handler *Handler) classified(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Classified(actor, request.PathValue("listing_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusOK, value.Revision, value, false)
}

func (handler *Handler) classifiedContact(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input ContactRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, err := handler.service.RevealContact(actor, request.PathValue("listing_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) classifiedRepost(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.RepostClassified(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusOK, value.Revision, value, replay)
}

func (handler *Handler) classifiedReport(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input ReportRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ReportClassified(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusAccepted, value.Revision, value, replay)
}

func (handler *Handler) classifiedUpgrade(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	var input struct {
		Plan string `json:"plan"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.UpgradeClassified(actor, request.Header.Get("Idempotency-Key"), request.PathValue("listing_id"), input.Plan)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeResource(writer, http.StatusOK, value.Revision, value, replay)
}

func (handler *Handler) expireClassifieds(writer http.ResponseWriter, request *http.Request) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return
	}
	count, err := handler.service.ExpireClassifieds(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]int{"expired": count})
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "LOCAL_VERTICAL_INTERNAL", "The local marketplace request could not be completed."
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, message = http.StatusUnprocessableEntity, "LOCAL_VERTICAL_REQUEST_INVALID", "The local marketplace request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "LOCAL_VERTICAL_NOT_FOUND", "The local marketplace resource was not found."
	case errors.Is(err, ErrForbidden):
		status, code, message = http.StatusForbidden, "LOCAL_VERTICAL_FORBIDDEN", "This action is not permitted."
	case errors.Is(err, ErrMFARequired):
		status, code, message = http.StatusForbidden, "LOCAL_VERTICAL_MFA_REQUIRED", "A verified MFA session is required."
	case errors.Is(err, ErrConflict):
		status, code, message = http.StatusConflict, "LOCAL_VERTICAL_REVISION_CONFLICT", "The resource changed; refresh before retrying."
	case errors.Is(err, ErrIdempotencyConflict):
		status, code, message = http.StatusConflict, "LOCAL_VERTICAL_IDEMPOTENCY_CONFLICT", "The idempotency key was reused for a different request."
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": request.Header.Get("X-Correlation-ID"), "retryable": false, "field_errors": []any{}, "details": map[string]any{}}})
}

func actorFrom(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Roles: strings.Split(request.Header.Get("X-Planext4u-Roles"), ","), MFAVerified: strings.EqualFold(request.Header.Get("X-Planext4u-MFA"), "verified")}
	if !validActor(actor) {
		writeJSON(writer, http.StatusForbidden, map[string]any{"error": map[string]any{"code": "LOCAL_VERTICAL_SCOPE_REQUIRED", "message": "The authenticated scope is incomplete."}})
		return Actor{}, false
	}
	return actor, true
}
func mutation(writer http.ResponseWriter, request *http.Request) (Actor, int64, bool) {
	actor, ok := actorFrom(writer, request)
	if !ok {
		return Actor{}, 0, false
	}
	revision, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(request.Header.Get("If-Match")), `"`), 10, 64)
	if err != nil || revision < 1 || !validKey(request.Header.Get("Idempotency-Key")) {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "LOCAL_VERTICAL_REQUEST_INVALID", "message": "Idempotency-Key and If-Match are required."}})
		return Actor{}, 0, false
	}
	return actor, revision, true
}
func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "LOCAL_VERTICAL_REQUEST_INVALID", "message": "The request body is invalid."}})
		return false
	}
	return true
}
func parseInt(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
func writeResource(writer http.ResponseWriter, status int, revision int64, value any, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, revision))
	writeReplay(writer, status, value, replay)
}
func writeReplay(writer http.ResponseWriter, status int, value any, replay bool) {
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(writer, status, value)
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
