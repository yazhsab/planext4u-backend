package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) AccountLifecycle(ctx context.Context, id string) (AccountLifecycle, error) {
	result := AccountLifecycle{Status: "ACTIVE"}
	var request DeletionRequest
	err := repository.pool.QueryRow(ctx, `SELECT id,identity_id,status,reason,requested_at,effective_at FROM identity.account_deletion_requests WHERE identity_id=$1 AND status='SCHEDULED'`, id).Scan(&request.ID, &request.IdentityID, &request.Status, &request.Reason, &request.RequestedAt, &request.EffectiveAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return AccountLifecycle{}, err
	}
	result.Status = "DELETION_SCHEDULED"
	result.Deletion = &request
	return result, nil
}

func (repository *PostgresRepository) DeactivateAccount(ctx context.Context, trusted TrustedIdentity, now time.Time) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Delivery commands/readback hold the shared counterpart through their
	// commit. Take this before the identity row, matching consent writes, so
	// deactivation cannot acknowledge while earlier processing is in flight.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "consent:"+trusted.Subject); err != nil {
		return err
	}
	var disabled *time.Time
	if err = tx.QueryRow(ctx, `SELECT disabled_at FROM identity.identities WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, trusted.Subject, trusted.TenantID).Scan(&disabled); errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	} else if err != nil {
		return err
	}
	if disabled != nil {
		return ErrForbidden
	}
	account, session, err := loadPrincipalSession(ctx, tx, trusted.Session)
	if err != nil {
		return err
	}
	if account.ID != trusted.Subject || account.TenantID != trusted.TenantID || session.Country != trusted.Country || session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return ErrSessionInvalid
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.account_restrictions(identity_id,source,changed_at) VALUES($1,'USER_DEACTIVATED',$2) ON CONFLICT(identity_id) DO UPDATE SET source=EXCLUDED.source,changed_at=EXCLUDED.changed_at,revision=identity.account_restrictions.revision+1`, trusted.Subject, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.identities SET disabled_at=$2 WHERE id=$1`, trusted.Subject, now); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM identity.sessions WHERE identity_id=$1 AND revoked_at IS NULL ORDER BY id`, trusted.Subject)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = revokeSessionTx(ctx, tx, id, now); err != nil {
			return err
		}
	}
	if err = insertSecurityEvent(ctx, tx, "ACCOUNT_SELF_DEACTIVATED", trusted.Subject, trusted.Session, "", "", "SUCCEEDED", now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (repository *PostgresRepository) RecoverAccount(ctx context.Context, tenant string, provider ProviderIdentity, now time.Time) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	var disabled *time.Time
	err = tx.QueryRow(ctx, `SELECT id,disabled_at FROM identity.identities WHERE tenant_id=$1 AND provider=$2 AND provider_subject=$3 FOR UPDATE`, tenant, provider.Provider, provider.Subject).Scan(&id, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if disabled == nil {
		return nil
	}
	var source string
	var changed time.Time
	if err = tx.QueryRow(ctx, `SELECT source,changed_at FROM identity.account_restrictions WHERE identity_id=$1 FOR UPDATE`, id).Scan(&source, &changed); errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	} else if err != nil {
		return err
	}
	if source != "USER_DEACTIVATED" || !changed.Equal(*disabled) {
		return ErrForbidden
	}
	var due bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.account_deletion_requests WHERE identity_id=$1 AND status='SCHEDULED' AND effective_at<=$2)`, id, now).Scan(&due); err != nil {
		return err
	}
	if due {
		return ErrForbidden
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.identities SET disabled_at=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.account_restrictions SET source='ACTIVE',changed_at=$2,revision=revision+1 WHERE identity_id=$1`, id, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.account_deletion_requests SET status='CANCELLED' WHERE identity_id=$1 AND status='SCHEDULED'`, id); err != nil {
		return err
	}
	if err = insertSecurityEvent(ctx, tx, "ACCOUNT_RECOVERED", id, "", "", "", "SUCCEEDED", now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Keep the identity and source session locked until a lifecycle write commits.
// A revoked token or disabled account cannot win a race after Service.Principal.
func validateDeletionAuthority(ctx context.Context, tx pgx.Tx, trusted TrustedIdentity, now time.Time) error {
	var disabled *time.Time
	err := tx.QueryRow(ctx, `SELECT disabled_at FROM identity.identities WHERE id=$1 AND tenant_id=$2 FOR SHARE`, trusted.Subject, trusted.TenantID).Scan(&disabled)
	if errors.Is(err, pgx.ErrNoRows) || disabled != nil {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	var subject, tenant, country string
	var revoked *time.Time
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT identity_id,tenant_id,country,revoked_at,refresh_expires_at FROM identity.sessions WHERE id=$1 FOR SHARE`, trusted.Session).Scan(&subject, &tenant, &country, &revoked, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionInvalid
	}
	if err != nil {
		return err
	}
	if subject != trusted.Subject || tenant != trusted.TenantID || country != trusted.Country || revoked != nil || !now.Before(expires) {
		return ErrSessionInvalid
	}
	return nil
}

func (repository *PostgresRepository) CancelDeletion(ctx context.Context, trusted TrustedIdentity, requestID string, now time.Time) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = validateDeletionAuthority(ctx, tx, trusted, now); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE identity.account_deletion_requests SET status='CANCELLED' WHERE identity_id=$1 AND id=$2 AND status='SCHEDULED' AND effective_at>$3`, trusted.Subject, requestID, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	if err = insertSecurityEvent(ctx, tx, "ACCOUNT_DELETION_CANCELLED", trusted.Subject, trusted.Session, "", "", "SUCCEEDED", now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ AccountLifecycleRepository = (*PostgresRepository)(nil)
