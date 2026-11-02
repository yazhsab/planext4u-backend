package adminshell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
)

type ParticipantReviewDocument struct {
	Kind         string `json:"kind"`
	OCRStatus    string `json:"ocr_status"`
	ReviewReason string `json:"review_reason,omitempty"`
}

type ParticipantReviewVisit struct {
	ScheduledAt     time.Time  `json:"scheduled_at"`
	CheckedInAt     *time.Time `json:"checked_in_at,omitempty"`
	CheckInDistance float64    `json:"check_in_distance_m,omitempty"`
}

type ParticipantReviewDetail struct {
	ID                  string                      `json:"id"`
	ParticipantID       string                      `json:"participant_id"`
	Kind                string                      `json:"kind"`
	Revision            int64                       `json:"revision"`
	Status              string                      `json:"status"`
	DisplayName         string                      `json:"display_name"`
	Category            string                      `json:"category"`
	ContactName         string                      `json:"contact_name,omitempty"`
	PhoneMasked         string                      `json:"phone_masked,omitempty"`
	VehicleNumberMasked string                      `json:"vehicle_number_masked,omitempty"`
	Documents           []ParticipantReviewDocument `json:"documents"`
	FieldVisit          *ParticipantReviewVisit     `json:"field_visit,omitempty"`
	ServiceZoneCount    int                         `json:"service_zone_count,omitempty"`
	Zones               []string                    `json:"zones,omitempty"`
	BankStatus          string                      `json:"bank_status"`
	BankLast4           string                      `json:"bank_last4,omitempty"`
	LastReviewReason    string                      `json:"last_review_reason,omitempty"`
	ReviewActions       []string                    `json:"review_actions"`
	UpdatedAt           time.Time                   `json:"updated_at"`
}

// Regional review responses have their own wire shape. Empty country-only
// evidence fields are omitted entirely, including their field names.
type regionalReviewVisitResponse struct {
	ScheduledAt time.Time  `json:"scheduled_at"`
	CheckedInAt *time.Time `json:"checked_in_at,omitempty"`
}

type regionalReviewDetailResponse struct {
	ID               string                       `json:"id"`
	ParticipantID    string                       `json:"participant_id"`
	Kind             string                       `json:"kind"`
	Revision         int64                        `json:"revision"`
	Status           string                       `json:"status"`
	DisplayName      string                       `json:"display_name"`
	Category         string                       `json:"category"`
	FieldVisit       *regionalReviewVisitResponse `json:"field_visit,omitempty"`
	ServiceZoneCount int                          `json:"service_zone_count"`
	UpdatedAt        time.Time                    `json:"updated_at"`
}

func safeRegionalReviewDetail(value ParticipantReviewDetail) regionalReviewDetailResponse {
	result := regionalReviewDetailResponse{
		ID: value.ID, ParticipantID: value.ParticipantID, Kind: value.Kind,
		Revision: value.Revision, Status: value.Status, DisplayName: value.DisplayName,
		Category: value.Category, ServiceZoneCount: value.ServiceZoneCount, UpdatedAt: value.UpdatedAt,
	}
	if value.FieldVisit != nil {
		result.FieldVisit = &regionalReviewVisitResponse{ScheduledAt: value.FieldVisit.ScheduledAt, CheckedInAt: value.FieldVisit.CheckedInAt}
	}
	return result
}

type ParticipantCountryReviews interface {
	ParticipantReviewDetail(context.Context, fulfillment.Actor, string, string) (ParticipantReviewDetail, error)
	ReviewParticipant(context.Context, fulfillment.Actor, string, string, string, string, int64, string) (ParticipantReceipt, bool, error)
}

var vendorReviewTargets = map[string]string{
	"START_OCR_REVIEW":    "OCR_REVIEW",
	"VERIFY_KYC":          "KYC_REVIEW",
	"REQUIRE_FIELD_VISIT": "FIELD_VISIT_REQUIRED",
	"VERIFY_BANK":         "BANK_REVIEW",
	"APPROVE":             "APPROVED",
	"REJECT":              "REJECTED",
}

func countryParticipantActor(actor fulfillment.Actor) bool {
	return len(actor.Roles) >= 1 && actor.Roles[0] == "OPS_ADMIN" && (len(actor.Roles) == 1 || len(actor.Roles) == 2 && actor.Roles[1] == "FINANCE")
}

func regionalVendorReadActor(actor fulfillment.Actor, kind string) bool {
	return kind == "VENDOR" && len(actor.Roles) == 1 && actor.Roles[0] == "REGIONAL_ADMIN"
}

