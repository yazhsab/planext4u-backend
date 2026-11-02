package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type OccupationReference struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	Country        string `json:"country"`
	NameEN         string `json:"name_en"`
	NameTA         string `json:"name_ta"`
	MasterRevision int64  `json:"master_revision"`
}
type OccupationSelection struct {
	Country    string               `json:"country"`
	Occupation *OccupationReference `json:"occupation"`
	Revision   int64                `json:"revision"`
	UpdatedAt  *time.Time           `json:"updated_at,omitempty"`
}
type OccupationUpdate struct {
	OccupationID     string `json:"occupation_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type OccupationRepository interface {
	Occupation(context.Context, TrustedIdentity, time.Time) (OccupationSelection, error)
	UpdateOccupation(context.Context, TrustedIdentity, OccupationUpdate, time.Time) (OccupationSelection, error)
}

func (handler *Handler) handleOccupation(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	if request.URL.RawQuery != "" {
		handler.writeFailure(writer, request, ErrInvalidInput)
		return
	}
	var value OccupationSelection
	if request.Method == http.MethodPut {
		var input OccupationUpdate
		if err = decodeJSON(request, &input); err != nil {
			handler.writeFailure(writer, request, err)
			return
		}
		value, err = handler.service.UpdateOccupation(request.Context(), trusted, input)
	} else {
		value, err = handler.service.Occupation(request.Context(), trusted)
	}
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, 200, value)
}
func (service *Service) Occupation(ctx context.Context, trusted TrustedIdentity) (OccupationSelection, error) {
	if _, _, err := service.Principal(ctx, trusted); err != nil {
		return OccupationSelection{}, err
	}
	repository, ok := service.repository.(OccupationRepository)
	if !ok {
		return OccupationSelection{}, ErrProviderUnavailable
	}
	return repository.Occupation(ctx, trusted, service.now().UTC())
}
func (service *Service) UpdateOccupation(ctx context.Context, trusted TrustedIdentity, input OccupationUpdate) (OccupationSelection, error) {
	if _, _, err := service.Principal(ctx, trusted); err != nil {
		return OccupationSelection{}, err
	}
	if input.ExpectedRevision < 0 || input.ExpectedRevision >= 1<<62 || input.OccupationID != "" && uuid.Validate(input.OccupationID) != nil {
		return OccupationSelection{}, ErrInvalidInput
	}
	repository, ok := service.repository.(OccupationRepository)
	if !ok {
		return OccupationSelection{}, ErrProviderUnavailable
	}
	return repository.UpdateOccupation(ctx, trusted, input, service.now().UTC())
}
func occupationSelection(ctx context.Context, tx pgx.Tx, trusted TrustedIdentity) (OccupationSelection, error) {
	result := OccupationSelection{Country: trusted.Country}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT occupation,revision,updated_at FROM identity.profile_occupations WHERE identity_id=$1 AND tenant_id=$2 AND country=$3`, trusted.Subject, trusted.TenantID, trusted.Country).Scan(&raw, &result.Revision, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return OccupationSelection{}, err
	}
	if raw != nil {
		if err = json.Unmarshal(raw, &result.Occupation); err != nil {
			return OccupationSelection{}, err
		}
	}
	return result, nil
}
func (repository *PostgresRepository) Occupation(ctx context.Context, trusted TrustedIdentity, now time.Time) (OccupationSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return OccupationSelection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = validateDeletionAuthority(ctx, tx, trusted, now); err != nil {
		return OccupationSelection{}, err
	}
	value, err := occupationSelection(ctx, tx, trusted)
	if err != nil {
		return OccupationSelection{}, err
	}
	return value, tx.Commit(ctx)
}
func (repository *PostgresRepository) UpdateOccupation(ctx context.Context, trusted TrustedIdentity, input OccupationUpdate, now time.Time) (OccupationSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return OccupationSelection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = validateDeletionAuthority(ctx, tx, trusted, now); err != nil {
		return OccupationSelection{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "occupation:"+trusted.TenantID+":"+trusted.Subject+":"+trusted.Country); err != nil {
		return OccupationSelection{}, err
	}
	current, err := occupationSelection(ctx, tx, trusted)
	if err != nil {
		return OccupationSelection{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return OccupationSelection{}, ErrVersionConflict
	}
	currentID := ""
	if current.Occupation != nil {
		currentID = current.Occupation.ID
	}
	// Retired references remain readable; unchanged selections need no new grant.
	if currentID == input.OccupationID {
		return current, tx.Commit(ctx)
	}
	value := OccupationSelection{Country: trusted.Country, Revision: current.Revision + 1, UpdatedAt: &now}
	var raw []byte
	if input.OccupationID != "" {
		if uuid.Validate(trusted.TenantID) != nil {
			return OccupationSelection{}, ErrInvalidInput
		}
		if err = tx.QueryRow(ctx, `SELECT admin.lock_occupation_reference($1,$2,$3)`, trusted.TenantID, trusted.Country, input.OccupationID).Scan(&raw); err != nil {
			return OccupationSelection{}, err
		}
		if raw == nil {
			return OccupationSelection{}, ErrInvalidInput
		}
		if err = json.Unmarshal(raw, &value.Occupation); err != nil {
			return OccupationSelection{}, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.profile_occupations(identity_id,tenant_id,country,occupation,revision,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(identity_id,country) DO UPDATE SET occupation=EXCLUDED.occupation,revision=EXCLUDED.revision,updated_at=EXCLUDED.updated_at`, trusted.Subject, trusted.TenantID, trusted.Country, raw, value.Revision, now); err != nil {
		return OccupationSelection{}, err
	}
	if err = insertSecurityEvent(ctx, tx, "PROFILE_OCCUPATION_UPDATED", trusted.Subject, trusted.Session, "", "", "SUCCEEDED", now); err != nil {
		return OccupationSelection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OccupationSelection{}, err
	}
	return value, nil
}
