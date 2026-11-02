package supply

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/participantreview"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"net/http"
	"strings"
)

func lockVendorSession(ctx context.Context, tx pgx.Tx, a Actor) error {
	if !hasRole(a, "VENDOR") {
		return ErrForbidden
	}
	err := participantreview.LockParticipant(ctx, tx, a.TenantID, a.Country, a.Subject, a.SourceSessionID, "VENDOR")
	if errors.Is(err, participantreview.ErrForbidden) {
		return ErrForbidden
	}
	return err
}

func validReworkDocuments(documents []Document) bool {
	return validApplicationDocuments(documents, postgresSupplyUUID)
}

func validApplicationDocuments(documents []Document, validAsset func(string) bool) bool {
	if len(documents) < 2 || len(documents) > 10 {
		return false
	}
	kinds, assets := map[string]bool{}, map[string]bool{}
	for _, document := range documents {
		kind := document.Kind
		switch kind {
		case "IDENTITY_PROOF":
			kind = "OWNER_IDENTITY"
		case "BUSINESS_LICENSE":
			kind = "BUSINESS_REGISTRATION"
		case "OWNER_IDENTITY", "BUSINESS_REGISTRATION", "TAX_REGISTRATION", "ADDRESS_PROOF":
		default:
			return false
		}
		if !validAsset(document.AssetID) || kinds[kind] || assets[document.AssetID] {
			return false
		}
		kinds[kind], assets[document.AssetID] = true, true
	}
	return kinds["OWNER_IDENTITY"] && kinds["BUSINESS_REGISTRATION"]
}

func (service *PostgresService) ResubmitDocuments(actor Actor, key string, revision int64, request ResubmissionRequest) (Application, bool, error) {
	if !hasRole(actor, "VENDOR") {
		return Application{}, false, ErrForbidden
	}
	if !validRegister(RegisterRequest{BusinessName: request.BusinessName, BusinessType: request.BusinessType, ContactName: request.ContactName}) || !validReworkDocuments(request.Documents) {
		return Application{}, false, ErrInvalidRequest
	}
	return service.mutateApplication(actor, actor.Subject, key, revision, "resubmit", request, func(value *Application) error {
		if value.Status != StatusRejected {
			return ErrInvalidTransition
		}
		ids := make([]string, len(request.Documents))
		for i, d := range request.Documents {
			ids[i] = d.AssetID
		}
		ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
		defer cancel()
		if err := service.evidence.VerifyIdentityDocuments(ctx, privateevidence.Scope{TenantID: actor.TenantID, Country: actor.Country, OwnerID: actor.Subject}, ids); err != nil {
			return err
		}
		value.BusinessName = strings.TrimSpace(request.BusinessName)
		value.BusinessType = strings.TrimSpace(request.BusinessType)
		value.ContactName = strings.TrimSpace(request.ContactName)
		value.Documents = cloneDocuments(request.Documents)
		for i := range value.Documents {
			value.Documents[i].OCRStatus = "PENDING"
			value.Documents[i].ExtractedFields = nil
			value.Documents[i].ReviewReason = ""
		}
		value.Bank, value.Visit = nil, nil
		value.Zones = []ServiceZone{}
		value.Verified = false
		value.Status = StatusDocumentsSubmitted
		return nil
	})
}

func (handler *Handler) resubmitDocuments(w http.ResponseWriter, r *http.Request) {
	a, revision, ok := supplyMutation(w, r)
	if !ok {
		return
	}
	var input ResubmissionRequest
	if !decodeSupplyJSON(w, r, &input) {
		return
	}
	service, ok := handler.service.(interface {
		ResubmitDocuments(Actor, string, int64, ResubmissionRequest) (Application, bool, error)
	})
	if !ok {
		writeSupplyProblem(w, r, 503, "SUPPLY_UNAVAILABLE", "Application rework is unavailable.")
		return
	}
	value, replay, err := service.ResubmitDocuments(a, r.Header.Get("Idempotency-Key"), revision, input)
	if err != nil {
		handler.problem(w, r, err)
		return
	}
	writeApplication(w, 200, value, replay)
}
