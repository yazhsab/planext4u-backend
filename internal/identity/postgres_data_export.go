package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) ExportSnapshot(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 1)
}

func (repository *PostgresRepository) ExportSnapshotV2(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 2)
}

func (repository *PostgresRepository) ExportSnapshotV3(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 3)
}

func (repository *PostgresRepository) ExportSnapshotV4(ctx context.Context, trusted TrustedIdentity, now time.Time) (DataExport, error) {
	return repository.exportSnapshot(ctx, trusted, now, 4)
}

func (repository *PostgresRepository) exportSnapshot(ctx context.Context, trusted TrustedIdentity, now time.Time, version int) (DataExport, error) {
	// Profile, sessions, consent withdrawals and their history come from one
	// committed snapshot, with authorization checked inside that same snapshot.
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return DataExport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	account, session, err := loadPrincipalSession(ctx, tx, trusted.Session)
	if errors.Is(err, pgx.ErrNoRows) {
		return DataExport{}, ErrSessionInvalid
	}
	if err != nil {
		return DataExport{}, err
	}
	if session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return DataExport{}, ErrSessionInvalid
	}
	if account.DisabledAt != nil || account.ID != trusted.Subject || account.TenantID != trusted.TenantID || session.TenantID != trusted.TenantID || session.Country != trusted.Country {
		return DataExport{}, ErrForbidden
	}
	var deletionDue bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.account_deletion_requests WHERE identity_id=$1 AND status='SCHEDULED' AND effective_at<=clock_timestamp())`, account.ID).Scan(&deletionDue); err != nil {
		return DataExport{}, err
	}
	if deletionDue {
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
	rows, err := tx.Query(ctx, `SELECT id,device_reference,country,authenticated_at,last_seen_at,refresh_expires_at,revoked_at FROM identity.sessions WHERE identity_id=$1 AND tenant_id=$2 ORDER BY authenticated_at DESC,id LIMIT $3`, account.ID, account.TenantID, maxExportRecords+1)
	if err != nil {
		return DataExport{}, err
	}
	for rows.Next() {
		var item SessionView
		if err = rows.Scan(&item.ID, &item.DeviceReference, &item.Country, &item.AuthenticatedAt, &item.LastSeenAt, &item.ExpiresAt, &item.RevokedAt); err != nil {
			rows.Close()
			return DataExport{}, err
		}
		item.Current = item.ID == session.ID
		value.Sessions = append(value.Sessions, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DataExport{}, err
	}
	rows, err = tx.Query(ctx, `SELECT e.evidence_id,e.purpose,e.granted,e.policy_version,e.recorded_at,e.version,e.country FROM identity.consent_evidence e JOIN identity.identities i ON i.id=e.identity_id WHERE e.identity_id=$1 AND i.tenant_id=$2 ORDER BY e.purpose,e.version LIMIT $3`, account.ID, account.TenantID, maxExportRecords+1)
	if err != nil {
		return DataExport{}, err
	}
	for rows.Next() {
		var item ExportConsent
		if err = rows.Scan(&item.EvidenceID, &item.Purpose, &item.Granted, &item.PolicyVersion, &item.RecordedAt, &item.Version, &item.Country); err != nil {
			rows.Close()
			return DataExport{}, err
		}
		if item.Country != nil {
			item.Consent.Country = *item.Country
		}
		value.ConsentHistory = append(value.ConsentHistory, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DataExport{}, err
	}
	if version >= 2 {
		rows, err = tx.Query(ctx, `SELECT s.country,s.revision,s.contact_count,s.policy_version,s.expires_at FROM identity.contact_snapshots s JOIN identity.identities i ON i.id=s.identity_id WHERE s.identity_id=$1 AND i.tenant_id=$2 ORDER BY s.country LIMIT $3`, account.ID, account.TenantID, maxExportRecords+1)
		if err != nil {
			return DataExport{}, err
		}
		for rows.Next() {
			var item ExportContactSnapshot
			if err = rows.Scan(&item.Country, &item.Revision, &item.ContactCount, &item.PolicyVersion, &item.ExpiresAt); err != nil {
				rows.Close()
				return DataExport{}, err
			}
			*value.ContactSnapshots = append(*value.ContactSnapshots, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return DataExport{}, err
		}
	}
	if version >= 3 {
		rows, err = tx.Query(ctx, `SELECT d.id,d.status,d.reason,d.requested_at,d.effective_at FROM identity.account_deletion_requests d JOIN identity.identities i ON i.id=d.identity_id WHERE d.identity_id=$1 AND i.tenant_id=$2 ORDER BY d.requested_at,d.id LIMIT $3`, account.ID, account.TenantID, maxExportRecords+1)
		if err != nil {
			return DataExport{}, err
		}
		for rows.Next() {
			var item ExportDeletionRequest
			if err = rows.Scan(&item.ID, &item.Status, &item.Reason, &item.RequestedAt, &item.EffectiveAt); err != nil {
				rows.Close()
				return DataExport{}, err
			}
			*value.DeletionHistory = append(*value.DeletionHistory, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return DataExport{}, err
		}
	}
	if value.OccupationReferences != nil {
		rows, err = tx.Query(ctx, `SELECT country,occupation,revision,updated_at FROM identity.profile_occupations WHERE identity_id=$1 AND tenant_id=$2 ORDER BY country LIMIT $3`, account.ID, account.TenantID, maxExportRecords+1)
		if err != nil {
			return DataExport{}, err
		}
		for rows.Next() {
			var item OccupationSelection
			var raw []byte
			if err = rows.Scan(&item.Country, &raw, &item.Revision, &item.UpdatedAt); err != nil {
				rows.Close()
				return DataExport{}, err
			}
			if raw != nil {
				if err = json.Unmarshal(raw, &item.Occupation); err != nil {
					rows.Close()
					return DataExport{}, err
				}
			}
			*value.OccupationReferences = append(*value.OccupationReferences, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return DataExport{}, err
		}
	}
	if value.VendorApplications != nil {
		rows, err = tx.Query(ctx, `SELECT country,revision,status,business_name,business_type,contact_name,created_at,updated_at FROM supply.privacy_export_vendor_applications($1,$2,$3,$4) LIMIT $5`, account.TenantID, account.ID, session.ID, session.Country, maxExportRecords+1)
		if err != nil {
			return DataExport{}, err
		}
		for rows.Next() {
			var item ExportVendorApplication
			if err = rows.Scan(&item.Country, &item.Revision, &item.Status, &item.BusinessName, &item.BusinessType, &item.ContactName, &item.CreatedAt, &item.UpdatedAt); err != nil {
				rows.Close()
				return DataExport{}, err
			}
			*value.VendorApplications = append(*value.VendorApplications, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return DataExport{}, err
		}
	}
	if value.RiderProfiles != nil {
		rows, err = tx.Query(ctx, `SELECT country,revision,status,full_name,vehicle_type,vehicle_number,zones,created_at,updated_at FROM fulfillment.privacy_export_rider_profiles($1,$2,$3,$4) LIMIT $5`, account.TenantID, account.ID, session.ID, session.Country, maxExportRecords+1)
		if err != nil {
			return DataExport{}, err
		}
		for rows.Next() {
			var item ExportRiderProfile
			if err = rows.Scan(&item.Country, &item.Revision, &item.Status, &item.FullName, &item.VehicleType, &item.VehicleNumber, &item.Zones, &item.CreatedAt, &item.UpdatedAt); err != nil {
				rows.Close()
				return DataExport{}, err
			}
			*value.RiderProfiles = append(*value.RiderProfiles, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return DataExport{}, err
		}
	}
	if err = finishDataExport(&value); err != nil {
		return DataExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DataExport{}, err
	}
	return value, nil
}
