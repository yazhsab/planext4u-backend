package verticalslice

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type syntheticConsentEvidence struct {
	EvidenceID    string    `json:"evidence_id"`
	Purpose       string    `json:"purpose"`
	Granted       bool      `json:"granted"`
	PolicyVersion string    `json:"policy_version"`
	RecordedAt    time.Time `json:"recorded_at"`
	Version       int64     `json:"version"`
}

type syntheticConsentHandler struct {
	mu       sync.RWMutex
	clock    func() time.Time
	evidence map[string]map[string]syntheticConsentEvidence
}

func newSyntheticConsentHandler(clock func() time.Time) *syntheticConsentHandler {
	return &syntheticConsentHandler{
		clock:    clock,
		evidence: make(map[string]map[string]syntheticConsentEvidence),
	}
}

func (handler *syntheticConsentHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	subject := request.Header.Get("X-Planext4u-Subject")
	if !safeID(subject) {
		writeAuthProblem(writer, request, http.StatusForbidden, "ACCESS_DENIED", "This account cannot access the requested resource.")
		return
	}
	if request.URL.Path == "/v1/me/consents" {
		if request.Method != http.MethodGet {
			writeAuthProblem(writer, request, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The requested method is not allowed.")
			return
		}
		handler.list(writer, subject)
		return
	}
	if request.Method != http.MethodPut || !strings.HasPrefix(request.URL.Path, "/v1/me/consents/") {
		writeAuthProblem(writer, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The requested resource was not found.")
		return
	}
	purpose := strings.TrimPrefix(request.URL.Path, "/v1/me/consents/")
	if !supportedSyntheticConsentPurpose(purpose) {
		writeAuthProblem(writer, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Check the submitted values.")
		return
	}
	defer request.Body.Close()
	var input struct {
		Granted       *bool  `json:"granted"`
		PolicyVersion string `json:"policy_version"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		input.Granted == nil || !safeID(input.PolicyVersion) {
		writeAuthProblem(writer, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Check the submitted values.")
		return
	}
	evidenceID, err := secureID("consent")
	if err != nil {
		writeAuthProblem(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "The request could not be completed.")
		return
	}
	handler.mu.Lock()
	byPurpose := handler.evidence[subject]
	if byPurpose == nil {
		byPurpose = make(map[string]syntheticConsentEvidence)
		handler.evidence[subject] = byPurpose
	}
	version := byPurpose[purpose].Version + 1
	value := syntheticConsentEvidence{
		EvidenceID: evidenceID, Purpose: purpose, Granted: *input.Granted,
		PolicyVersion: input.PolicyVersion, RecordedAt: handler.clock().UTC(), Version: version,
	}
	byPurpose[purpose] = value
	handler.mu.Unlock()
	writeAuthJSON(writer, http.StatusOK, value)
}

func (handler *syntheticConsentHandler) list(writer http.ResponseWriter, subject string) {
	handler.mu.RLock()
	values := make([]syntheticConsentEvidence, 0, len(handler.evidence[subject]))
	for _, value := range handler.evidence[subject] {
		values = append(values, value)
	}
	handler.mu.RUnlock()
	sort.Slice(values, func(left, right int) bool { return values[left].Purpose < values[right].Purpose })
	writeAuthJSON(writer, http.StatusOK, map[string]any{"consents": values})
}

func supportedSyntheticConsentPurpose(value string) bool {
	switch value {
	case "ANALYTICS", "MARKETING", "LOCATION_SERVICEABILITY", "LOCATION_DELIVERY":
		return true
	default:
		return false
	}
}
