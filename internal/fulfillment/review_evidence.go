package fulfillment

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
)

type RiderReviewAccess struct {
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RiderReviewDocumentAccessRequest struct {
	Purpose string `json:"purpose"`
}

func safeRiderReviewKind(kind string) bool {
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

func (service *PostgresService) RiderReviewDocumentAccess(ctx context.Context, actor Actor, riderID, kind string) (RiderReviewAccess, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(riderID) || !safeRiderReviewKind(kind) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		return RiderReviewAccess{}, ErrForbidden
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RiderReviewAccess{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockRiderReviewer(ctx, tx, actor, riderID); err != nil {
		return RiderReviewAccess{}, err
	}
	if err = lockScopedRiderReviewer(ctx, tx, actor, riderID); err != nil {
		return RiderReviewAccess{}, err
	}
	value, err := loadPostgresRider(ctx, tx, actor.TenantID, actor.Country, riderID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderReviewAccess{}, ErrNotFound
	}
	if err != nil {
		return RiderReviewAccess{}, err
	}
	var assetID string
	count := 0
	for _, document := range value.Documents {
		if document.Kind == kind {
			assetID = document.AssetID
			count++
		}
	}
	accessErr := error(nil)
	grant := privateevidence.AccessGrant{}
	if value.Status != RiderKYCReview || count > 1 {
		accessErr = ErrConflict
	} else if count == 0 {
		accessErr = ErrNotFound
	} else if provider, ok := service.evidence.(privateevidence.AccessProvider); !ok {
		accessErr = privateevidence.ErrUnavailable
	} else {
		grant, accessErr = provider.AccessIdentityDocument(ctx,
			privateevidence.Scope{TenantID: actor.TenantID, Country: actor.Country, OwnerID: riderID}, assetID)
		if accessErr != nil && !errors.Is(accessErr, privateevidence.ErrRejected) {
			accessErr = privateevidence.ErrUnavailable
		}
	}
	actorAction := "RIDER_EVIDENCE_ACCESS_GRANTED"
	if accessErr != nil {
		actorAction = "RIDER_EVIDENCE_ACCESS_DENIED"
	}
	if err = insertFulfillmentAudit(ctx, tx, actor, actorAction, "RIDER_PROFILE", riderID, "Private onboarding document "+kind, service.Now()); err != nil {
		return RiderReviewAccess{}, err
	}
	if err = lockRiderReviewer(ctx, tx, actor, riderID); err != nil {
		return RiderReviewAccess{}, err
	}
	if err = lockScopedRiderReviewer(ctx, tx, actor, riderID); err != nil {
		return RiderReviewAccess{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RiderReviewAccess{}, err
	}
	if accessErr != nil {
		return RiderReviewAccess{}, accessErr
	}
	return RiderReviewAccess{Kind: kind, URL: grant.URL, ExpiresAt: grant.ExpiresAt}, nil
}

func (handler *Handler) riderReviewDocumentAccess(w http.ResponseWriter, r *http.Request) {
	actor, ok := fulfillmentActorFromRequest(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		handler.problem(w, r, ErrInvalidRequest)
		return
	}
	var command RiderReviewDocumentAccessRequest
	if !decodeFulfillmentJSON(w, r, &command) {
		return
	}
	if command.Purpose != "ONBOARDING_REVIEW" {
		handler.problem(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.service.(interface {
		RiderReviewDocumentAccess(context.Context, Actor, string, string) (RiderReviewAccess, error)
	})
	if !ok {
		writeFulfillmentProblem(w, r, http.StatusServiceUnavailable, "FULFILLMENT_UNAVAILABLE", "Document review is unavailable.")
		return
	}
	value, err := reader.RiderReviewDocumentAccess(r.Context(), actor, r.PathValue("rider_id"), r.PathValue("kind"))
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	writeFulfillmentJSON(w, http.StatusOK, value)
}
