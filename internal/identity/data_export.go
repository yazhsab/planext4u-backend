package identity

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// These budgets reject an oversized snapshot; they never return a silently
// truncated file. A resumable cross-owner export is a separate workflow.
const maxExportRecords = 10_000

// The shared mobile HTTP transport accepts two MiB, including the HTTP JSON
// encoder's trailing newline.
const maxExportBytes = (2 << 20) - 1

type ExportCoverage struct {
	Included []string `json:"included"`
	Excluded []string `json:"excluded"`
}

type ExportConsent struct {
	Consent
	Country *string `json:"country"`
}

// Safe owner-visible snapshot metadata. Phone numbers, HMACs, key IDs and
// idempotency fingerprints are never exported.
type ExportContactSnapshot struct {
	Country       string     `json:"country"`
	Revision      int64      `json:"revision"`
	ContactCount  int        `json:"contact_count"`
	PolicyVersion *string    `json:"policy_version,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// Identity-owned lifecycle history. The root export already identifies the
// account, so no second owner selector or provider credential is included.
type ExportDeletionRequest struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason"`
	RequestedAt time.Time `json:"requested_at"`
	EffectiveAt time.Time `json:"effective_at"`
}

// Schema 4 includes only the subject's current onboarding records. Private
// evidence, bank references, reviews and historical submissions are excluded.
type ExportVendorApplication struct {
	Country      string    `json:"country"`
	Revision     int64     `json:"revision"`
	Status       string    `json:"status"`
	BusinessName string    `json:"business_name"`
	BusinessType string    `json:"business_type"`
	ContactName  string    `json:"contact_name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ExportRiderProfile struct {
	Country       string    `json:"country"`
	Revision      int64     `json:"revision"`
	Status        string    `json:"status"`
	FullName      string    `json:"full_name"`
	VehicleType   string    `json:"vehicle_type"`
	VehicleNumber string    `json:"vehicle_number"`
	Zones         []string  `json:"zones"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func newDataExport(account Identity, session Session, now time.Time) DataExport {
	return DataExport{
		SchemaVersion: 1,
		GeneratedAt:   now.UTC(), IdentityID: account.ID, TenantID: account.TenantID,
		Country: session.Country, Roles: append([]Role(nil), account.Roles...), Profile: account.Profile,
		Sessions: []SessionView{}, Consents: []Consent{}, ConsentHistory: []ExportConsent{},
		Coverage: ExportCoverage{
			Included: []string{"identity.profile", "identity.roles", "identity.sessions", "identity.consent_history"},
			Excluded: []string{"orders_and_payments", "wallet", "vendor_and_rider_applications", "delivery_and_location", "bookings", "social_and_messages", "private_media", "security_audit", "deletion_history"},
		},
	}
}

func newDataExportV2(account Identity, session Session, now time.Time) DataExport {
	value := newDataExport(account, session, now)
	value.SchemaVersion = 2
	contacts := []ExportContactSnapshot{}
	value.ContactSnapshots = &contacts
	value.Coverage.Included = append(value.Coverage.Included, "identity.contact_snapshot_metadata")
	return value
}

func newDataExportV3(account Identity, session Session, now time.Time) DataExport {
	value := newDataExportV2(account, session, now)
	value.SchemaVersion = 3
	history := []ExportDeletionRequest{}
	value.DeletionHistory = &history
	occupations := []OccupationSelection{}
	value.OccupationReferences = &occupations
	value.Coverage.Included = append(value.Coverage.Included, "identity.occupation_references")
	value.Coverage.Included = append(value.Coverage.Included, "identity.deletion_history")
	for index, excluded := range value.Coverage.Excluded {
		if excluded == "deletion_history" {
			value.Coverage.Excluded = append(value.Coverage.Excluded[:index], value.Coverage.Excluded[index+1:]...)
			break
		}
	}
	return value
}

func newDataExportV4(account Identity, session Session, now time.Time) DataExport {
	value := newDataExportV3(account, session, now)
	value.SchemaVersion = 4
	applications := []ExportVendorApplication{}
	profiles := []ExportRiderProfile{}
	value.VendorApplications = &applications
	value.RiderProfiles = &profiles
	value.Coverage.Included = append(value.Coverage.Included,
		"supply.current_vendor_application_metadata", "fulfillment.current_rider_profile_metadata")
	for index, excluded := range value.Coverage.Excluded {
		if excluded == "vendor_and_rider_applications" {
			value.Coverage.Excluded = append(value.Coverage.Excluded[:index], value.Coverage.Excluded[index+1:]...)
			break
		}
	}
	value.Coverage.Excluded = append(value.Coverage.Excluded,
		"vendor_rider_private_evidence_and_reviews", "vendor_rider_bank_details", "vendor_rider_submission_history")
	return value
}

func finishDataExport(value *DataExport) error {
	if len(value.Sessions) > maxExportRecords || len(value.ConsentHistory) > maxExportRecords || value.ContactSnapshots != nil && len(*value.ContactSnapshots) > maxExportRecords || value.DeletionHistory != nil && len(*value.DeletionHistory) > maxExportRecords || value.OccupationReferences != nil && len(*value.OccupationReferences) > maxExportRecords || value.VendorApplications != nil && len(*value.VendorApplications) > maxExportRecords || value.RiderProfiles != nil && len(*value.RiderProfiles) > maxExportRecords {
		return ErrExportTooLarge
	}
	sort.Slice(value.Sessions, func(i, j int) bool {
		if value.Sessions[i].AuthenticatedAt.Equal(value.Sessions[j].AuthenticatedAt) {
			return value.Sessions[i].ID < value.Sessions[j].ID
		}
		return value.Sessions[i].AuthenticatedAt.After(value.Sessions[j].AuthenticatedAt)
	})
	sort.Slice(value.ConsentHistory, func(i, j int) bool {
		if value.ConsentHistory[i].Purpose == value.ConsentHistory[j].Purpose {
			return value.ConsentHistory[i].Version < value.ConsentHistory[j].Version
		}
		return value.ConsentHistory[i].Purpose < value.ConsentHistory[j].Purpose
	})
	latest := map[ConsentPurpose]Consent{}
	for _, evidence := range value.ConsentHistory {
		if evidence.Country == nil || *evidence.Country != value.Country {
			continue
		}
		if old, exists := latest[evidence.Purpose]; !exists || old.Version < evidence.Version {
			latest[evidence.Purpose] = evidence.Consent
		}
	}
	for _, evidence := range latest {
		value.Consents = append(value.Consents, evidence)
	}
	sort.Slice(value.Consents, func(i, j int) bool { return value.Consents[i].Purpose < value.Consents[j].Purpose })
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > maxExportBytes {
		return ErrExportTooLarge
	}
	return nil
}

func (repository *MemoryRepository) ExportSnapshot(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 1)
}

