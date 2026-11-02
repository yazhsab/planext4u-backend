package adminshell

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
)

type ParticipantReviewAccess struct {
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ParticipantEvidenceAccess interface {
	ParticipantReviewDocumentAccess(context.Context, fulfillment.Actor, string, string, string) (ParticipantReviewAccess, error)
}

func validEvidenceKind(kind string) bool {
	if len(kind) < 2 || len(kind) > 64 || kind[0] < 'A' || kind[0] > 'Z' {
		return false
	}
	for i := 1; i < len(kind); i++ {
		if kind[i] != '_' && (kind[i] < 'A' || kind[i] > 'Z') && (kind[i] < '0' || kind[i] > '9') {
			return false
		}
	}
	return true
}

func validReviewGrant(value ParticipantReviewAccess) bool {
	if !validEvidenceKind(value.Kind) || value.URL == "" || len(value.URL) > 8192 || value.ExpiresAt.IsZero() {
		return false
	}
	now := time.Now().UTC()
	if !value.ExpiresAt.After(now) || value.ExpiresAt.After(now.Add(2*time.Minute+5*time.Second)) {
		return false
	}
	parsed, err := url.Parse(value.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	return parsed.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

func (reader *HTTPFulfillmentReader) ParticipantReviewDocumentAccess(ctx context.Context, actor fulfillment.Actor, kind, id, documentKind string) (ParticipantReviewAccess, error) {
	if !countryParticipantActor(actor) || participantStatuses[kind] == nil || !participantreview.UUID(id) || id == actor.Subject || !validEvidenceKind(documentKind) {
		return ParticipantReviewAccess{}, ErrForbidden
	}
	path := "/v1/vendor/applications/" + id + "/review/documents/" + documentKind + "/access"
	if kind == "RIDER" {
		path = "/v1/rider/applications/" + id + "/review/documents/" + documentKind + "/access"
	}
	data, _, err := reader.participantRequest(ctx, actor, kind, http.MethodPost, path, "OPS_ADMIN", nil, struct {
		Purpose string `json:"purpose"`
	}{Purpose: "ONBOARDING_REVIEW"}, "", 0)
	if err != nil {
		return ParticipantReviewAccess{}, err
	}
	var value ParticipantReviewAccess
	if json.Unmarshal(data, &value) != nil || value.Kind != documentKind || !validReviewGrant(value) {
		return ParticipantReviewAccess{}, errors.New("invalid private review grant")
	}
	return value, nil
}

func (handler *Handler) participantReviewDocumentAccess(w http.ResponseWriter, r *http.Request) {
	actor, ok := handler.countryParticipantSession(w, r, true)
	if !ok {
		return
	}
	kind, id, documentKind := r.PathValue("kind"), r.PathValue("participant_id"), r.PathValue("document_kind")
	if r.URL.RawQuery != "" || r.ContentLength > 0 || participantStatuses[kind] == nil || !participantreview.UUID(id) || id == actor.Subject || !validEvidenceKind(documentKind) {
		handler.fulfillmentError(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.fulfillmentReader.(ParticipantEvidenceAccess)
	if !ok {
		handler.fulfillmentError(w, r, errors.New("participant evidence review unconfigured"))
		return
	}
	value, err := reader.ParticipantReviewDocumentAccess(r.Context(), actor, kind, id, documentKind)
	if err != nil {
		handler.fulfillmentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
