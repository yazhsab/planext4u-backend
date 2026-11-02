package fulfillment

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type RiderReviewDocument struct {
	Kind      string `json:"kind"`
	OCRStatus string `json:"ocr_status"`
}

type RiderReviewDetail struct {
	ID                  string                `json:"id"`
	RiderID             string                `json:"rider_id"`
	Revision            int64                 `json:"revision"`
	Status              RiderStatus           `json:"status"`
	FullName            string                `json:"full_name"`
	PhoneMasked         string                `json:"phone_masked"`
	VehicleType         string                `json:"vehicle_type"`
	VehicleNumberMasked string                `json:"vehicle_number_masked"`
	Documents           []RiderReviewDocument `json:"documents"`
	BankStatus          string                `json:"bank_status"`
	Zones               []string              `json:"zones"`
	ReviewReason        string                `json:"review_reason,omitempty"`
	ReviewedAt          *time.Time            `json:"reviewed_at,omitempty"`
	ReviewActions       []string              `json:"review_actions"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

func safeRiderReview(value RiderProfile) RiderReviewDetail {
	masked := ""
	if value.VehicleNumber != "" {
		visible := 4
		if len(value.VehicleNumber) <= visible {
			visible = len(value.VehicleNumber) - 1
		}
		masked = "••••" + value.VehicleNumber[len(value.VehicleNumber)-visible:]
	}
	result := RiderReviewDetail{ID: value.ID, RiderID: value.RiderID, Revision: value.Revision, Status: value.Status, FullName: value.FullName, PhoneMasked: value.PhoneMasked, VehicleType: value.VehicleType, VehicleNumberMasked: masked, Documents: make([]RiderReviewDocument, 0, len(value.Documents)), BankStatus: value.BankStatus, Zones: append([]string{}, value.Zones...), ReviewReason: value.ReviewReason, ReviewedAt: value.ReviewedAt, ReviewActions: []string{}, UpdatedAt: value.UpdatedAt}
	for _, document := range value.Documents {
		result.Documents = append(result.Documents, RiderReviewDocument{Kind: document.Kind, OCRStatus: document.OCRStatus})
	}
	if value.Status == RiderKYCReview {
		result.ReviewActions = []string{"REJECT"}
		if validRiderRegistration(RiderRegistrationRequest{FullName: value.FullName, PhoneMasked: value.PhoneMasked, VehicleType: value.VehicleType, VehicleNumber: value.VehicleNumber, Documents: value.Documents, BankReference: value.BankReference, Zones: value.Zones}) && allFulfillmentUUIDDocuments(value.Documents) {
			result.ReviewActions = append([]string{"APPROVE"}, result.ReviewActions...)
		}
	}
	return result
}

func (service *PostgresService) RiderReview(ctx context.Context, actor Actor, riderID string) (RiderReviewDetail, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(riderID) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		return RiderReviewDetail{}, ErrForbidden
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RiderReviewDetail{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockRiderReviewer(ctx, tx, actor, riderID); err != nil {
		return RiderReviewDetail{}, err
	}
	value, err := loadPostgresRider(ctx, tx, actor.TenantID, actor.Country, riderID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderReviewDetail{}, ErrNotFound
	}
	if err != nil {
		return RiderReviewDetail{}, err
	}
	result := safeRiderReview(value)
	if err = lockRiderReviewer(ctx, tx, actor, riderID); err != nil {
		return RiderReviewDetail{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RiderReviewDetail{}, err
	}
	return result, nil
}

func (handler *Handler) riderReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := fulfillmentActorFromRequest(w, r)
	if !ok {
		return
	}
	reader, ok := handler.service.(interface {
		RiderReview(context.Context, Actor, string) (RiderReviewDetail, error)
	})
	if !ok {
		writeFulfillmentProblem(w, r, 503, "FULFILLMENT_UNAVAILABLE", "Application review is unavailable.")
		return
	}
	value, err := reader.RiderReview(r.Context(), actor, r.PathValue("rider_id"))
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	writeFulfillmentJSON(w, http.StatusOK, value)
}
