package supply

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
)

type VendorReviewAccess struct {
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type VendorReviewDocumentAccessRequest struct {
	Purpose string `json:"purpose"`
}

func vendorReviewable(status ApplicationStatus) bool {
	switch status {
	case StatusDocumentsSubmitted, StatusOCRReview, StatusKYCReview, StatusFieldVisitRequired,
		StatusFieldVisitScheduled, StatusFieldVisitPassed, StatusBankReview:
		return true
	}
	return false
}

func safeReviewKind(kind string) bool {
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

// VendorReviewDocumentAccess resolves the stored document reference only after
// current country/operator and participant scope locks. A private media grant is
// returned only if the decision audit commits under the same locks.
func (service *PostgresService) VendorReviewDocumentAccess(ctx context.Context, actor Actor, vendorID, kind string) (VendorReviewAccess, error) {
	if !postgresSupplyActor(actor) || !postgresSupplyUUID(vendorID) || !safeReviewKind(kind) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		return VendorReviewAccess{}, ErrForbidden
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return VendorReviewAccess{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockApplicationReviewer(ctx, tx, actor, vendorID); err != nil {
		return VendorReviewAccess{}, err
	}
	if err = lockScopedApplicationReviewer(ctx, tx, actor, vendorID); err != nil {
		return VendorReviewAccess{}, err
	}
	value, err := service.loadApplicationByVendor(ctx, tx, actor.TenantID, actor.Country, vendorID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return VendorReviewAccess{}, ErrNotFound
	}
	if err != nil {
		return VendorReviewAccess{}, err
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
	if !vendorReviewable(value.Status) || count > 1 {
		accessErr = ErrConflict
	} else if count == 0 {
		accessErr = ErrNotFound
	} else if provider, ok := service.evidence.(privateevidence.AccessProvider); !ok {
		accessErr = privateevidence.ErrUnavailable
	} else {
		grant, accessErr = provider.AccessIdentityDocument(ctx,
			privateevidence.Scope{TenantID: actor.TenantID, Country: actor.Country, OwnerID: vendorID}, assetID)
		if accessErr != nil && !errors.Is(accessErr, privateevidence.ErrRejected) {
			accessErr = privateevidence.ErrUnavailable
		}
	}
	outcome := "GRANTED"
	if accessErr != nil {
		outcome = "DENIED"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO supply.reviewer_evidence_access
	 (id,tenant_id,country,reviewer_identity_id,vendor_identity_id,document_kind,outcome,occurred_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp())`, uuid.NewString(), actor.TenantID, actor.Country, actor.Subject, vendorID, kind, outcome); err != nil {
		return VendorReviewAccess{}, err
	}
	if err = lockApplicationReviewer(ctx, tx, actor, vendorID); err != nil {
		return VendorReviewAccess{}, err
	}
	if err = lockScopedApplicationReviewer(ctx, tx, actor, vendorID); err != nil {
		return VendorReviewAccess{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return VendorReviewAccess{}, err
	}
	if accessErr != nil {
		return VendorReviewAccess{}, accessErr
	}
	return VendorReviewAccess{Kind: kind, URL: grant.URL, ExpiresAt: grant.ExpiresAt}, nil
}

func (handler *Handler) vendorReviewDocumentAccess(w http.ResponseWriter, r *http.Request) {
	actor, ok := supplyActor(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		handler.problem(w, r, ErrInvalidRequest)
		return
	}
	var command VendorReviewDocumentAccessRequest
	if !decodeSupplyJSON(w, r, &command) {
		return
	}
	if command.Purpose != "ONBOARDING_REVIEW" {
		handler.problem(w, r, ErrInvalidRequest)
		return
	}
	reader, ok := handler.service.(interface {
		VendorReviewDocumentAccess(context.Context, Actor, string, string) (VendorReviewAccess, error)
	})
	if !ok {
		writeSupplyProblem(w, r, http.StatusServiceUnavailable, "SUPPLY_UNAVAILABLE", "Document review is unavailable.")
		return
	}
	value, err := reader.VendorReviewDocumentAccess(r.Context(), actor, r.PathValue("vendor_id"), r.PathValue("kind"))
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	writeSupplyJSON(w, http.StatusOK, value)
}
