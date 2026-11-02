package fulfillment

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"net/http"
)

var participantStatuses = []string{"REGISTERED", "KYC_REVIEW", "APPROVED", "SUSPENDED"}

func reviewerScope(a Actor) participantreview.Scope {
	return participantreview.Scope{TenantID: a.TenantID, Country: a.Country, Subject: a.Subject, SourceSession: a.OperatorSessionID, Revision: a.OperatorRevision, MFA: a.MFAVerified}
}
func lockRiderReviewer(ctx context.Context, tx pgx.Tx, a Actor, owner string) error {
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
func lockScopedRiderReviewer(ctx context.Context, tx pgx.Tx, a Actor, owner string) error {
	if owner == a.Subject {
		return ErrForbidden
	}
	capability := "resource.command"
	if owner == "" {
		capability = "resource.read"
	}
	err := participantreview.LockAccess(ctx, tx, reviewerScope(a), "RIDER", owner, capability)
	if errors.Is(err, participantreview.ErrForbidden) {
		return ErrForbidden
	}
	return err
}
func (service *PostgresService) RiderApplications(ctx context.Context, a Actor, f participantreview.Filter) (participantreview.Page, error) {
	if !postgresFulfillmentActor(a) || !hasAnyRole(a, "OPS_ADMIN", "SUPER_ADMIN", "REGIONAL_ADMIN") || !a.MFAVerified {
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
	if err = lockScopedRiderReviewer(ctx, tx, a, ""); err != nil {
		return participantreview.Page{}, err
	}
	var after any
	if f.After != "" {
		after = f.After
	}
	privateProjection := "jsonb_array_length(a.documents),coalesce(a.bank_status,'')"
	if len(a.Roles) == 1 && a.Roles[0] == "REGIONAL_ADMIN" {
		// Do not read private document or bank state into a regional queue.
		privateProjection = "0::int,''::text"
	}
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.rider_identity_id::text,a.revision,a.status,a.full_name,a.vehicle_type,`+privateProjection+`,a.updated_at,admin.participant_access_allowed($1,$2,$6,$7,'RIDER',a.rider_identity_id,'resource.command') FROM fulfillment.rider_profiles a WHERE a.tenant_id=$1 AND a.country=$2 AND ($3='' OR a.status=$3) AND ($4='' OR strpos(lower(a.full_name),lower($4))>0) AND ($5::uuid IS NULL OR a.id>$5::uuid) AND admin.participant_access_allowed($1,$2,$6,$7,'RIDER',a.rider_identity_id,'resource.read') ORDER BY a.id LIMIT 51`, a.TenantID, a.Country, f.Status, f.Search, after, a.Subject, a.OperatorSessionID)
	if err != nil {
		return participantreview.Page{}, err
	}
	items := make([]participantreview.Summary, 0, 51)
	for rows.Next() {
		var v participantreview.Summary
		var canCommand bool
		v.Kind = "RIDER"
		v.Actions = []string{}
		if err = rows.Scan(&v.ID, &v.ParticipantID, &v.Revision, &v.Status, &v.DisplayName, &v.Category, &v.DocumentCount, &v.BankStatus, &v.UpdatedAt, &canCommand); err != nil {
			rows.Close()
			return participantreview.Page{}, err
		}
		if canCommand && v.ParticipantID != a.Subject && (v.Status == "KYC_REVIEW") {
			v.Actions = append(v.Actions, "REJECT")
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return participantreview.Page{}, err
	}
	if err = lockScopedRiderReviewer(ctx, tx, a, ""); err != nil {
		return participantreview.Page{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return participantreview.Page{}, err
	}
	return participantreview.Bounded(items), nil
}
func (handler *Handler) participantApplications(w http.ResponseWriter, r *http.Request) {
	a, ok := fulfillmentActorFromRequest(w, r)
	if !ok {
		return
	}
	f, err := participantreview.Parse(r.URL.RawQuery, participantStatuses)
	if err != nil {
		writeFulfillmentProblem(w, r, 422, "FULFILLMENT_REQUEST_INVALID", "Choose a valid participant filter.")
		return
	}
	reader, ok := handler.service.(interface {
		RiderApplications(context.Context, Actor, participantreview.Filter) (participantreview.Page, error)
	})
	if !ok {
		writeFulfillmentProblem(w, r, 503, "FULFILLMENT_UNAVAILABLE", "Participant directory is unavailable.")
		return
	}
	page, err := reader.RiderApplications(r.Context(), a, f)
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	if len(a.Roles) == 1 && a.Roles[0] == "REGIONAL_ADMIN" {
		writeFulfillmentJSON(w, 200, participantreview.MinimalRegionalPage(page))
		return
	}
	writeFulfillmentJSON(w, 200, page)
}
