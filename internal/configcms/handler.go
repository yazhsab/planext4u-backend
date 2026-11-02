package configcms

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	service     *Service
	audiences   AudienceMembershipResolver
	startup     PublicStartupConfig
	occupations OccupationDirectory
	geography   GeographyDirectory
}

func NewHandler(service *Service) (http.Handler, error) {
	return NewHandlerWithAudiences(service, nil)
}

func NewHandlerWithAudiences(service *Service, audiences AudienceMembershipResolver) (http.Handler, error) {
	return NewHandlerWithPublicStartup(service, audiences, PublicStartupConfig{})
}

func NewHandlerWithPublicStartup(service *Service, audiences AudienceMembershipResolver, startup PublicStartupConfig) (http.Handler, error) {
	return NewHandlerWithOccupations(service, audiences, startup, nil)
}

func NewHandlerWithOccupations(service *Service, audiences AudienceMembershipResolver, startup PublicStartupConfig, occupations OccupationDirectory) (http.Handler, error) {
	return NewHandlerWithReferenceDirectories(service, audiences, startup, occupations, nil)
}

func NewHandlerWithReferenceDirectories(service *Service, audiences AudienceMembershipResolver, startup PublicStartupConfig, occupations OccupationDirectory, geography GeographyDirectory) (http.Handler, error) {
	if service == nil {
		return nil, errors.New("configuration service is required")
	}
	if !startup.Valid() {
		return nil, ErrInvalidSnapshot
	}
	startup.Countries = append([]string(nil), startup.Countries...)
	handler := &Handler{service: service, audiences: audiences, startup: startup}
	handler.occupations = occupations
	handler.geography = geography
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/startup", handler.publicStartup)
	mux.HandleFunc("GET /v1/bootstrap", handler.bootstrap)
	mux.HandleFunc("GET /v1/occupations", handler.occupationList)
	mux.HandleFunc("GET /v1/geography", handler.geographyList)
	mux.HandleFunc("GET /v1/pages/{page_id}", handler.page)
	return mux, nil
}

func (handler *Handler) page(writer http.ResponseWriter, request *http.Request) {
	tenantID := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant"))
	country := strings.TrimSpace(request.Header.Get("X-Planext4u-Country"))
	result, err := handler.service.Page(request.Context(), tenantID, country, request.PathValue("page_id"), request.URL.Query().Get("locale"))
	if err == nil && !pageVisible(result.Page, request.Header.Get("X-Planext4u-Roles")) {
		err = ErrNotFound
	}
	if err == nil {
		var visible []Page
		visible, err = handler.visibleAudiencePages(request, tenantID, country, []Page{result.Page})
		if err == nil && len(visible) == 0 {
			err = ErrNotFound
		}
	}
	if err != nil {
		status, code, message := http.StatusUnprocessableEntity, "PAGE_REQUEST_INVALID", "The page request is invalid."
		if errors.Is(err, ErrNotFound) {
			status, code, message = http.StatusNotFound, "PAGE_NOT_FOUND", "The page is unavailable for this region."
		}
		if errors.Is(err, errAudienceUnavailable) {
			status, code, message = http.StatusServiceUnavailable, "PAGE_AUDIENCE_UNAVAILABLE", "Page access could not be checked. Try again."
		}
		writeProblem(writer, request, status, code, message)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	// A browser/private intermediary cache must not reuse one account's audience
	// selection after logout. Mobile durable caches are explicitly account scoped.
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"config-`+strconv.FormatInt(result.Revision, 10)+`-page-`+result.Page.ID+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler *Handler) bootstrap(writer http.ResponseWriter, request *http.Request) {
	tenantID := strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant"))
	country := strings.TrimSpace(request.Header.Get("X-Planext4u-Country"))
	result, err := handler.service.Bootstrap(
		request.Context(), tenantID, country, Platform(strings.ToUpper(request.URL.Query().Get("platform"))),
		request.URL.Query().Get("app_version"), request.URL.Query().Get("deployment_id"), request.URL.Query().Get("locale"),
	)
	if err != nil {
		status, code, message := http.StatusUnprocessableEntity, "BOOTSTRAP_REQUEST_INVALID", "The bootstrap request is invalid."
		if errors.Is(err, ErrNotFound) {
			status, code, message = http.StatusNotFound, "CONFIGURATION_NOT_FOUND", "Configuration is unavailable for this region."
		}
		writeProblem(writer, request, status, code, message)
		return
	}
	visible, err := handler.visibleAudiencePages(request, tenantID, country, result.Pages)
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "BOOTSTRAP_AUDIENCE_UNAVAILABLE", "Page access could not be checked. Try again.")
		return
	}
	result.Pages = visible
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"config-`+strconv.FormatInt(result.Revision, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

// Roles are supplied only by the gateway's live session authority. Query/body
// fields never participate. Missing/guest/unknown roles can see PUBLIC pages.
func pageVisible(page Page, trustedRoles string) bool {
	roles := map[string]bool{}
	for _, role := range strings.Split(trustedRoles, ",") {
		if role == "CUSTOMER" || role == "VENDOR" || role == "RIDER" {
			roles[role] = true
		}
	}
	for _, audience := range page.Audience {
		if audience == "PUBLIC" || roles[audience] {
			return true
		}
	}
	return false
}

func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = "unavailable"
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "correlation_id": correlationID, "retryable": status >= 500,
		"field_errors": []any{}, "details": map[string]any{},
	}})
}
