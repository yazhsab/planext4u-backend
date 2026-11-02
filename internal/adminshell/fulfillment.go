package adminshell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
)

const CapabilityFulfillmentRead = "admin.fulfillment.read"

type FulfillmentReader interface {
	Territories(context.Context, fulfillment.Actor) ([]fulfillment.Territory, error)
	Dashboard(context.Context, fulfillment.Actor, string) (fulfillment.RegionalDashboard, error)
}

type HTTPFulfillmentReader struct {
	endpoint url.URL
	client   http.Client
}

func NewHTTPFulfillmentReader(endpoint *url.URL, client *http.Client) (*HTTPFulfillmentReader, error) {
	if endpoint == nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || client == nil || client.Timeout <= 0 || client.Timeout > 10*time.Second {
		return nil, ErrInvalidRequest
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPFulfillmentReader{endpoint: *endpoint, client: copyClient}, nil
}

func (reader *HTTPFulfillmentReader) fetch(ctx context.Context, actor fulfillment.Actor, path string, destination any) error {
	target := reader.endpoint
	target.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Planext4u-Tenant", actor.TenantID)
	request.Header.Set("X-Planext4u-Country", actor.Country)
	request.Header.Set("X-Planext4u-Subject", actor.Subject)
	request.Header.Set("X-Planext4u-Roles", strings.Join(actor.Roles, ","))
	if actor.OperatorSessionID != "" && actor.OperatorRevision > 0 {
		request.Header.Set("X-Planext4u-Operator-Session", actor.OperatorSessionID)
		request.Header.Set("X-Planext4u-Operator-Revision", strconv.FormatInt(actor.OperatorRevision, 10))
	}
	if actor.MFAVerified {
		request.Header.Set("X-Planext4u-MFA", "verified")
	}
	response, err := reader.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return fulfillment.ErrNotFound
	case http.StatusOK:
	default:
		return errors.New("fulfillment owner unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 524289))
	if err != nil || len(data) > 524288 || json.Unmarshal(data, destination) != nil {
		return errors.New("fulfillment owner returned an invalid response")
	}
	return nil
}

func (reader *HTTPFulfillmentReader) Territories(ctx context.Context, actor fulfillment.Actor) ([]fulfillment.Territory, error) {
	var payload struct {
		Items []fulfillment.Territory `json:"items"`
	}
	if err := reader.fetch(ctx, actor, "/v1/operations/territories", &payload); err != nil {
		return nil, err
	}
	if payload.Items == nil {
		return nil, errors.New("fulfillment territory list is missing")
	}
	return payload.Items, nil
}

func (reader *HTTPFulfillmentReader) Dashboard(ctx context.Context, actor fulfillment.Actor, region string) (fulfillment.RegionalDashboard, error) {
	var value fulfillment.RegionalDashboard
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(region) {
		return value, ErrInvalidRequest
	}
	var document map[string]json.RawMessage
	err := reader.fetch(ctx, actor, "/v1/operations/regions/"+region+"/dashboard", &document)
	if err != nil {
		return value, err
	}
	for _, field := range []string{"region_id", "territories", "online_riders", "unassigned_tasks", "at_risk_tasks", "completed_today", "generated_at", "latest_locations", "recent_field_check_ins"} {
		if raw := document[field]; len(raw) == 0 || string(raw) == "null" {
			return value, errors.New("fulfillment dashboard is incomplete")
		}
	}
	data, _ := json.Marshal(document)
	if json.Unmarshal(data, &value) != nil || value.GeneratedAt.IsZero() || value.Territories < 0 || value.OnlineRiders < 0 || value.UnassignedTasks < 0 || value.AtRiskTasks < 0 || value.CompletedToday < 0 {
		return fulfillment.RegionalDashboard{}, errors.New("fulfillment dashboard is invalid")
	}
	// Country payout totals have no safe regional attribution. A regional
	// response containing them is an owner contract violation, even if the
	// owning service accidentally emits a value for the requested region.
	if !countryParticipantActor(actor) && value.PayoutsPending != nil {
		return fulfillment.RegionalDashboard{}, errors.New("regional fulfillment dashboard included country payout totals")
	}
	if value.RegionID != region {
		return fulfillment.RegionalDashboard{}, errors.New("fulfillment region did not match request")
	}
	return value, err
}

func fulfillmentOperator(session ResolvedSession, now time.Time) fulfillment.Actor {
	roles := []string{"REGIONAL_ADMIN"}
	for _, role := range session.Principal.Roles {
		if role == RoleSuperAdmin || role == RoleCountryAdmin {
			roles = []string{"OPS_ADMIN"}
			break
		}
	}
	return fulfillment.Actor{TenantID: session.Principal.TenantID, Country: session.Principal.SelectedCountry, Subject: session.Principal.SubjectID, Roles: roles, MFAVerified: assuranceFor(session.Principal, now).MFASatisfied, OperatorSessionID: session.Principal.SessionID, OperatorRevision: session.Principal.OperatorRevision}
}

func (handler *Handler) fulfillmentTerritories(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityFulfillmentRead)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeProblem(writer, request, 422, "ADMIN_FULFILLMENT_REQUEST_INVALID", "This read uses your current operator scope.")
		return
	}
	if handler.fulfillmentReader == nil {
		handler.fulfillmentError(writer, request, errors.New("fulfillment reader unconfigured"))
		return
	}
	items, err := handler.fulfillmentReader.Territories(request.Context(), fulfillmentOperator(session, handler.clock()))
	if err != nil {
		handler.fulfillmentError(writer, request, err)
		return
	}
	writeJSON(writer, 200, map[string]any{"items": items})
}

func (handler *Handler) fulfillmentDashboard(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityFulfillmentRead)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeProblem(writer, request, 422, "ADMIN_FULFILLMENT_REQUEST_INVALID", "This read uses your current operator scope.")
		return
	}
	if handler.fulfillmentReader == nil {
		handler.fulfillmentError(writer, request, errors.New("fulfillment reader unconfigured"))
		return
	}
	value, err := handler.fulfillmentReader.Dashboard(request.Context(), fulfillmentOperator(session, handler.clock()), request.PathValue("region_id"))
	if err != nil {
		handler.fulfillmentError(writer, request, err)
		return
	}
	writeJSON(writer, 200, value)
}

func (handler *Handler) fulfillmentError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		writeProblem(writer, request, 403, "ADMIN_FULFILLMENT_FORBIDDEN", "Your current assignments do not allow this fulfillment resource.")
	case errors.Is(err, fulfillment.ErrNotFound):
		writeProblem(writer, request, 404, "ADMIN_FULFILLMENT_NOT_FOUND", "The fulfillment resource is unavailable.")
	case errors.Is(err, ErrInvalidRequest):
		writeProblem(writer, request, 422, "ADMIN_FULFILLMENT_REQUEST_INVALID", "Choose a valid delivery region.")
	case errors.Is(err, fulfillment.ErrConflict):
		writeProblem(writer, request, 409, "ADMIN_FULFILLMENT_CONFLICT", "This request conflicts with an earlier command. Retry the same recorded visit or refresh before starting a new one.")
	default:
		writeProblem(writer, request, 503, "ADMIN_FULFILLMENT_UNAVAILABLE", "Delivery operations could not be loaded. Retry when the owning service is available.")
	}
}