func financeParticipantActor(actor fulfillment.Actor) bool {
	return countryParticipantActor(actor) && len(actor.Roles) == 2 && actor.Roles[1] == "FINANCE"
}

func validReviewActions(value ParticipantReviewDetail) bool {
	if value.ReviewActions == nil || len(value.ReviewActions) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, action := range value.ReviewActions {
		if seen[action] {
			return false
		}
		seen[action] = true
		if value.Kind == "VENDOR" {
			switch action {
			case "START_OCR_REVIEW":
				if value.Status != "DOCUMENTS_SUBMITTED" {
					return false
				}
			case "VERIFY_KYC":
				if value.Status != "OCR_REVIEW" {
					return false
				}
			case "REQUIRE_FIELD_VISIT":
				if value.Status != "KYC_REVIEW" {
					return false
				}
			case "VERIFY_BANK":
				if value.Status != "BANK_REVIEW" || value.BankStatus == "" || value.BankStatus == "VERIFIED" {
					return false
				}
			case "APPROVE":
				if value.Status != "BANK_REVIEW" || value.BankStatus != "VERIFIED" || value.ServiceZoneCount < 1 || value.FieldVisit == nil || value.FieldVisit.CheckedInAt == nil {
					return false
				}
			case "REJECT":
				if value.Status != "OCR_REVIEW" && value.Status != "KYC_REVIEW" && value.Status != "BANK_REVIEW" {
					return false
				}
			default:
				return false
			}
		} else if value.Kind == "RIDER" {
			if value.Status != "KYC_REVIEW" || action != "APPROVE" && action != "REJECT" {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

func (reader *HTTPFulfillmentReader) ParticipantReviewDetail(ctx context.Context, actor fulfillment.Actor, kind, id string) (ParticipantReviewDetail, error) {
	regional := regionalVendorReadActor(actor, kind)
	if !countryParticipantActor(actor) && !regional || participantStatuses[kind] == nil || !participantreview.UUID(id) || id == actor.Subject {
		return ParticipantReviewDetail{}, ErrForbidden
	}
	role := "OPS_ADMIN"
	path := "/v1/rider/applications/" + id + "/review"
	if kind == "VENDOR" {
		if regional {
			role = "REGIONAL_ADMIN"
		} else if financeParticipantActor(actor) {
			role = "OPS_ADMIN,FINANCE"
		}
		path = "/v1/vendor/applications/" + id + "/review"
	}
	data, _, err := reader.participantRequest(ctx, actor, kind, http.MethodGet, path, role, nil, nil, "", 0)
	if err != nil {
		return ParticipantReviewDetail{}, err
	}
	var value ParticipantReviewDetail
	value.Kind, value.ParticipantID = kind, id
	if kind == "VENDOR" {
		var owner struct {
			ID               string                      `json:"id"`
			VendorID         string                      `json:"vendor_id"`
			Revision         int64                       `json:"revision"`
			Status           string                      `json:"status"`
			BusinessName     string                      `json:"business_name"`
			BusinessType     string                      `json:"business_type"`
			ContactName      string                      `json:"contact_name"`
			Documents        []ParticipantReviewDocument `json:"documents"`
			FieldVisit       *ParticipantReviewVisit     `json:"field_visit"`
			ServiceZoneCount int                         `json:"service_zone_count"`
			BankStatus       string                      `json:"bank_status"`
			BankLast4        string                      `json:"bank_last4"`
			LastReviewReason string                      `json:"last_review_reason"`
			ReviewActions    []string                    `json:"review_actions"`
			UpdatedAt        time.Time                   `json:"updated_at"`
		}
		if json.Unmarshal(data, &owner) != nil || owner.VendorID != id || owner.BusinessName == "" || owner.BusinessType == "" || !regional && owner.ContactName == "" || owner.ServiceZoneCount < 0 || owner.Documents == nil || owner.ReviewActions == nil || owner.BankLast4 != "" && !fourDigits(owner.BankLast4) {
			return ParticipantReviewDetail{}, errors.New("invalid vendor review detail")
		}
		if regional && (owner.ContactName != "" || len(owner.Documents) != 0 || owner.BankStatus != "" || owner.BankLast4 != "" || owner.LastReviewReason != "" || len(owner.ReviewActions) != 0 || !regionalVendorOwnerFields(data)) {
			return ParticipantReviewDetail{}, errors.New("regional vendor review contains private or country-only fields")
		}
		value.ID, value.Revision, value.Status, value.DisplayName, value.Category, value.ContactName = owner.ID, owner.Revision, owner.Status, owner.BusinessName, owner.BusinessType, owner.ContactName
		value.Documents, value.FieldVisit, value.ServiceZoneCount, value.BankStatus, value.BankLast4, value.LastReviewReason, value.ReviewActions, value.UpdatedAt = owner.Documents, owner.FieldVisit, owner.ServiceZoneCount, owner.BankStatus, owner.BankLast4, owner.LastReviewReason, owner.ReviewActions, owner.UpdatedAt
	} else {
		var owner struct {
			ID                  string                      `json:"id"`
			RiderID             string                      `json:"rider_id"`
			Revision            int64                       `json:"revision"`
			Status              string                      `json:"status"`
			FullName            string                      `json:"full_name"`
			PhoneMasked         string                      `json:"phone_masked"`
			VehicleType         string                      `json:"vehicle_type"`
			VehicleNumberMasked string                      `json:"vehicle_number_masked"`
			Documents           []ParticipantReviewDocument `json:"documents"`
			BankStatus          string                      `json:"bank_status"`
			Zones               []string                    `json:"zones"`
			ReviewReason        string                      `json:"review_reason"`
			ReviewActions       []string                    `json:"review_actions"`
			UpdatedAt           time.Time                   `json:"updated_at"`
		}
		if json.Unmarshal(data, &owner) != nil || owner.RiderID != id || owner.FullName == "" || owner.VehicleType == "" || owner.Documents == nil || owner.Zones == nil || owner.ReviewActions == nil {
			return ParticipantReviewDetail{}, errors.New("invalid rider review detail")
		}
		value.ID, value.Revision, value.Status, value.DisplayName, value.Category = owner.ID, owner.Revision, owner.Status, owner.FullName, owner.VehicleType
		value.PhoneMasked, value.VehicleNumberMasked, value.Documents, value.Zones, value.BankStatus, value.LastReviewReason, value.ReviewActions, value.UpdatedAt = owner.PhoneMasked, owner.VehicleNumberMasked, owner.Documents, owner.Zones, owner.BankStatus, owner.ReviewReason, owner.ReviewActions, owner.UpdatedAt
	}
	if !participantreview.UUID(value.ID) || value.Revision < 1 || value.UpdatedAt.IsZero() || value.Status == "" || (participantreview.Filter{Status: value.Status}).Validate(participantStatuses[kind]) != nil || !validReviewActions(value) {
		return ParticipantReviewDetail{}, errors.New("invalid participant review projection")
	}
	for _, action := range value.ReviewActions {
		if action == "VERIFY_BANK" && !financeParticipantActor(actor) {
			return ParticipantReviewDetail{}, errors.New("unassigned finance review action")
		}
	}
	for _, document := range value.Documents {
		if document.Kind == "" || document.OCRStatus == "" {
			return ParticipantReviewDetail{}, errors.New("invalid participant document projection")
		}
	}
	if value.FieldVisit != nil && (value.FieldVisit.ScheduledAt.IsZero() || value.FieldVisit.CheckedInAt != nil && value.FieldVisit.CheckedInAt.IsZero() || value.FieldVisit.CheckInDistance < 0) {
		return ParticipantReviewDetail{}, errors.New("invalid participant visit projection")
	}
	return value, nil
}

func regionalVendorOwnerFields(data []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	allowed := map[string]bool{
		"id": true, "vendor_id": true, "revision": true, "status": true,
		"business_name": true, "business_type": true, "contact_name": true,
		"documents": true, "field_visit": true, "service_zone_count": true,
		"bank_status": true, "bank_last4": true, "last_review_reason": true,
		"review_actions": true, "updated_at": true,
	}
	for field := range fields {
		if !allowed[field] {
			return false
		}
	}
	if visit, ok := fields["field_visit"]; ok && string(visit) != "null" {
		var visitFields map[string]json.RawMessage
		if json.Unmarshal(visit, &visitFields) != nil {
			return false
		}
		for field := range visitFields {
			if field != "scheduled_at" && field != "checked_in_at" {
				return false
			}
		}
	}
	return true
}

func fourDigits(value string) bool {
	if len(value) != 4 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (reader *HTTPFulfillmentReader) ReviewParticipant(ctx context.Context, actor fulfillment.Actor, kind, id, action, key string, revision int64, reason string) (ParticipantReceipt, bool, error) {
	if !countryParticipantActor(actor) || !participantreview.UUID(id) || id == actor.Subject || !fieldKeyPattern.MatchString(key) || revision < 1 || utf8.RuneCountInString(strings.TrimSpace(reason)) < 8 || utf8.RuneCountInString(reason) > 512 {
		return ParticipantReceipt{}, false, ErrInvalidRequest
	}
	reason = strings.TrimSpace(reason)
	role := "OPS_ADMIN"
	ownerID := "vendor_id"
	path := "/v1/vendor/applications/" + id + "/transitions"
	body := map[string]any{"status": vendorReviewTargets[action], "reason": reason}
	expected := vendorReviewTargets[action]
	if kind == "VENDOR" {
		if expected == "" {
			return ParticipantReceipt{}, false, ErrInvalidRequest
		}
		if action == "VERIFY_BANK" {
			if !financeParticipantActor(actor) {
				return ParticipantReceipt{}, false, ErrForbidden
			}
			path = "/v1/vendor/applications/" + id + "/bank-verification"
			role = "FINANCE"
			body = map[string]any{"reason": reason}
		}
	} else if kind == "RIDER" {
		if action != "APPROVE" && action != "REJECT" {
			return ParticipantReceipt{}, false, ErrInvalidRequest
		}
		path = "/v1/rider/applications/" + id + "/review"
		ownerID = "rider_id"
		body = map[string]any{"approved": action == "APPROVE", "reason": reason}
		if action == "APPROVE" {
			expected = "APPROVED"
		} else {
			expected = "SUSPENDED"
		}
	} else {
		return ParticipantReceipt{}, false, ErrInvalidRequest
	}
	data, replay, err := reader.participantRequest(ctx, actor, kind, http.MethodPost, path, role, nil, body, key, revision)
	if err != nil {
		return ParticipantReceipt{}, false, err
	}
	var raw map[string]json.RawMessage
	var value ParticipantReceipt
	var participant string
	if json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil || json.Unmarshal(raw[ownerID], &participant) != nil || participant != id || value.Status != expected || value.Revision != revision+1 {
		return ParticipantReceipt{}, false, errors.New("participant review command response did not match")
	}
	value.Kind, value.ParticipantID = kind, id
	return value, replay, nil
}

func (handler *Handler) countryParticipantSession(w http.ResponseWriter, r *http.Request, write bool) (fulfillment.Actor, bool) {
	actor, ok := handler.participantSession(w, r, write)
	if !ok {
		return fulfillment.Actor{}, false
	}
	if !countryParticipantActor(actor) {
		writeProblem(w, r, 403, "ADMIN_COUNTRY_REVIEW_REQUIRED", "Current country review authority is required.")
		return fulfillment.Actor{}, false
	}
	return actor, true
}

func (handler *Handler) participantReviewDetail(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.participantSession(w, r, false)
	if !ok {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("participant_id")
	if !countryParticipantActor(actor) && !regionalVendorReadActor(actor, kind) {
		writeProblem(w, r, 403, "ADMIN_COUNTRY_REVIEW_REQUIRED", "Current scoped participant review authority is required.")
		return
	}
	if r.URL.RawQuery != "" || participantStatuses[kind] == nil || !participantreview.UUID(id) || id == actor.Subject {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.fulfillmentReader.(ParticipantCountryReviews)
	if !ok {
		handler.fulfillmentError(w, r, errors.New("participant detail unconfigured"))
		return
	}
	value, err := reader.ParticipantReviewDetail(r.Context(), actor, kind, id)
	if err != nil {
		handler.fulfillmentError(w, r, err)
		return
	}
	if regionalVendorReadActor(actor, kind) {
		writeJSON(w, 200, safeRegionalReviewDetail(value))
		return
	}
	writeJSON(w, 200, value)
}

func (handler *Handler) participantReviewAction(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.countryParticipantSession(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("participant_id")
	revision, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil || revision < 1 || r.URL.RawQuery != "" || participantStatuses[kind] == nil || !participantreview.UUID(id) || id == actor.Subject || !fieldKeyPattern.MatchString(r.Header.Get("Idempotency-Key")) || utf8.RuneCountInString(strings.TrimSpace(input.Reason)) < 8 || utf8.RuneCountInString(input.Reason) > 512 || kind == "VENDOR" && vendorReviewTargets[input.Action] == "" || kind == "RIDER" && input.Action != "APPROVE" && input.Action != "REJECT" {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.fulfillmentReader.(ParticipantCountryReviews)
	if !ok {
		handler.fulfillmentError(w, r, errors.New("participant reviewer unconfigured"))
		return
	}
	value, replay, err := reader.ReviewParticipant(r.Context(), actor, kind, id, input.Action, r.Header.Get("Idempotency-Key"), revision, input.Reason)
	if err != nil {
		handler.fulfillmentError(w, r, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, value)
}
