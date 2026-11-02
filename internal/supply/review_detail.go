package supply

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type VendorReviewDocument struct {
	Kind         string `json:"kind"`
	OCRStatus    string `json:"ocr_status"`
	ReviewReason string `json:"review_reason,omitempty"`
}

type VendorReviewVisit struct {
	ScheduledAt     time.Time  `json:"scheduled_at"`
	CheckedInAt     *time.Time `json:"checked_in_at,omitempty"`
	CheckInDistance float64    `json:"check_in_distance_m,omitempty"`
}

type VendorReviewDetail struct {
	ID               string                 `json:"id"`
	VendorID         string                 `json:"vendor_id"`
	Revision         int64                  `json:"revision"`
	Status           ApplicationStatus      `json:"status"`
	BusinessName     string                 `json:"business_name"`
	BusinessType     string                 `json:"business_type"`
	ContactName      string                 `json:"contact_name"`
	Documents        []VendorReviewDocument `json:"documents"`
	FieldVisit       *VendorReviewVisit     `json:"field_visit,omitempty"`
	ServiceZoneCount int                    `json:"service_zone_count"`
	BankStatus       string                 `json:"bank_status"`
	BankLast4        string                 `json:"bank_last4,omitempty"`
	LastReviewReason string                 `json:"last_review_reason,omitempty"`
	ReviewActions    []string               `json:"review_actions"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

func safeVendorReview(value Application, actor Actor) VendorReviewDetail {
	result := VendorReviewDetail{ID: value.ID, VendorID: value.VendorID, Revision: value.Revision, Status: value.Status, BusinessName: value.BusinessName, BusinessType: value.BusinessType, ContactName: value.ContactName, Documents: make([]VendorReviewDocument, 0, len(value.Documents)), ServiceZoneCount: len(value.Zones), ReviewActions: []string{}, UpdatedAt: value.UpdatedAt}
	for _, document := range value.Documents {
		result.Documents = append(result.Documents, VendorReviewDocument{Kind: document.Kind, OCRStatus: document.OCRStatus, ReviewReason: document.ReviewReason})
	}
	if value.Visit != nil {
		result.FieldVisit = &VendorReviewVisit{ScheduledAt: value.Visit.ScheduledAt, CheckedInAt: value.Visit.CheckedInAt, CheckInDistance: value.Visit.CheckInDistance}
	}
	if value.Bank != nil {
		result.BankStatus, result.BankLast4 = value.Bank.Status, value.Bank.Last4
	}
	for i := len(value.Timeline) - 1; i >= 0; i-- {
		if value.Timeline[i].Status == StatusRejected {
			result.LastReviewReason = value.Timeline[i].Reason
			break
		}
	}
	switch value.Status {
	case StatusDocumentsSubmitted:
		if hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
			result.ReviewActions = []string{"START_OCR_REVIEW"}
		}
	case StatusOCRReview:
		if hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
			result.ReviewActions = []string{"VERIFY_KYC", "REJECT"}
		}
	case StatusKYCReview:
		if hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
			result.ReviewActions = []string{"REQUIRE_FIELD_VISIT", "REJECT"}
		}
	case StatusBankReview:
		if hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
			result.ReviewActions = append(result.ReviewActions, "REJECT")
		}
		if value.Bank != nil && value.Bank.Status != "VERIFIED" && hasAnyRole(actor, "FINANCE", "SUPER_ADMIN") {
			result.ReviewActions = append(result.ReviewActions, "VERIFY_BANK")
		}
		documentsVerified := validReworkDocuments(value.Documents)
		for _, document := range value.Documents {
			documentsVerified = documentsVerified && document.OCRStatus == "VERIFIED"
		}
		if value.Bank != nil && value.Bank.Status == "VERIFIED" && len(value.Zones) > 0 && value.Visit != nil && value.Visit.CheckedInAt != nil && documentsVerified && hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
			result.ReviewActions = append(result.ReviewActions, "APPROVE")
		}
	}
	return result
}

// Regional reads use a separate SQL projection. In particular, no contact,
// document, bank, visit coordinates or review-audit columns are selected into
// the regional process before the response is encoded.
func (service *PostgresService) regionalVendorReview(ctx context.Context, tx pgx.Tx, actor Actor, vendorID string) (VendorReviewDetail, error) {
	result := VendorReviewDetail{Documents: []VendorReviewDocument{}, ReviewActions: []string{}}
	var status string
	var scheduledAt, checkedInAt *time.Time
	err := tx.QueryRow(ctx, `SELECT a.id::text,a.vendor_identity_id::text,a.revision,a.status,
       a.business_name,a.business_type,a.updated_at,
       (SELECT count(*)::int FROM supply.service_zones z
        WHERE z.tenant_id=a.tenant_id AND z.country=a.country
          AND z.vendor_identity_id=a.vendor_identity_id),
       visit.scheduled_at,visit.checked_in_at
      FROM supply.vendor_applications a
      LEFT JOIN LATERAL (
        SELECT scheduled_at,checked_in_at FROM supply.field_visits
        WHERE application_id=a.id ORDER BY scheduled_at DESC LIMIT 1
      ) visit ON true
      WHERE a.tenant_id=$1 AND a.country=$2 AND a.vendor_identity_id=$3
      FOR SHARE OF a`, actor.TenantID, actor.Country, vendorID).
		Scan(&result.ID, &result.VendorID, &result.Revision, &status,
			&result.BusinessName, &result.BusinessType, &result.UpdatedAt,
			&result.ServiceZoneCount, &scheduledAt, &checkedInAt)
	if err != nil {
		return VendorReviewDetail{}, err
	}
	result.Status = ApplicationStatus(status)
	if scheduledAt != nil {
		result.FieldVisit = &VendorReviewVisit{ScheduledAt: *scheduledAt, CheckedInAt: checkedInAt}
	}
	return result, nil
}

func (service *PostgresService) VendorReview(ctx context.Context, actor Actor, vendorID string) (VendorReviewDetail, error) {
	regional := len(actor.Roles) == 1 && actor.Roles[0] == "REGIONAL_ADMIN"
	if !postgresSupplyActor(actor) || !postgresSupplyUUID(vendorID) || !regional && !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "FINANCE") || !actor.MFAVerified {
		return VendorReviewDetail{}, ErrForbidden
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return VendorReviewDetail{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lock := lockApplicationReviewer
	if regional {
		lock = lockScopedApplicationRead
	}
	if err = lock(ctx, tx, actor, vendorID); err != nil {
		return VendorReviewDetail{}, err
	}
	var result VendorReviewDetail
	if regional {
		result, err = service.regionalVendorReview(ctx, tx, actor, vendorID)
	} else {
		var value Application
		value, err = service.loadApplicationByVendor(ctx, tx, actor.TenantID, actor.Country, vendorID, false)
		if err == nil {
			result = safeVendorReview(value, actor)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return VendorReviewDetail{}, ErrNotFound
	}
	if err != nil {
		return VendorReviewDetail{}, err
	}
	if err = lock(ctx, tx, actor, vendorID); err != nil {
		return VendorReviewDetail{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return VendorReviewDetail{}, err
	}
	return result, nil
}

func (handler *Handler) vendorReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := supplyActor(w, r)
	if !ok {
		return
	}
	reader, ok := handler.service.(interface {
		VendorReview(context.Context, Actor, string) (VendorReviewDetail, error)
	})
	if !ok {
		writeSupplyProblem(w, r, 503, "SUPPLY_UNAVAILABLE", "Application review is unavailable.")
		return
	}
	value, err := reader.VendorReview(r.Context(), actor, r.PathValue("vendor_id"))
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	writeSupplyJSON(w, http.StatusOK, value)
}
