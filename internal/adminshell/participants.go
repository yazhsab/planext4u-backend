package adminshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
)

const CapabilityParticipantReview = "admin.participants.review"
const CapabilityParticipantFieldCheckIn = "admin.participants.field_checkin"
const CapabilityParticipantBankVerify = "admin.participants.bank_verify"

var participantStatuses = map[string][]string{
	"VENDOR": {"REGISTERED", "DOCUMENTS_SUBMITTED", "OCR_REVIEW", "KYC_REVIEW", "FIELD_VISIT_REQUIRED", "FIELD_VISIT_SCHEDULED", "FIELD_VISIT_PASSED", "BANK_REVIEW", "APPROVED", "REJECTED"},
	"RIDER":  {"REGISTERED", "KYC_REVIEW", "APPROVED", "SUSPENDED"},
}

type ParticipantReceipt struct {
	ParticipantID string `json:"participant_id"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Revision      int64  `json:"revision"`
}

// Regional queues omit country-only document and banking metadata even when
// the owning service includes it in its country-review projection.
type regionalParticipantSummary struct {
	ID            string    `json:"id"`
	ParticipantID string    `json:"participant_id"`
	Kind          string    `json:"kind"`
	Revision      int64     `json:"revision"`
	Status        string    `json:"status"`
	DisplayName   string    `json:"display_name"`
	Category      string    `json:"category"`
	UpdatedAt     time.Time `json:"updated_at"`
	Actions       []string  `json:"actions"`
}

type regionalParticipantPage struct {
	Items     []regionalParticipantSummary `json:"items"`
	NextAfter string                       `json:"next_after,omitempty"`
}

func safeRegionalParticipantPage(page participantreview.Page) regionalParticipantPage {
	result := regionalParticipantPage{Items: make([]regionalParticipantSummary, 0, len(page.Items)), NextAfter: page.NextAfter}
	for _, item := range page.Items {
		result.Items = append(result.Items, regionalParticipantSummary{
			ID: item.ID, ParticipantID: item.ParticipantID, Kind: item.Kind,
			Revision: item.Revision, Status: item.Status, DisplayName: item.DisplayName,
			Category: item.Category, UpdatedAt: item.UpdatedAt,
			Actions: append([]string{}, item.Actions...),
		})
	}
	return result
}

type ParticipantReviews interface {
	ParticipantApplications(context.Context, fulfillment.Actor, string, participantreview.Filter) (participantreview.Page, error)
	RejectParticipant(context.Context, fulfillment.Actor, string, string, string, int64, string) (ParticipantReceipt, bool, error)
	FieldCheckInParticipant(context.Context, fulfillment.Actor, string, string, int64, fulfillment.Point) (ParticipantReceipt, bool, error)
}

func (reader *HTTPFulfillmentReader) participantRequest(ctx context.Context, actor fulfillment.Actor, kind, method, path, protocolRole string, query url.Values, body any, key string, revision int64) ([]byte, bool, error) {
	if participantStatuses[kind] == nil || (protocolRole != "OPS_ADMIN" && protocolRole != "REGIONAL_ADMIN" && protocolRole != "FIELD_OFFICER" && protocolRole != "FINANCE" && protocolRole != "OPS_ADMIN,FINANCE") || !participantreview.UUID(actor.OperatorSessionID) || actor.OperatorRevision < 1 || !actor.MFAVerified {
		return nil, false, ErrForbidden
	}
	target := reader.endpoint
	target.Path = path
	target.RawQuery = query.Encode()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, false, err
		}
		input = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), input)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Planext4u-Tenant", actor.TenantID)
	request.Header.Set("X-Planext4u-Country", actor.Country)
	request.Header.Set("X-Planext4u-Subject", actor.Subject)
	request.Header.Set("X-Planext4u-Roles", protocolRole)
	request.Header.Set("X-Planext4u-MFA", "verified")
	request.Header.Set("X-Planext4u-Operator-Session", actor.OperatorSessionID)
	request.Header.Set("X-Planext4u-Operator-Revision", strconv.FormatInt(actor.OperatorRevision, 10))
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("If-Match", strconv.FormatInt(revision, 10))
	}
	response, err := reader.client.Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 403:
		return nil, false, ErrForbidden
	case 404:
		return nil, false, fulfillment.ErrNotFound
	case 409:
		return nil, false, fulfillment.ErrConflict
	case 422:
		return nil, false, ErrInvalidRequest
	default:
		return nil, false, errors.New("participant owner unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 524289))
	if err != nil || len(data) > 524288 {
		return nil, false, errors.New("invalid participant owner response")
	}
	return data, response.Header.Get("Idempotency-Replayed") == "true", nil
}
func (reader *HTTPFulfillmentReader) ParticipantApplications(ctx context.Context, actor fulfillment.Actor, kind string, f participantreview.Filter) (participantreview.Page, error) {
	if participantStatuses[kind] == nil || f.Validate(participantStatuses[kind]) != nil {
		return participantreview.Page{}, ErrInvalidRequest
	}
	role := participantScopeRole(actor)
	if role == "" {
		return participantreview.Page{}, ErrForbidden
	}
	q := url.Values{"status": {f.Status}, "search": {f.Search}, "after": {f.After}}
	data, _, err := reader.participantRequest(ctx, actor, kind, http.MethodGet, "/v1/"+strings.ToLower(kind)+"/applications", role, q, nil, "", 0)
	if err != nil {
		return participantreview.Page{}, err
	}
	var page participantreview.Page
	if json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > 50 {
		return participantreview.Page{}, errors.New("incomplete participant page")
	}
	seen := map[string]bool{}
	previous := f.After
	for _, item := range page.Items {
		// The owning queue is scoped and ordered before it is returned to the
		// browser. Reject a response that crosses the requested status/cursor
		// boundary, including one supplied by a misconfigured owner.
		if !participantreview.UUID(item.ID) || !participantreview.UUID(item.ParticipantID) || item.ID <= previous || seen[item.ID] || item.Kind != kind || f.Status != "" && item.Status != f.Status || item.Revision < 1 || item.DisplayName == "" || item.Category == "" || item.UpdatedAt.IsZero() || item.DocumentCount < 0 || item.Actions == nil || (participantreview.Filter{Status: item.Status}).Validate(participantStatuses[kind]) != nil || item.Status == "" {
			return participantreview.Page{}, errors.New("invalid participant projection")
		}
		for _, action := range item.Actions {
			if (action != "REJECT" && action != "FIELD_CHECK_IN") || action == "FIELD_CHECK_IN" && (kind != "VENDOR" || item.Status != "FIELD_VISIT_SCHEDULED") || item.ParticipantID == actor.Subject {
				return participantreview.Page{}, errors.New("invalid participant action")
			}
		}
		if len(item.Actions) > 1 {
			return participantreview.Page{}, errors.New("invalid participant actions")
		}
		seen[item.ID] = true
		previous = item.ID
	}
	if page.NextAfter != "" && (!participantreview.UUID(page.NextAfter) || len(page.Items) != 50 || page.Items[49].ID != page.NextAfter) {
		return participantreview.Page{}, errors.New("invalid participant cursor")
	}
	return page, nil
}
func (reader *HTTPFulfillmentReader) RejectParticipant(ctx context.Context, actor fulfillment.Actor, kind, id, key string, revision int64, reason string) (ParticipantReceipt, bool, error) {
	if participantStatuses[kind] == nil || !participantreview.UUID(id) || id == actor.Subject || !fieldKeyPattern.MatchString(key) || revision < 1 || utf8.RuneCountInString(strings.TrimSpace(reason)) < 8 || utf8.RuneCountInString(reason) > 512 {
		return ParticipantReceipt{}, false, ErrInvalidRequest
	}
	role := participantScopeRole(actor)
	if role == "" {
		return ParticipantReceipt{}, false, ErrForbidden
	}
	path := "/v1/vendor/applications/" + id + "/transitions"
	body := map[string]any{"status": "REJECTED", "reason": reason}
	expected := "REJECTED"
	ownerID := "vendor_id"
	if kind == "RIDER" {
		path = "/v1/rider/applications/" + id + "/review"
		body = map[string]any{"approved": false, "reason": reason}
		expected = "SUSPENDED"
		ownerID = "rider_id"
	}
	data, replay, err := reader.participantRequest(ctx, actor, kind, http.MethodPost, path, role, nil, body, key, revision)
	if err != nil {
		return ParticipantReceipt{}, false, err
	}
	var raw map[string]json.RawMessage
	var value ParticipantReceipt
	var participant string
	if json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil || json.Unmarshal(raw[ownerID], &participant) != nil || participant != id || value.Status != expected || value.Revision != revision+1 {
		return ParticipantReceipt{}, false, errors.New("participant command response did not match")
	}
	value.Kind = kind
	value.ParticipantID = id
	return value, replay, nil
}

func participantScopeRole(actor fulfillment.Actor) string {
	if countryParticipantActor(actor) {
		return "OPS_ADMIN"
	}
	if len(actor.Roles) == 1 && actor.Roles[0] == "REGIONAL_ADMIN" {
		return "REGIONAL_ADMIN"
	}
	return ""
}

func (reader *HTTPFulfillmentReader) FieldCheckInParticipant(ctx context.Context, actor fulfillment.Actor, id, key string, revision int64, point fulfillment.Point) (ParticipantReceipt, bool, error) {
	if !participantreview.UUID(id) || id == actor.Subject || !fieldKeyPattern.MatchString(key) || revision < 1 || !validFieldPoint(point) {
		return ParticipantReceipt{}, false, ErrInvalidRequest
	}
	data, replay, err := reader.participantRequest(ctx, actor, "VENDOR", http.MethodPost, "/v1/vendor/applications/"+id+"/field-check-in", "FIELD_OFFICER", nil, point, key, revision)
	if err != nil {
		return ParticipantReceipt{}, false, err
	}
	var raw map[string]json.RawMessage
	var value ParticipantReceipt
	var participant string
	if json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil || json.Unmarshal(raw["vendor_id"], &participant) != nil || participant != id || value.Status != "FIELD_VISIT_PASSED" || value.Revision != revision+1 {
		return ParticipantReceipt{}, false, errors.New("participant field command response did not match")
	}
	value.Kind = "VENDOR"
	value.ParticipantID = id
	return value, replay, nil
}

func (handler *Handler) participantSession(w http.ResponseWriter, r *http.Request, write bool) (fulfillment.Actor, bool) {
	return handler.participantSessionFor(w, r, CapabilityParticipantReview, write)
}
func (handler *Handler) participantSessionFor(w http.ResponseWriter, r *http.Request, capability string, write bool) (fulfillment.Actor, bool) {
	session, _, ok := handler.authorize(w, r, capability)
	if !ok {
		return fulfillment.Actor{}, false
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	if !assurance.FreshAuth || !assurance.MFASatisfied || write && !handler.csrfAllowed(r, session.CSRFToken) {
		writeProblem(w, r, 403, "ADMIN_FRESH_AUTH_REQUIRED", "Use a fresh multi-factor sign-in and retry.")
		return fulfillment.Actor{}, false
	}
	actor := fulfillmentOperator(session, handler.clock())
	if containsRole(session.Principal.Roles, RoleFinance) && len(actor.Roles) == 1 && actor.Roles[0] == "OPS_ADMIN" {
		actor.Roles = append(actor.Roles, "FINANCE")
	}
	actor.OperatorSessionID = session.Principal.SessionID
	actor.OperatorRevision = session.Principal.OperatorRevision
	return actor, true
}
func (handler *Handler) participantFieldCheckIn(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.participantSessionFor(w, r, CapabilityParticipantFieldCheckIn, true)
	if !ok {
		return
	}
	var input struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Latitude == nil || input.Longitude == nil {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	point := fulfillment.Point{Latitude: *input.Latitude, Longitude: *input.Longitude}
	id := r.PathValue("participant_id")
	revision, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil || revision < 1 || r.URL.RawQuery != "" || !participantreview.UUID(id) || id == actor.Subject || !fieldKeyPattern.MatchString(r.Header.Get("Idempotency-Key")) || !validFieldPoint(point) {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.fulfillmentReader.(ParticipantReviews)
	if !ok {
		handler.fulfillmentError(w, r, errors.New("participant commander unconfigured"))
		return
	}
	receipt, replay, err := reader.FieldCheckInParticipant(r.Context(), actor, id, r.Header.Get("Idempotency-Key"), revision, point)
	if err != nil {
		handler.fulfillmentError(w, r, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, receipt)
}
func (handler *Handler) participantList(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.participantSession(w, r, false)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	statuses, ok := participantStatuses[kind]
	if !ok {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	f, err := participantreview.Parse(r.URL.RawQuery, statuses)
	if err != nil {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.fulfillmentReader.(ParticipantReviews)
	if !ok {
		handler.fulfillmentError(w, r, errors.New("participant reader unconfigured"))
		return
	}
	page, err := reader.ParticipantApplications(r.Context(), actor, kind, f)
	if err != nil {
		handler.fulfillmentError(w, r, err)
		return
	}
	if participantScopeRole(actor) == "REGIONAL_ADMIN" {
		writeJSON(w, 200, safeRegionalParticipantPage(page))
		return
	}
	writeJSON(w, 200, page)
}
func (handler *Handler) participantReject(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.participantSession(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8193))
	decoder.DisallowUnknownFields()
	revision, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil || revision < 1 || r.URL.RawQuery != "" || decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || participantStatuses[r.PathValue("kind")] == nil || !participantreview.UUID(r.PathValue("participant_id")) || r.PathValue("participant_id") == actor.Subject || !fieldKeyPattern.MatchString(r.Header.Get("Idempotency-Key")) || utf8.RuneCountInString(strings.TrimSpace(input.Reason)) < 8 || utf8.RuneCountInString(input.Reason) > 512 {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.fulfillmentReader.(ParticipantReviews)
	if !ok {
		handler.fulfillmentError(w, r, errors.New("participant commander unconfigured"))
		return
	}
	receipt, replay, err := reader.RejectParticipant(r.Context(), actor, r.PathValue("kind"), r.PathValue("participant_id"), r.Header.Get("Idempotency-Key"), revision, strings.TrimSpace(input.Reason))
	if err != nil {
		handler.fulfillmentError(w, r, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, receipt)
}
