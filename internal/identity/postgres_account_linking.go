package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) ConfirmProviderAccount(ctx context.Context, trusted TrustedIdentity, proof ProviderAccount, now time.Time) error {
	if !validProviderAccount(proof) {
		return ErrProviderRejected
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Same lock order as account deactivation: account, then session.
	var provider, subject string
	var disabled *time.Time
	err = tx.QueryRow(ctx, `SELECT provider,provider_subject,disabled_at FROM identity.identities WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, trusted.Subject, trusted.TenantID).Scan(&provider, &subject, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if disabled != nil || provider != "firebase" || subject != proof.Subject {
		return ErrForbidden
	}
	session, err := loadSessionForUpdate(ctx, tx, trusted.Session)
	if err != nil {
		return ErrSessionInvalid
	}
	if session.IdentityID != trusted.Subject || session.TenantID != trusted.TenantID || session.Country != trusted.Country || session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return ErrSessionInvalid
	}
	// Only provider-verified contacts enter the platform profile. An unverified
	// replacement clears the previous contact rather than retaining stale proof.
	if _, err = tx.Exec(ctx, `UPDATE identity.profiles SET email=$2,phone=$3,version=version+1,updated_at=$4 WHERE identity_id=$1 AND (email IS DISTINCT FROM $2 OR phone IS DISTINCT FROM $3)`, trusted.Subject, proof.VerifiedEmail, proof.VerifiedPhone, now); err != nil {
		return err
	}
	if err = insertSecurityEvent(ctx, tx, "SIGN_IN_METHODS_VERIFIED", trusted.Subject, session.ID, session.DeviceReference, "", "SUCCEEDED", now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (repository *MemoryRepository) ConfirmProviderAccount(_ context.Context, trusted TrustedIdentity, proof ProviderAccount, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !validProviderAccount(proof) {
		return ErrProviderRejected
	}
	account, found := repository.identities[trusted.Subject]
	if !found || account.TenantID != trusted.TenantID || account.DisabledAt != nil || account.Provider != "firebase" || account.ProviderSubject != proof.Subject {
		return ErrForbidden
	}
	session, found := repository.sessions[trusted.Session]
	if !found || session.IdentityID != trusted.Subject || session.TenantID != trusted.TenantID || session.Country != trusted.Country || session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return ErrSessionInvalid
	}
	if account.Profile.Email != proof.VerifiedEmail || account.Profile.Phone != proof.VerifiedPhone {
		account.Profile.Email, account.Profile.Phone = proof.VerifiedEmail, proof.VerifiedPhone
		account.Profile.Version++
		account.Profile.UpdatedAt = now
		repository.identities[trusted.Subject] = account
	}
	repository.appendAudit(AuditEvent{Type: "SIGN_IN_METHODS_VERIFIED", IdentityID: trusted.Subject, SessionID: session.ID, DeviceRef: session.DeviceReference, Outcome: "SUCCEEDED", OccurredAt: now})
	return nil
}
