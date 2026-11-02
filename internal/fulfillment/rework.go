package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"net/http"
	"strings"
)

func lockRiderSession(ctx context.Context, tx pgx.Tx, a Actor) error {
	if !hasRole(a, "RIDER") {
		return ErrForbidden
	}
	err := participantreview.LockParticipant(ctx, tx, a.TenantID, a.Country, a.Subject, a.SourceSessionID, "RIDER")
	if errors.Is(err, participantreview.ErrForbidden) {
		return ErrForbidden
	}
	return err
}

func (service *PostgresService) ResubmitRider(actor Actor, key string, revision int64, request RiderRegistrationRequest) (RiderProfile, bool, error) {
	if !hasRole(actor, "RIDER") {
		return RiderProfile{}, false, ErrForbidden
	}
	if !postgresFulfillmentActor(actor) || !validKey(key) || revision < 1 || !validRiderRegistration(request) || !allFulfillmentUUIDDocuments(request.Documents) {
		return RiderProfile{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RiderProfile{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockRiderSession(ctx, tx, actor); err != nil {
		return RiderProfile{}, false, err
	}
	const operation = "rider-resubmit"
	if err = fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return RiderProfile{}, false, err
	}
	fingerprint := digest(struct {
		Revision int64
		Input    RiderRegistrationRequest
	}{revision, request})
	var replay RiderProfile
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return RiderProfile{}, false, err
	} else if found {
		if err = lockRiderSession(ctx, tx, actor); err != nil {
			return RiderProfile{}, false, err
		}
		return replay, true, nil
	}
	value, err := loadPostgresRider(ctx, tx, actor.TenantID, actor.Country, actor.Subject, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderProfile{}, false, ErrNotFound
	}
	if err != nil {
		return RiderProfile{}, false, err
	}
	if value.Revision != revision {
		return RiderProfile{}, false, ErrConflict
	}
	if value.Status != RiderSuspended || !value.ReworkAllowed {
		return RiderProfile{}, false, ErrInvalidTransition
	}
	ids := make([]string, len(request.Documents))
	for i, d := range request.Documents {
		ids[i] = d.AssetID
	}
	if err = service.evidence.VerifyIdentityDocuments(ctx, privateevidence.Scope{TenantID: actor.TenantID, Country: actor.Country, OwnerID: actor.Subject}, ids); err != nil {
		return RiderProfile{}, false, err
	}
	previous, err := json.Marshal(value.Documents)
	if err != nil {
		return RiderProfile{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO fulfillment.rider_submissions(profile_id,revision,documents,submitted_by,submitted_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, value.ID, value.Revision, previous, value.RiderID, value.UpdatedAt); err != nil {
		return RiderProfile{}, false, err
	}
	value.Documents = cloneRiderDocuments(request.Documents)
	for i := range value.Documents {
		value.Documents[i].OCRStatus = "REVIEW_REQUIRED"
	}
	value.Revision++
	value.Status = RiderKYCReview
	value.ReworkAllowed = false
	value.ReviewReason = ""
	value.ReviewedAt = nil
	value.FullName = strings.TrimSpace(request.FullName)
	value.PhoneMasked = request.PhoneMasked
	value.VehicleType = request.VehicleType
	value.VehicleNumber = strings.ToUpper(request.VehicleNumber)
	value.BankReference = request.BankReference
	value.BankStatus = "PENDING_VERIFICATION"
	value.Zones = append([]string{}, request.Zones...)
	value.AllowedActions = []string{"VIEW_REVIEW_STATUS"}
	value.UpdatedAt = service.Now()
	documents, err := json.Marshal(value.Documents)
	if err != nil {
		return RiderProfile{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE fulfillment.rider_profiles SET revision=$2,status=$3,full_name=$4,phone_masked=$5,vehicle_type=$6,vehicle_number=$7,bank_reference=$8,bank_status=$9,zones=$10,documents=$11,rework_allowed=false,review_reason='',reviewed_at=NULL,updated_at=$12 WHERE id=$1`, value.ID, value.Revision, value.Status, value.FullName, value.PhoneMasked, value.VehicleType, value.VehicleNumber, value.BankReference, value.BankStatus, value.Zones, documents, value.UpdatedAt)
	if err != nil {
		return RiderProfile{}, false, err
	}
	if err = privateevidence.Claim(ctx, tx, privateevidence.Scope{TenantID: actor.TenantID, Country: actor.Country, OwnerID: actor.Subject}, "RIDER_APPLICATION", value.ID, ids); err != nil {
		return RiderProfile{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO fulfillment.rider_submissions(profile_id,revision,documents,submitted_by,submitted_at) VALUES($1,$2,$3,$4,$5)`, value.ID, value.Revision, documents, actor.Subject, value.UpdatedAt); err != nil {
		return RiderProfile{}, false, err
	}
	if err = insertFulfillmentAudit(ctx, tx, actor, "RIDER_RESUBMITTED", "RIDER_PROFILE", actor.Subject, "Corrected onboarding evidence submitted for independent review", value.UpdatedAt); err != nil {
		return RiderProfile{}, false, err
	}
	if err = storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, value.UpdatedAt); err != nil {
		return RiderProfile{}, false, err
	}
	if err = lockRiderSession(ctx, tx, actor); err != nil {
		return RiderProfile{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RiderProfile{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (handler *Handler) resubmitRider(w http.ResponseWriter, r *http.Request) {
	a, revision, ok := fulfillmentMutation(w, r)
	if !ok {
		return
	}
	var input RiderRegistrationRequest
	if !decodeFulfillmentJSON(w, r, &input) {
		return
	}
	service, ok := handler.service.(interface {
		ResubmitRider(Actor, string, int64, RiderRegistrationRequest) (RiderProfile, bool, error)
	})
	if !ok {
		writeFulfillmentProblem(w, r, 503, "FULFILLMENT_UNAVAILABLE", "Application rework is unavailable.")
		return
	}
	value, replay, err := service.ResubmitRider(a, r.Header.Get("Idempotency-Key"), revision, input)
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	writeRider(w, 200, value, replay)
}
