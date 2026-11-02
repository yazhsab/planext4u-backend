package adminshell

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
)

const CapabilityFulfillmentExport = "admin.fulfillment.export"

type FulfillmentExporter interface {
	RegionalSummaryExport(context.Context, fulfillment.Actor, string) (fulfillment.RegionalExport, error)
}

func (reader *HTTPFulfillmentReader) RegionalSummaryExport(ctx context.Context, actor fulfillment.Actor, region string) (fulfillment.RegionalExport, error) {
	source, sourceErr := uuid.Parse(actor.OperatorSessionID)
	if !actor.MFAVerified || sourceErr != nil || source.String() != actor.OperatorSessionID || actor.OperatorRevision < 1 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(region) {
		return fulfillment.RegionalExport{}, ErrInvalidRequest
	}
	var raw map[string]json.RawMessage
	if err := reader.fetch(ctx, actor, "/v1/operations/regions/"+region+"/summary-export", &raw); err != nil {
		return fulfillment.RegionalExport{}, err
	}
	for _, name := range []string{"country", "region_id", "territories", "online_riders", "unassigned_tasks", "at_risk_tasks", "completed_today", "generated_at"} {
		if len(raw[name]) == 0 || string(raw[name]) == "null" {
			return fulfillment.RegionalExport{}, errors.New("incomplete fulfillment export")
		}
	}
	encoded, _ := json.Marshal(raw)
	var result fulfillment.RegionalExport
	if json.Unmarshal(encoded, &result) != nil || result.Country != actor.Country || result.RegionID != region || result.Territories < 1 || result.OnlineRiders < 0 || result.UnassignedTasks < 0 || result.AtRiskTasks < 0 || result.CompletedToday < 0 || result.GeneratedAt.IsZero() {
		return fulfillment.RegionalExport{}, errors.New("invalid fulfillment export")
	}
	return result, nil
}

func (handler *Handler) fulfillmentSummaryExport(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityFulfillmentExport)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		handler.fulfillmentError(writer, request, ErrInvalidRequest)
		return
	}
	if !assuranceFor(session.Principal, handler.clock()).FreshAuth {
		writeProblem(writer, request, 403, "ADMIN_FRESH_AUTH_REQUIRED", "Sign in again before exporting delivery operations.")
		return
	}
	exporter, configured := handler.fulfillmentReader.(FulfillmentExporter)
	if !configured {
		handler.fulfillmentError(writer, request, errors.New("fulfillment exporter unavailable"))
		return
	}
	actor := fulfillmentOperator(session, handler.clock())
	actor.OperatorSessionID = session.Principal.SessionID
	actor.OperatorRevision = session.Principal.OperatorRevision
	result, err := exporter.RegionalSummaryExport(request.Context(), actor, request.PathValue("region_id"))
	if err != nil {
		handler.fulfillmentError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="delivery-summary.csv"`)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	out := csv.NewWriter(writer)
	_ = out.Write([]string{"Country", "Region", "Generated at (UTC)", "Territories", "Online riders", "Unassigned tasks", "At risk tasks", "Completed today (UTC)"})
	_ = out.Write([]string{organizationCSVText(result.Country), organizationCSVText(result.RegionID), result.GeneratedAt.UTC().Format(time.RFC3339Nano), strconv.Itoa(result.Territories), strconv.Itoa(result.OnlineRiders), strconv.Itoa(result.UnassignedTasks), strconv.Itoa(result.AtRiskTasks), strconv.Itoa(result.CompletedToday)})
	out.Flush()
}
