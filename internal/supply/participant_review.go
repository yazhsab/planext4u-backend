package supply

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"net/http"
)

var participantStatuses = []string{"REGISTERED", "DOCUMENTS_SUBMITTED", "OCR_REVIEW", "KYC_REVIEW", "FIELD_VISIT_REQUIRED", "FIELD_VISIT_SCHEDULED", "FIELD_VISIT_PASSED", "BANK_REVIEW", "APPROVED", "REJECTED"}

func reviewerScope(a Actor) participantreview.Scope {
	return participantreview.Scope{TenantID: a.TenantID, Country: a.Country, Subject: a.Subject, SourceSession: a.OperatorSessionID, Revision: a.OperatorRevision, MFA: a.MFAVerified}
}
func lockApplicationReviewer(ctx context.Context, tx pgx.Tx, a Actor, owner string) error {
	if owner == a.Subject {
		return ErrForbidden
	}
	if err := participantreview.Lock(ctx, tx, reviewerScope(a)); err != nil {
		if errors.Is(err, participantreview.ErrForbidden) {
			return ErrForbidden
		}
		return err
	}
	return nil
}
func lockScopedApplicationReviewer(ctx context.Context, tx pgx.Tx, a Actor, owner string) error {
	if owner == a.Subject {
		return ErrForbidden
	}
	capability := "resource.command"
	if owner == "" {
		capability = "resource.read"
	}
	err := participantreview.LockAccess(ctx, tx, reviewerScope(a), "VENDOR", owner, capability)
	if errors.Is(err, participantreview.ErrForbidden) {
		return ErrForbidden
	}
	return err
}
func lockScopedApplicationRead(ctx context.Context, tx pgx.Tx, a Actor, owner string) error {
	if owner == a.Subject || owner == "" {
		return ErrForbidden
	}
	err := participantreview.LockAccess(ctx, tx, reviewerScope(a), "VENDOR", owner, "resource.read")
	if errors.Is(err, participantreview.ErrForbidden) {
		return ErrForbidden
	}
	return err
}
func lockFieldApplicationReviewer(ctx context.Context, tx pgx.Tx, a Actor, owner string) error {
	if !a.MFAVerified || !participantreview.UUID(a.TenantID) || !participantreview.UUID(a.Subject) || !participantreview.UUID(a.OperatorSessionID) || !participantreview.UUID(owner) || len(a.Country) != 2 || a.OperatorRevision < 1 || owner == a.Subject {
		return ErrForbidden
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_participant_field_access($1,$2,$3,$4,$5,$6)`, a.TenantID, a.Country, a.Subject, a.OperatorSessionID, a.OperatorRevision, owner).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
func (service *PostgresService) Applications(ctx context.Context, a Actor, f participantreview.Filter) (participantreview.Page, error) {
	if !postgresSupplyActor(a) || !hasAnyRole(a, "OPS_ADMIN", "SUPER_ADMIN", "REGIONAL_ADMIN") || !a.MFAVerified {
		return participantreview.Page{}, ErrForbidden
	}
	if err := f.Validate(participantStatuses); err != nil {
		return participantreview.Page{}, ErrInvalidRequest
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return participantreview.Page{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockScopedApplicationReviewer(ctx, tx, a, ""); err != nil {
		return participantreview.Page{}, err
	}
	var after any
	if f.After != "" {
		after = f.After
	}
	privateProjection := "(SELECT count(*) FROM supply.vendor_documents d WHERE d.application_id=a.id),coalesce(a.bank_status,'')"
	if len(a.Roles) == 1 && a.Roles[0] == "REGIONAL_ADMIN" {
		// Regional reviewers have no document or banking read permission.
		privateProjection = "0::bigint,''::text"
	}
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.vendor_identity_id::text,a.revision,a.status,a.business_name,a.business_type,`+privateProjection+`,a.updated_at,admin.participant_access_allowed($1,$2,$6,$7,'VENDOR',a.vendor_identity_id,'resource.command'),admin.participant_field_assignment_allowed($1,$2,$6,a.vendor_identity_id) FROM supply.vendor_applications a WHERE a.tenant_id=$1 AND a.country=$2 AND ($3='' OR a.status=$3) AND ($4='' OR strpos(lower(a.business_name),lower($4))>0) AND ($5::uuid IS NULL OR a.id>$5::uuid) AND admin.participant_access_allowed($1,$2,$6,$7,'VENDOR',a.vendor_identity_id,'resource.read') ORDER BY a.id LIMIT 51`, a.TenantID, a.Country, f.Status, f.Search, after, a.Subject, a.OperatorSessionID)
	if err != nil {
		return participantreview.Page{}, err
	}
	items := make([]participantreview.Summary, 0, 51)
	for rows.Next() {
		var v participantreview.Summary
		var canCommand bool
		var canFieldCheckIn bool
		v.Kind = "VENDOR"
		v.Actions = []string{}
		if err = rows.Scan(&v.ID, &v.ParticipantID, &v.Revision, &v.Status, &v.DisplayName, &v.Category, &v.DocumentCount, &v.BankStatus, &v.UpdatedAt, &canCommand, &canFieldCheckIn); err != nil {
			rows.Close()
			return participantreview.Page{}, err
		}
		if canCommand && v.ParticipantID != a.Subject && (v.Status == "OCR_REVIEW" || v.Status == "KYC_REVIEW" || v.Status == "BANK_REVIEW") {
			v.Actions = append(v.Actions, "REJECT")
		}
		if canFieldCheckIn && v.ParticipantID != a.Subject && v.Status == "FIELD_VISIT_SCHEDULED" {
			v.Actions = append(v.Actions, "FIELD_CHECK_IN")
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return participantreview.Page{}, err
	}
	if err = lockScopedApplicationReviewer(ctx, tx, a, ""); err != nil {
		return participantreview.Page{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return participantreview.Page{}, err
	}
	return participantreview.Bounded(items), nil
}
func (handler *Handler) participantApplications(w http.ResponseWriter, r *http.Request) {
	a, ok := supplyActor(w, r)
	if !ok {
		return
	}
	f, err := participantreview.Parse(r.URL.RawQuery, participantStatuses)
	if err != nil {
		writeSupplyProblem(w, r, 422, "SUPPLY_REQUEST_INVALID", "Choose a valid participant filter.")
		return
	}
	reader, ok := handler.service.(interface {
		Applications(context.Context, Actor, participantreview.Filter) (participantreview.Page, error)
	})
	if !ok {
		writeSupplyProblem(w, r, 503, "SUPPLY_UNAVAILABLE", "Participant directory is unavailable.")
		return
	}
	page, err := reader.Applications(r.Context(), a, f)
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	if len(a.Roles) == 1 && a.Roles[0] == "REGIONAL_ADMIN" {
		writeSupplyJSON(w, 200, participantreview.MinimalRegionalPage(page))
		return
	}
	writeSupplyJSON(w, 200, page)
}
