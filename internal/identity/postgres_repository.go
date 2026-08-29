package identity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrInvalidConfiguration
	}
	return &PostgresRepository{pool: pool}, nil
}

func (repository *PostgresRepository) Ready(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT to_regclass('identity.identities') IS NOT NULL
		   AND to_regclass('identity.sessions') IS NOT NULL
		   AND to_regclass('identity.refresh_tokens') IS NOT NULL
		   AND to_regclass('identity.consent_evidence') IS NOT NULL
		   AND to_regclass('identity.security_events') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check identity schema readiness: %w", err)
	}
	if !ready {
		return errors.New("identity schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) StartSession(ctx context.Context, params StartSessionParams) (Identity, Session, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Identity{}, Session{}, fmt.Errorf("begin identity exchange: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	inserted, err := transaction.Exec(ctx, `
		INSERT INTO identity.identities (id, tenant_id, provider, provider_subject, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, provider, provider_subject) DO NOTHING`,
		params.IdentityID, params.TenantID, params.Provider.Provider, params.Provider.Subject, params.Now)
	if err != nil {
		return Identity{}, Session{}, fmt.Errorf("create identity: %w", err)
	}
	if inserted.RowsAffected() == 1 {
		if _, err := transaction.Exec(ctx, `
			INSERT INTO identity.profiles (identity_id, display_name, locale, time_zone, version, updated_at)
			VALUES ($1, '', 'en', 'Asia/Kolkata', 1, $2)`, params.IdentityID, params.Now); err != nil {
			return Identity{}, Session{}, fmt.Errorf("create profile: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO identity.identity_roles (identity_id, role, granted_at)
			VALUES ($1, 'CUSTOMER', $2)`, params.IdentityID, params.Now); err != nil {
			return Identity{}, Session{}, fmt.Errorf("grant default role: %w", err)
		}
		if err := insertSecurityEvent(ctx, transaction, "IDENTITY_CREATED", params.IdentityID, "", "", "", "SUCCEEDED", params.Now); err != nil {
			return Identity{}, Session{}, err
		}
	}

	account, err := loadIdentityByProvider(ctx, transaction, params.TenantID, params.Provider)
	if err != nil {
		return Identity{}, Session{}, err
	}
	if account.DisabledAt != nil {
		return Identity{}, Session{}, ErrForbidden
	}
	session := Session{
		ID:              params.SessionID,
		IdentityID:      account.ID,
		TenantID:        account.TenantID,
		Country:         params.Country,
		DeviceID:        params.DeviceID,
		DeviceReference: params.DeviceReference,
		RefreshDigest:   params.RefreshDigest,
		RefreshExpires:  params.RefreshExpires,
		AuthenticatedAt: params.Now,
		LastSeenAt:      params.Now,
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO identity.sessions
			(id, identity_id, tenant_id, country, device_id, device_reference, refresh_expires_at, authenticated_at, last_seen_at, rotation)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, 0)`,
		session.ID, session.IdentityID, session.TenantID, session.Country, session.DeviceID,
		session.DeviceReference, session.RefreshExpires, session.AuthenticatedAt); err != nil {
		return Identity{}, Session{}, fmt.Errorf("create session: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO identity.refresh_tokens (digest, session_id, status, created_at)
		VALUES ($1, $2, 'ACTIVE', $3)`, session.RefreshDigest, session.ID, params.Now); err != nil {
		return Identity{}, Session{}, fmt.Errorf("store refresh token digest: %w", err)
	}
	if err := insertSecurityEvent(ctx, transaction, "SESSION_AUTHENTICATED", account.ID, session.ID, session.DeviceReference, "", "SUCCEEDED", params.Now); err != nil {
		return Identity{}, Session{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Identity{}, Session{}, fmt.Errorf("commit identity exchange: %w", err)
	}
	return account, session, nil
}

func (repository *PostgresRepository) RotateSession(ctx context.Context, params RotateSessionParams) (Identity, Session, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Identity{}, Session{}, fmt.Errorf("begin refresh rotation: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status, sessionID string
	if err := transaction.QueryRow(ctx, `
		SELECT status, session_id
		FROM identity.refresh_tokens
		WHERE digest = $1
		FOR UPDATE`, params.CurrentDigest).Scan(&status, &sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, Session{}, ErrRefreshInvalid
		}
		return Identity{}, Session{}, fmt.Errorf("load refresh token: %w", err)
	}
	session, err := loadSessionForUpdate(ctx, transaction, sessionID)
	if err != nil {
		return Identity{}, Session{}, err
	}
	if status == "CONSUMED" {
		changed, err := revokeSessionTx(ctx, transaction, session.ID, params.Now)
		if err != nil {
			return Identity{}, Session{}, err
		}
		if changed {
			if err := insertSecurityEvent(ctx, transaction, "REFRESH_REUSE_DETECTED", session.IdentityID, session.ID, session.DeviceReference, "", "SUCCEEDED", params.Now); err != nil {
				return Identity{}, Session{}, err
			}
		}
		if err := transaction.Commit(ctx); err != nil {
			return Identity{}, Session{}, fmt.Errorf("commit refresh reuse revocation: %w", err)
		}
		return Identity{}, Session{}, ErrRefreshReuse
	}
	if status != "ACTIVE" || session.RevokedAt != nil {
		return Identity{}, Session{}, ErrRefreshInvalid
	}
	if !params.Now.Before(session.RefreshExpires) {
		changed, err := revokeSessionTx(ctx, transaction, session.ID, params.Now)
		if err != nil {
			return Identity{}, Session{}, err
		}
		if changed {
			if err := insertSecurityEvent(ctx, transaction, "REFRESH_EXPIRED", session.IdentityID, session.ID, session.DeviceReference, "", "SUCCEEDED", params.Now); err != nil {
				return Identity{}, Session{}, err
			}
		}
		if err := transaction.Commit(ctx); err != nil {
			return Identity{}, Session{}, fmt.Errorf("commit refresh expiry: %w", err)
		}
		return Identity{}, Session{}, ErrRefreshExpired
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE identity.refresh_tokens
		SET status = 'CONSUMED', consumed_at = $2
		WHERE digest = $1 AND status = 'ACTIVE'`, params.CurrentDigest, params.Now); err != nil {
		return Identity{}, Session{}, fmt.Errorf("consume refresh token: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO identity.refresh_tokens (digest, session_id, status, created_at)
		VALUES ($1, $2, 'ACTIVE', $3)`, params.NextDigest, session.ID, params.Now); err != nil {
		return Identity{}, Session{}, fmt.Errorf("store rotated refresh token: %w", err)
	}
	if err := transaction.QueryRow(ctx, `
		UPDATE identity.sessions
		SET last_seen_at = $2, rotation = rotation + 1
		WHERE id = $1
		RETURNING rotation`, session.ID, params.Now).Scan(&session.Rotation); err != nil {
		return Identity{}, Session{}, fmt.Errorf("update rotated session: %w", err)
	}
	session.RefreshDigest = params.NextDigest
	session.LastSeenAt = params.Now
	account, err := loadIdentityByID(ctx, transaction, session.IdentityID)
	if err != nil {
		return Identity{}, Session{}, err
	}
	if account.DisabledAt != nil {
		changed, revokeErr := revokeSessionTx(ctx, transaction, session.ID, params.Now)
		if revokeErr != nil {
			return Identity{}, Session{}, revokeErr
		}
		if changed {
			if eventErr := insertSecurityEvent(ctx, transaction, "IDENTITY_DISABLED", account.ID, session.ID, session.DeviceReference, "", "SUCCEEDED", params.Now); eventErr != nil {
				return Identity{}, Session{}, eventErr
			}
		}
		if commitErr := transaction.Commit(ctx); commitErr != nil {
			return Identity{}, Session{}, fmt.Errorf("commit disabled identity revocation: %w", commitErr)
		}
		return Identity{}, Session{}, ErrForbidden
	}
	if err := insertSecurityEvent(ctx, transaction, "SESSION_REFRESHED", account.ID, session.ID, session.DeviceReference, "", "SUCCEEDED", params.Now); err != nil {
		return Identity{}, Session{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Identity{}, Session{}, fmt.Errorf("commit refresh rotation: %w", err)
	}
	return account, session, nil
}

func (repository *PostgresRepository) RevokeByRefresh(ctx context.Context, digest string, now time.Time) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin refresh revocation: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var sessionID string
	if err := transaction.QueryRow(ctx, `
		SELECT session_id FROM identity.refresh_tokens WHERE digest = $1 FOR UPDATE`, digest).Scan(&sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load refresh token for revocation: %w", err)
	}
	session, err := loadSessionForUpdate(ctx, transaction, sessionID)
	if err != nil {
		return err
	}
	changed, err := revokeSessionTx(ctx, transaction, session.ID, now)
	if err != nil {
		return err
	}
	if changed {
		if err := insertSecurityEvent(ctx, transaction, "SESSION_REVOKED", session.IdentityID, session.ID, session.DeviceReference, "", "SUCCEEDED", now); err != nil {
			return err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit refresh revocation: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) Authenticate(ctx context.Context, trusted TrustedIdentity, now time.Time) (Identity, Session, error) {
	account, session, err := loadPrincipalSession(ctx, repository.pool, trusted.Session)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, Session{}, ErrSessionInvalid
		}
		return Identity{}, Session{}, err
	}
	if session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return Identity{}, Session{}, ErrSessionInvalid
	}
	if account.DisabledAt != nil || account.ID != trusted.Subject ||
		account.TenantID != trusted.TenantID || session.TenantID != trusted.TenantID ||
		session.Country != trusted.Country {
		return Identity{}, Session{}, ErrForbidden
	}
	return account, session, nil
}

func (repository *PostgresRepository) ListSessions(ctx context.Context, identityID string) ([]Session, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT id, identity_id, tenant_id, country, device_id, device_reference,
		       refresh_expires_at, authenticated_at, last_seen_at, revoked_at, rotation
		FROM identity.sessions
		WHERE identity_id = $1
		ORDER BY authenticated_at DESC`, identityID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	result := make([]Session, 0)
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.IdentityID, &session.TenantID, &session.Country,
			&session.DeviceID, &session.DeviceReference, &session.RefreshExpires,
			&session.AuthenticatedAt, &session.LastSeenAt, &session.RevokedAt, &session.Rotation); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	if len(result) == 0 {
		var exists bool
		if err := repository.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.identities WHERE id = $1)`, identityID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check identity: %w", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return result, nil
}

func (repository *PostgresRepository) RevokeSession(ctx context.Context, identityID, sessionID string, now time.Time) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin session revocation: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var session Session
	if err := transaction.QueryRow(ctx, `
		SELECT id, identity_id, tenant_id, country, device_id, device_reference,
		       refresh_expires_at, authenticated_at, last_seen_at, revoked_at, rotation
		FROM identity.sessions
		WHERE id = $1 AND identity_id = $2
		FOR UPDATE`, sessionID, identityID).Scan(
		&session.ID, &session.IdentityID, &session.TenantID, &session.Country, &session.DeviceID,
		&session.DeviceReference, &session.RefreshExpires, &session.AuthenticatedAt,
		&session.LastSeenAt, &session.RevokedAt, &session.Rotation); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load owned session: %w", err)
	}
	changed, err := revokeSessionTx(ctx, transaction, session.ID, now)
	if err != nil {
		return err
	}
	if changed {
		if err := insertSecurityEvent(ctx, transaction, "SESSION_REVOKED", identityID, session.ID, session.DeviceReference, "", "SUCCEEDED", now); err != nil {
			return err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit session revocation: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) UpdateProfile(ctx context.Context, identityID string, update ProfileUpdate, now time.Time) (Profile, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Profile{}, fmt.Errorf("begin profile update: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var profile Profile
	err = transaction.QueryRow(ctx, `
		UPDATE identity.profiles
		SET display_name = $3, locale = $4, time_zone = $5, version = version + 1, updated_at = $6
		WHERE identity_id = $1 AND version = $2
		RETURNING display_name, locale, time_zone, version, updated_at`,
		identityID, update.Version, update.DisplayName, update.Locale, update.TimeZone, now).Scan(
		&profile.DisplayName, &profile.Locale, &profile.TimeZone, &profile.Version, &profile.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if checkErr := transaction.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.profiles WHERE identity_id = $1)`, identityID).Scan(&exists); checkErr != nil {
			return Profile{}, fmt.Errorf("check profile: %w", checkErr)
		}
		if !exists {
			return Profile{}, ErrNotFound
		}
		return Profile{}, ErrVersionConflict
	}
	if err != nil {
		return Profile{}, fmt.Errorf("update profile: %w", err)
	}
	if err := insertSecurityEvent(ctx, transaction, "PROFILE_UPDATED", identityID, "", "", "", "SUCCEEDED", now); err != nil {
		return Profile{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Profile{}, fmt.Errorf("commit profile update: %w", err)
	}
	return profile, nil
}

func (repository *PostgresRepository) ListConsents(ctx context.Context, identityID string) ([]Consent, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT DISTINCT ON (purpose)
		       evidence_id, purpose, granted, policy_version, recorded_at, version
		FROM identity.consent_evidence
		WHERE identity_id = $1
		ORDER BY purpose, version DESC`, identityID)
	if err != nil {
		return nil, fmt.Errorf("list consent evidence: %w", err)
	}
	defer rows.Close()
	result := make([]Consent, 0)
	for rows.Next() {
		var consent Consent
		if err := rows.Scan(&consent.EvidenceID, &consent.Purpose, &consent.Granted,
			&consent.PolicyVersion, &consent.RecordedAt, &consent.Version); err != nil {
			return nil, fmt.Errorf("scan consent evidence: %w", err)
		}
		result = append(result, consent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate consent evidence: %w", err)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Purpose < result[right].Purpose })
	return result, nil
}

func (repository *PostgresRepository) RecordConsent(ctx context.Context, identityID string, consent Consent, now time.Time) (Consent, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Consent{}, fmt.Errorf("begin consent evidence: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var lockedIdentity string
	if err := transaction.QueryRow(ctx, `SELECT id FROM identity.identities WHERE id = $1 FOR UPDATE`, identityID).Scan(&lockedIdentity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consent{}, ErrNotFound
		}
		return Consent{}, fmt.Errorf("lock consent identity: %w", err)
	}
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM identity.consent_evidence
		WHERE identity_id = $1 AND purpose = $2`, identityID, consent.Purpose).Scan(&consent.Version); err != nil {
		return Consent{}, fmt.Errorf("select consent version: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO identity.consent_evidence
			(evidence_id, identity_id, purpose, granted, policy_version, recorded_at, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		consent.EvidenceID, identityID, consent.Purpose, consent.Granted,
		consent.PolicyVersion, consent.RecordedAt, consent.Version); err != nil {
		return Consent{}, fmt.Errorf("record consent evidence: %w", err)
	}
	if err := insertSecurityEvent(ctx, transaction, "CONSENT_CHANGED", identityID, "", "", string(consent.Purpose), consentOutcome(consent.Granted), now); err != nil {
		return Consent{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Consent{}, fmt.Errorf("commit consent evidence: %w", err)
	}
	return consent, nil
}

func (repository *PostgresRepository) CreateDeletionRequest(ctx context.Context, request DeletionRequest) (DeletionRequest, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DeletionRequest{}, fmt.Errorf("begin deletion request: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	err = transaction.QueryRow(ctx, `
		INSERT INTO identity.account_deletion_requests
			(id, identity_id, status, reason, requested_at, effective_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (identity_id) WHERE status = 'SCHEDULED'
		DO UPDATE SET reason = EXCLUDED.reason
		RETURNING id, identity_id, status, reason, requested_at, effective_at`,
		request.ID, request.IdentityID, request.Status, request.Reason, request.RequestedAt, request.EffectiveAt).Scan(
		&request.ID, &request.IdentityID, &request.Status, &request.Reason, &request.RequestedAt, &request.EffectiveAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeletionRequest{}, ErrNotFound
	}
	if err != nil {
		return DeletionRequest{}, fmt.Errorf("create deletion request: %w", err)
	}
	if err := insertSecurityEvent(ctx, transaction, "ACCOUNT_DELETION_REQUESTED", request.IdentityID, "", "", "", "SCHEDULED", request.RequestedAt); err != nil {
		return DeletionRequest{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return DeletionRequest{}, fmt.Errorf("commit deletion request: %w", err)
	}
	return request, nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadIdentityByProvider(ctx context.Context, query queryRower, tenantID string, provider ProviderIdentity) (Identity, error) {
	return scanIdentity(query.QueryRow(ctx, identitySelect+`
		WHERE i.tenant_id = $1 AND i.provider = $2 AND i.provider_subject = $3
		GROUP BY i.id, p.identity_id`, tenantID, provider.Provider, provider.Subject))
}

func loadIdentityByID(ctx context.Context, query queryRower, identityID string) (Identity, error) {
	return scanIdentity(query.QueryRow(ctx, identitySelect+`
		WHERE i.id = $1
		GROUP BY i.id, p.identity_id`, identityID))
}

const identitySelect = `
	SELECT i.id, i.tenant_id, i.provider, i.provider_subject, i.created_at, i.disabled_at,
	       p.display_name, p.locale, p.time_zone, p.version, p.updated_at,
	       ARRAY_AGG(r.role ORDER BY r.role)
	FROM identity.identities i
	JOIN identity.profiles p ON p.identity_id = i.id
	JOIN identity.identity_roles r ON r.identity_id = i.id`

func scanIdentity(row pgx.Row) (Identity, error) {
	var account Identity
	var roleValues []string
	if err := row.Scan(&account.ID, &account.TenantID, &account.Provider, &account.ProviderSubject,
		&account.CreatedAt, &account.DisabledAt, &account.Profile.DisplayName, &account.Profile.Locale,
		&account.Profile.TimeZone, &account.Profile.Version, &account.Profile.UpdatedAt, &roleValues); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, fmt.Errorf("load identity: %w", err)
	}
	account.Roles = make([]Role, len(roleValues))
	for index, role := range roleValues {
		account.Roles[index] = Role(role)
	}
	return account, nil
}

func loadSessionForUpdate(ctx context.Context, transaction pgx.Tx, sessionID string) (Session, error) {
	var session Session
	err := transaction.QueryRow(ctx, `
		SELECT id, identity_id, tenant_id, country, device_id, device_reference,
		       refresh_expires_at, authenticated_at, last_seen_at, revoked_at, rotation
		FROM identity.sessions WHERE id = $1 FOR UPDATE`, sessionID).Scan(
		&session.ID, &session.IdentityID, &session.TenantID, &session.Country, &session.DeviceID,
		&session.DeviceReference, &session.RefreshExpires, &session.AuthenticatedAt,
		&session.LastSeenAt, &session.RevokedAt, &session.Rotation)
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	return session, nil
}

func loadPrincipalSession(ctx context.Context, query queryRower, sessionID string) (Identity, Session, error) {
	var account Identity
	var session Session
	var roleValues []string
	err := query.QueryRow(ctx, `
		SELECT s.id, s.identity_id, s.tenant_id, s.country, s.device_id, s.device_reference,
		       s.refresh_expires_at, s.authenticated_at, s.last_seen_at, s.revoked_at, s.rotation,
		       i.id, i.tenant_id, i.provider, i.provider_subject, i.created_at, i.disabled_at,
		       p.display_name, p.locale, p.time_zone, p.version, p.updated_at,
		       ARRAY_AGG(r.role ORDER BY r.role)
		FROM identity.sessions s
		JOIN identity.identities i ON i.id = s.identity_id
		JOIN identity.profiles p ON p.identity_id = i.id
		JOIN identity.identity_roles r ON r.identity_id = i.id
		WHERE s.id = $1
		GROUP BY s.id, i.id, p.identity_id`, sessionID).Scan(
		&session.ID, &session.IdentityID, &session.TenantID, &session.Country, &session.DeviceID,
		&session.DeviceReference, &session.RefreshExpires, &session.AuthenticatedAt,
		&session.LastSeenAt, &session.RevokedAt, &session.Rotation,
		&account.ID, &account.TenantID, &account.Provider, &account.ProviderSubject,
		&account.CreatedAt, &account.DisabledAt, &account.Profile.DisplayName, &account.Profile.Locale,
		&account.Profile.TimeZone, &account.Profile.Version, &account.Profile.UpdatedAt, &roleValues)
	if err != nil {
		return Identity{}, Session{}, err
	}
	account.Roles = make([]Role, len(roleValues))
	for index, role := range roleValues {
		account.Roles[index] = Role(role)
	}
	return account, session, nil
}

func revokeSessionTx(ctx context.Context, transaction pgx.Tx, sessionID string, now time.Time) (bool, error) {
	result, err := transaction.Exec(ctx, `
		UPDATE identity.sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, sessionID, now)
	if err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE identity.refresh_tokens
		SET status = 'REVOKED', consumed_at = COALESCE(consumed_at, $2)
		WHERE session_id = $1 AND status = 'ACTIVE'`, sessionID, now); err != nil {
		return false, fmt.Errorf("revoke refresh family: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func insertSecurityEvent(
	ctx context.Context,
	transaction pgx.Tx,
	eventType, identityID, sessionID, deviceReference, purpose, outcome string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
		INSERT INTO identity.security_events
			(event_type, identity_id, session_id, device_reference, purpose, outcome, occurred_at)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7)`,
		eventType, identityID, sessionID, deviceReference, purpose, outcome, now); err != nil {
		return fmt.Errorf("record identity security event: %w", err)
	}
	return nil
}
