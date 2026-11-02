package adminshell

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yazhsab/planext4u-backend/internal/adminops"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

func (handler *Handler) privacyGovernor(w http.ResponseWriter, r *http.Request, write bool) (identity.DeletionGovernor, bool) {
	session, _, ok := handler.authorize(w, r, adminops.CapabilityPolicy)
	if !ok {
		return identity.DeletionGovernor{}, false
	}
	assurance := assuranceFor(session.Principal, handler.clock())
	if (!containsRole(session.Principal.Roles, RoleCountryAdmin) && !containsRole(session.Principal.Roles, RoleSuperAdmin)) ||
		!assurance.FreshAuth || !assurance.MFASatisfied || (write && !handler.csrfAllowed(r, session.CSRFToken)) ||
		r.URL.RawQuery != "" || session.Principal.OperatorRevision < 1 {
		writeProblem(w, r, http.StatusForbidden, "ADMIN_PRIVACY_GOVERNANCE_FORBIDDEN", "Fresh country administrator authority is required.")
		return identity.DeletionGovernor{}, false
	}
	return identity.DeletionGovernor{
		TenantID: session.Principal.TenantID, Country: session.Principal.SelectedCountry,
		SubjectID: session.Principal.SubjectID, OperatorSessionID: session.Principal.SessionID,
		OperatorRevision: session.Principal.OperatorRevision,
	}, true
}

func readPrivacyCommand(w http.ResponseWriter, r *http.Request, input any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(w, r, http.StatusUnprocessableEntity, "ADMIN_PRIVACY_COMMAND_INVALID", "Provide a valid privacy governance command.")
		return false
	}
	return true
}

func emptyPrivacyCommand(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	defer r.Body.Close()
	value, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(value))) != 0 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "ADMIN_PRIVACY_COMMAND_INVALID", "This command has no request body.")
		return false
	}
	return true
}

func (handler *Handler) privacyGovernanceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, identity.ErrInvalidInput) {
		writeProblem(w, r, http.StatusUnprocessableEntity, "ADMIN_PRIVACY_COMMAND_INVALID", "The privacy governance command is invalid.")
		return
	}
	if errors.Is(err, identity.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "ADMIN_PRIVACY_GOVERNANCE_NOT_FOUND", "No privacy record is available in the current scope.")
		return
	}
	var database *pgconn.PgError
	if errors.As(err, &database) && (database.Code == "P0001" || database.Code == "23505" || database.Code == "23514") {
		writeProblem(w, r, http.StatusConflict, "ADMIN_PRIVACY_GOVERNANCE_CONFLICT", "The privacy decision is unavailable for the current scope or revision.")
		return
	}
	writeProblem(w, r, http.StatusServiceUnavailable, "ADMIN_PRIVACY_GOVERNANCE_UNAVAILABLE", "The privacy decision could not be completed. Retry later.")
}

func (handler *Handler) proposeDeletionPolicy(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ID               string `json:"id"`
		Owner            string `json:"owner"`
		PolicyVersion    string `json:"policy_version"`
		RetentionSeconds int64  `json:"retention_seconds"`
		ExpectedRevision int64  `json:"expected_revision"`
		ReasonReference  string `json:"reason_reference"`
	}
	if !readPrivacyCommand(w, r, &input) {
		return
	}
	if uuid.Validate(input.ID) != nil || input.RetentionSeconds < 0 || input.RetentionSeconds > math.MaxInt64/int64(time.Second) {
		writeProblem(w, r, http.StatusUnprocessableEntity, "ADMIN_PRIVACY_COMMAND_INVALID", "The retention proposal is invalid.")
		return
	}
	id, err := handler.privacyGovernance.ProposeDeletionPolicy(r.Context(), actor, identity.DeletionPolicyProposal{
		ID: input.ID, Owner: input.Owner, Version: input.PolicyVersion,
		Retention:        time.Duration(input.RetentionSeconds) * time.Second,
		ExpectedRevision: input.ExpectedRevision, ReasonReference: input.ReasonReference,
	})
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "PROPOSED"})
}

func (handler *Handler) publishDeletionPolicy(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, true)
	if !ok {
		return
	}
	if !emptyPrivacyCommand(w, r) {
		return
	}
	id := r.PathValue("proposal_id")
	if uuid.Validate(id) != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, "ADMIN_PRIVACY_COMMAND_INVALID", "The proposal identifier is invalid.")
		return
	}
	revision, err := handler.privacyGovernance.PublishDeletionPolicy(r.Context(), actor, id)
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "PUBLISHED", "revision": revision})
}

func (handler *Handler) placeDeletionHold(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Owner           string `json:"owner"`
		ReasonReference string `json:"reason_reference"`
	}
	if !readPrivacyCommand(w, r, &input) {
		return
	}
	holdID, err := handler.privacyGovernance.PlaceDeletionHold(r.Context(), actor, r.PathValue("request_id"), input.Owner, input.ReasonReference)
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": holdID, "status": "ACTIVE", "revision": 1})
}

func (handler *Handler) releaseDeletionHold(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64  `json:"expected_revision"`
		ReasonReference  string `json:"reason_reference"`
	}
	if !readPrivacyCommand(w, r, &input) {
		return
	}
	holdID, err := strconv.ParseInt(r.PathValue("hold_id"), 10, 64)
	if err != nil || holdID < 1 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "ADMIN_PRIVACY_COMMAND_INVALID", "The hold identifier is invalid.")
		return
	}
	revision, err := handler.privacyGovernance.ReleaseDeletionHold(r.Context(), actor, holdID, input.ExpectedRevision, input.ReasonReference)
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": holdID, "status": "RELEASED", "revision": revision})
}

func (handler *Handler) listDeletionPolicyProposals(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, false)
	if !ok {
		return
	}
	items, err := handler.privacyGovernance.ListDeletionPolicyProposals(r.Context(), actor)
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) getDeletionPolicyProposal(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, false)
	if !ok {
		return
	}
	value, err := handler.privacyGovernance.GetDeletionPolicyProposal(r.Context(), actor, r.PathValue("proposal_id"))
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (handler *Handler) listDeletionRequests(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, false)
	if !ok {
		return
	}
	items, err := handler.privacyGovernance.ListDeletionRequests(r.Context(), actor)
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) getDeletionRequest(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, false)
	if !ok {
		return
	}
	value, err := handler.privacyGovernance.GetDeletionRequest(r.Context(), actor, r.PathValue("request_id"))
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (handler *Handler) listDeletionHolds(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.privacyGovernor(w, r, false)
	if !ok {
		return
	}
	items, err := handler.privacyGovernance.ListDeletionHolds(r.Context(), actor)
	if err != nil {
		handler.privacyGovernanceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