func (repository *MemoryRepository) ExportSnapshotV2(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 2)
}

func (repository *MemoryRepository) ExportSnapshotV3(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 3)
}

func (repository *MemoryRepository) ExportSnapshotV4(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 4)
}

func (repository *MemoryRepository) exportSnapshot(_ context.Context, trusted TrustedIdentity, now time.Time, version int) (DataExport, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	session, exists := repository.sessions[trusted.Session]
	if !exists || session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return DataExport{}, ErrSessionInvalid
	}
	account, exists := repository.identities[session.IdentityID]
	if !exists || account.DisabledAt != nil || account.ID != trusted.Subject || account.TenantID != trusted.TenantID || session.TenantID != trusted.TenantID || session.Country != trusted.Country {
		return DataExport{}, ErrForbidden
	}
	if repository.deletionDueLocked(account.ID, now) {
		return DataExport{}, ErrForbidden
	}
	value := newDataExport(account, session, now)
	if version == 2 {
		value = newDataExportV2(account, session, now)
	} else if version == 3 {
		value = newDataExportV3(account, session, now)
	} else if version == 4 {
		value = newDataExportV4(account, session, now)
	}
	for _, ownSession := range repository.sessions {
		if ownSession.IdentityID == account.ID && ownSession.TenantID == account.TenantID {
			value.Sessions = append(value.Sessions, sessionView(ownSession, ownSession.ID == session.ID))
		}
	}
	for _, evidence := range repository.consents[account.ID] {
		item := ExportConsent{Consent: evidence}
		if evidence.Country != "" {
			country := evidence.Country
			item.Country = &country
		}
		value.ConsentHistory = append(value.ConsentHistory, item)
	}
	if version >= 3 {
		for _, request := range repository.deletionHistory {
			if request.IdentityID == account.ID {
				*value.DeletionHistory = append(*value.DeletionHistory, ExportDeletionRequest{
					ID: request.ID, Status: request.Status, Reason: request.Reason,
					RequestedAt: request.RequestedAt, EffectiveAt: request.EffectiveAt,
				})
			}
		}
		sort.Slice(*value.DeletionHistory, func(i, j int) bool {
			left, right := (*value.DeletionHistory)[i], (*value.DeletionHistory)[j]
			if left.RequestedAt.Equal(right.RequestedAt) {
				return left.ID < right.ID
			}
			return left.RequestedAt.Before(right.RequestedAt)
		})
	}
	if err := finishDataExport(&value); err != nil {
		return DataExport{}, err
	}
	return value, nil
}
