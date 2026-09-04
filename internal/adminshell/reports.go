package adminshell

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
)

func (handler *Handler) listReports(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityGovernanceRead)
	if !ok {
		return
	}
	query, err := parseReportQuery(request, true)
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	page, err := handler.reports.List(request.Context(), session.Principal, query)
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) reportDetail(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, CapabilityGovernanceRead)
	if !ok {
		return
	}
	query, err := parseReportQuery(request, false)
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	detail, err := handler.reports.Detail(request.Context(), session.Principal, request.PathValue("report_id"), query)
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (handler *Handler) createReportExport(writer http.ResponseWriter, request *http.Request) {
	session, capabilities, ok := handler.authorize(writer, request, adminops.CapabilityReporting)
	if !ok {
		return
	}
	if !handler.csrfAllowed(request, session.CSRFToken) {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_CSRF_INVALID", "The request safety token is invalid. Refresh and try again.")
		return
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	if !assurance.MFASatisfied || !assurance.FreshAuth {
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_FRESH_MFA_REQUIRED", "Re-authenticate with multi-factor authentication to export reports.")
		return
	}
	var input struct {
		Format string `json:"format"`
		Reason string `json:"reason"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	if !decodeJSON(request, &input) || strings.ToUpper(strings.TrimSpace(input.Format)) != "CSV" || len(strings.TrimSpace(input.Reason)) < 8 || len(input.Reason) > 500 {
		handler.writeReportError(writer, request, ErrReportInvalid)
		return
	}
	query, err := parseReportTimeRange(input.From, input.To)
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	validationQuery := query
	validationQuery.Limit = 1
	if _, err := handler.reports.Detail(request.Context(), session.Principal, request.PathValue("report_id"), validationQuery); err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	correlationID := strings.TrimSpace(request.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = fmt.Sprintf("admin-report-%d", handler.clock().UTC().UnixNano())
	}
	payload := map[string]any{"format": "CSV", "privacy_mode": "aggregate_and_masked"}
	if !query.From.IsZero() {
		payload["from"] = query.From.UTC().Format(time.RFC3339)
	}
	if !query.To.IsZero() {
		payload["to"] = query.To.UTC().Format(time.RFC3339)
	}
	change, err := handler.operations.Submit(operationPrincipal(session.Principal, capabilities), adminops.Command{
		Domain: adminops.DomainReporting, Action: adminops.ActionReportingExport, TargetID: request.PathValue("report_id"),
		Reason: strings.TrimSpace(input.Reason), Payload: payload, CorrelationID: correlationID,
	})
	if err != nil {
		handler.writeOperationError(writer, request, err)
		return
	}
	if change.Status != adminops.StatusExecuted {
		writeProblem(writer, request, http.StatusConflict, "ADMIN_REPORT_EXPORT_PENDING_APPROVAL", "A different authorized administrator must approve this export request.")
		return
	}
	value, err := handler.reports.CreateExport(request.Context(), session.Principal, request.PathValue("report_id"), query, change.ID)
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusAccepted, value)
}

func (handler *Handler) reportExport(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, adminops.CapabilityReporting)
	if !ok {
		return
	}
	value, err := handler.reports.Export(request.Context(), session.Principal, request.PathValue("export_id"))
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, value)
}

func (handler *Handler) downloadReportExport(writer http.ResponseWriter, request *http.Request) {
	session, _, ok := handler.authorize(writer, request, adminops.CapabilityReporting)
	if !ok {
		return
	}
	value, err := handler.reports.Download(request.Context(), session.Principal, request.PathValue("export_id"), request.URL.Query().Get("token"))
	if err != nil {
		handler.writeReportError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", value.ContentType)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(value.FileName, `"`, "")+`"`)
	writer.Header().Set("Content-Length", strconv.Itoa(len(value.Bytes)))
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(value.Bytes)
}

func parseReportQuery(request *http.Request, includeDomain bool) (ReportQuery, error) {
	query := ReportQuery{Cursor: request.URL.Query().Get("cursor"), Limit: 20}
	if includeDomain {
		query.Domain = request.URL.Query().Get("domain")
	}
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return ReportQuery{}, ErrReportInvalid
		}
		query.Limit = limit
	}
	rangeQuery, err := parseReportTimeRange(request.URL.Query().Get("from"), request.URL.Query().Get("to"))
	if err != nil {
		return ReportQuery{}, err
	}
	query.From, query.To = rangeQuery.From, rangeQuery.To
	return query, nil
}

func parseReportTimeRange(from, to string) (ReportQuery, error) {
	var query ReportQuery
	var err error
	if from != "" {
		query.From, err = time.Parse(time.RFC3339, from)
		if err != nil {
			return ReportQuery{}, ErrReportInvalid
		}
	}
	if to != "" {
		query.To, err = time.Parse(time.RFC3339, to)
		if err != nil {
			return ReportQuery{}, ErrReportInvalid
		}
	}
	if !validReportRange(query.From, query.To) {
		return ReportQuery{}, ErrReportInvalid
	}
	return query, nil
}

func (handler *Handler) writeReportError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrReportNotFound), errors.Is(err, ErrExportNotFound):
		writeProblem(writer, request, http.StatusNotFound, "ADMIN_REPORT_NOT_FOUND", "The report or export is not available in this country context.")
	case errors.Is(err, ErrExportExpired):
		writeProblem(writer, request, http.StatusGone, "ADMIN_REPORT_EXPORT_EXPIRED", "The report export has expired. Request a new export.")
	case errors.Is(err, ErrExportToken):
		writeProblem(writer, request, http.StatusForbidden, "ADMIN_REPORT_EXPORT_TOKEN_INVALID", "The report download link is invalid.")
	case errors.Is(err, ErrReportInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADMIN_REPORT_FILTER_INVALID", "Check the report filters and try again.")
	default:
		writeProblem(writer, request, http.StatusServiceUnavailable, "ADMIN_REPORT_UNAVAILABLE", "Reporting is temporarily unavailable.")
	}
}
