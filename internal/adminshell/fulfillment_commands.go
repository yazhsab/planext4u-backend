package adminshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
)

const CapabilityFulfillmentCommand = "admin.fulfillment.command"

var fieldKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

func validFieldCommand(territory, key string, point fulfillment.Point) bool {
	id, err := uuid.Parse(territory)
	return err == nil && id.String() == territory && validFieldPoint(point) && fieldKeyPattern.MatchString(key)
}

type FulfillmentCommander interface {
	FieldCheckIn(context.Context, fulfillment.Actor, string, string, fulfillment.Point) (fulfillment.FieldCheckIn, bool, error)
}

func validFieldPoint(point fulfillment.Point) bool {
	return !math.IsNaN(point.Latitude) && !math.IsInf(point.Latitude, 0) && !math.IsNaN(point.Longitude) && !math.IsInf(point.Longitude, 0) && point.Latitude >= -90 && point.Latitude <= 90 && point.Longitude >= -180 && point.Longitude <= 180
}

func (reader *HTTPFulfillmentReader) FieldCheckIn(ctx context.Context, actor fulfillment.Actor, territory, key string, point fulfillment.Point) (fulfillment.FieldCheckIn, bool, error) {
	sessionID, sessionErr := uuid.Parse(actor.OperatorSessionID)
	if sessionErr != nil || sessionID.String() != actor.OperatorSessionID || actor.OperatorRevision < 1 || !actor.MFAVerified {
		return fulfillment.FieldCheckIn{}, false, ErrForbidden
	}
	if !validFieldCommand(territory, key, point) {
		return fulfillment.FieldCheckIn{}, false, ErrInvalidRequest
	}
	target := reader.endpoint
	target.Path = "/v1/operations/territories/" + territory + "/field-check-in"
	encoded, err := json.Marshal(map[string]any{"point": point})
	if err != nil {
		return fulfillment.FieldCheckIn{}, false, ErrInvalidRequest
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(encoded))
	if err != nil {
		return fulfillment.FieldCheckIn{}, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("X-Planext4u-Tenant", actor.TenantID)
	request.Header.Set("X-Planext4u-Country", actor.Country)
	request.Header.Set("X-Planext4u-Subject", actor.Subject)
	request.Header.Set("X-Planext4u-Roles", strings.Join(actor.Roles, ","))
	request.Header.Set("X-Planext4u-Operator-Session", actor.OperatorSessionID)
	request.Header.Set("X-Planext4u-Operator-Revision", strconv.FormatInt(actor.OperatorRevision, 10))
	if actor.MFAVerified {
		request.Header.Set("X-Planext4u-MFA", "verified")
	}
	response, err := reader.client.Do(request)
	if err != nil {
		return fulfillment.FieldCheckIn{}, false, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusForbidden:
		return fulfillment.FieldCheckIn{}, false, ErrForbidden
	case http.StatusNotFound:
		return fulfillment.FieldCheckIn{}, false, fulfillment.ErrNotFound
	case http.StatusConflict:
		return fulfillment.FieldCheckIn{}, false, fulfillment.ErrConflict
	case http.StatusUnprocessableEntity:
		return fulfillment.FieldCheckIn{}, false, ErrInvalidRequest
	case http.StatusCreated:
	default:
		return fulfillment.FieldCheckIn{}, false, errors.New("fulfillment command unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	var raw map[string]json.RawMessage
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &raw) != nil {
		return fulfillment.FieldCheckIn{}, false, errors.New("invalid field command response")
	}
	for _, field := range []string{"id", "officer_id", "territory_id", "point", "distance_m", "recorded_at"} {
		if len(raw[field]) == 0 || string(raw[field]) == "null" {
			return fulfillment.FieldCheckIn{}, false, errors.New("incomplete field command response")
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw["point"], &fields) != nil || len(fields["latitude"]) == 0 || len(fields["longitude"]) == 0 {
		return fulfillment.FieldCheckIn{}, false, errors.New("incomplete field command location")
	}
	var value fulfillment.FieldCheckIn
	if json.Unmarshal(data, &value) != nil || value.OfficerID != actor.Subject || value.TerritoryID != territory || value.RecordedAt.IsZero() || value.ID == "" || !validFieldPoint(value.Point) || value.Point != point || value.DistanceM < 0 || math.IsNaN(value.DistanceM) || math.IsInf(value.DistanceM, 0) {
		return fulfillment.FieldCheckIn{}, false, errors.New("field command response did not match")
	}
	return value, response.Header.Get("Idempotency-Replayed") == "true", nil
}

func (handler *Handler) fulfillmentFieldCheckIn(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityFulfillmentCommand)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, 403, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	if !assurance.MFASatisfied || !assurance.FreshAuth {
		writeProblem(writer, request, 403, "ADMIN_FRESH_MFA_REQUIRED", "Re-authenticate with MFA before recording a field visit.")
		return
	}
	if request.URL.RawQuery != "" {
		handler.fulfillmentError(writer, request, ErrInvalidRequest)
		return
	}
	var input struct {
		Point *struct {
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
		} `json:"point"`
	}
	if !decodeJSON(request, &input) || input.Point == nil || input.Point.Latitude == nil || input.Point.Longitude == nil {
		handler.fulfillmentError(writer, request, ErrInvalidRequest)
		return
	}
	point := fulfillment.Point{Latitude: *input.Point.Latitude, Longitude: *input.Point.Longitude}
	if !validFieldCommand(request.PathValue("territory_id"), request.Header.Get("Idempotency-Key"), point) {
		handler.fulfillmentError(writer, request, ErrInvalidRequest)
		return
	}
	commander, configured := handler.fulfillmentReader.(FulfillmentCommander)
	if !configured {
		handler.fulfillmentError(writer, request, errors.New("field command unconfigured"))
		return
	}
	actor := fulfillmentOperator(session, handler.clock())
	// FIELD_OFFICER is a protocol role only. The owning command requires the
	// caller's current explicit resource.command assignment, even for a global
	// operator; no platform-wide bypass is forwarded by this route.
	actor.Roles = []string{"FIELD_OFFICER"}
	actor.OperatorSessionID = session.Principal.SessionID
	actor.OperatorRevision = session.Principal.OperatorRevision
	value, replay, err := commander.FieldCheckIn(request.Context(), actor, request.PathValue("territory_id"), request.Header.Get("Idempotency-Key"), point)
	if err != nil {
		handler.fulfillmentError(writer, request, err)
		return
	}
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(writer, http.StatusCreated, value)
}

func (handler *Handler) fulfillmentFieldTerritories(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityFulfillmentCommand)
	if !ok {
		return
	}
	if !assuranceFor(session.Principal, handler.clock()).MFASatisfied {
		writeProblem(writer, request, 403, "ADMIN_MFA_REQUIRED", "Complete multi-factor authentication to continue.")
		return
	}
	if request.URL.RawQuery != "" {
		handler.fulfillmentError(writer, request, ErrInvalidRequest)
		return
	}
	if handler.organization == nil {
		handler.fulfillmentError(writer, request, errors.New("field scope unconfigured"))
		return
	}
	ids, err := handler.organization.FieldCommandTerritories(request.Context(), organizationActor(session))
	if err != nil {
		handler.fulfillmentError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"territory_ids": ids})
}
