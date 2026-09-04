package customerweb

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/identity"
)

const customerSessionTimeout = 5 * time.Second

type PostgresSessionStore struct {
	pool   *pgxpool.Pool
	clock  func() time.Time
	random func([]byte) error
	cipher *TokenCipher
}

func NewPostgresSessionStore(pool *pgxpool.Pool, clock func() time.Time, tokenCipher *TokenCipher) (*PostgresSessionStore, error) {
	if pool == nil || clock == nil || tokenCipher == nil {
		return nil, ErrInvalidConfiguration
	}
	return &PostgresSessionStore{pool: pool, clock: clock, cipher: tokenCipher, random: func(value []byte) error { _, err := rand.Read(value); return err }}, nil
}

func (store *PostgresSessionStore) Ready(ctx context.Context) error {
	var ready bool
	if err := store.pool.QueryRow(ctx, `SELECT to_regclass('customer_web.sessions') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check customer web session readiness: %w", err)
	}
	if !ready {
		return errors.New("customer web session schema is unavailable")
	}
	return nil
}

func (store *PostgresSessionStore) Create(ctx context.Context, session Session) (string, error) {
	if !validSession(session) {
		return "", ErrInvalidRequest
	}
	token, err := opaqueToken(store.random)
	if err != nil {
		return "", err
	}
	if err := store.insert(ctx, nil, token, session); err != nil {
		return "", err
	}
	return token, nil
}

func (store *PostgresSessionStore) Resolve(ctx context.Context, token string) (Session, error) {
	if !validOpaqueToken(token) {
		return Session{}, ErrAuthenticationRequired
	}
	ctx, cancel := context.WithTimeout(ctx, customerSessionTimeout)
	defer cancel()
	var session Session
	var roles []string
	var accessCiphertext []byte
	var refreshCiphertext []byte
	var refreshExpiresAt *time.Time
	err := store.pool.QueryRow(ctx, `
		SELECT session_id::text,platform_session_id,identity_id,tenant_id,country,display_name,roles,is_guest,
		       access_token,access_expires_at,refresh_token,refresh_expires_at,csrf_token,expires_at,created_at,updated_at
		FROM customer_web.sessions WHERE token_digest=$1 AND expires_at>$2`, tokenDigest(token), store.clock().UTC()).Scan(
		&session.ID, &session.PlatformSession, &session.IdentityID, &session.TenantID, &session.Country, &session.DisplayName, &roles, &session.Guest,
		&accessCiphertext, &session.AccessExpiresAt, &refreshCiphertext, &refreshExpiresAt, &session.CSRFToken,
		&session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrAuthenticationRequired
	}
	if err != nil {
		return Session{}, fmt.Errorf("resolve customer web session: %w", err)
	}
	session.Roles = make([]identity.Role, len(roles))
	for index, role := range roles {
		session.Roles[index] = identity.Role(role)
	}
	session.AccessToken, err = store.cipher.Decrypt(accessCiphertext, session.ID+":access")
	if err != nil {
		return Session{}, ErrAuthenticationRequired
	}
	if len(refreshCiphertext) > 0 {
		session.RefreshToken, err = store.cipher.Decrypt(refreshCiphertext, session.ID+":refresh")
		if err != nil || refreshExpiresAt == nil {
			return Session{}, ErrAuthenticationRequired
		}
		session.RefreshExpiresAt = refreshExpiresAt.UTC()
	}
	if !validSession(session) {
		return Session{}, ErrAuthenticationRequired
	}
	return session, nil
}

func (store *PostgresSessionStore) Replace(ctx context.Context, oldToken string, session Session) (string, error) {
	if !validOpaqueToken(oldToken) || !validSession(session) {
		return "", ErrInvalidRequest
	}
	newToken, err := opaqueToken(store.random)
	if err != nil {
		return "", err
	}
	if err := store.insert(ctx, &oldToken, newToken, session); err != nil {
		return "", err
	}
	return newToken, nil
}

func (store *PostgresSessionStore) insert(ctx context.Context, oldToken *string, newToken string, session Session) error {
	ctx, cancel := context.WithTimeout(ctx, customerSessionTimeout)
	defer cancel()
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin customer web session transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	if oldToken != nil {
		command, deleteErr := transaction.Exec(ctx, `DELETE FROM customer_web.sessions WHERE token_digest=$1`, tokenDigest(*oldToken))
		if deleteErr != nil {
			return fmt.Errorf("replace customer web session: %w", deleteErr)
		}
		if command.RowsAffected() != 1 {
			return ErrSessionNotFound
		}
	}
	roles := make([]string, len(session.Roles))
	for index, role := range session.Roles {
		roles[index] = string(role)
	}
	accessCiphertext, err := store.cipher.Encrypt(session.AccessToken, session.ID+":access")
	if err != nil {
		return fmt.Errorf("encrypt customer web access token: %w", err)
	}
	var refreshCiphertext []byte
	if session.RefreshToken != "" {
		refreshCiphertext, err = store.cipher.Encrypt(session.RefreshToken, session.ID+":refresh")
		if err != nil {
			return fmt.Errorf("encrypt customer web refresh token: %w", err)
		}
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO customer_web.sessions
		(token_digest,session_id,platform_session_id,identity_id,tenant_id,country,display_name,roles,is_guest,
		 access_token,access_expires_at,refresh_token,refresh_expires_at,csrf_token,expires_at,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		tokenDigest(newToken), session.ID, session.PlatformSession, session.IdentityID, session.TenantID, session.Country,
		session.DisplayName, roles, session.Guest, accessCiphertext, session.AccessExpiresAt,
		refreshCiphertext, nullableTime(session.RefreshExpiresAt), session.CSRFToken, session.ExpiresAt, session.CreatedAt, session.UpdatedAt)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" {
			return ErrSessionExists
		}
		return fmt.Errorf("write customer web session: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit customer web session: %w", err)
	}
	return nil
}

func (store *PostgresSessionStore) Delete(ctx context.Context, token string) error {
	if !validOpaqueToken(token) {
		return ErrInvalidRequest
	}
	command, err := store.pool.Exec(ctx, `DELETE FROM customer_web.sessions WHERE token_digest=$1`, tokenDigest(token))
	if err != nil {
		return fmt.Errorf("delete customer web session: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrSessionNotFound
	}
	return nil
}

func (store *PostgresSessionStore) AcquireRefresh(ctx context.Context, token string, ttl time.Duration) error {
	if !validOpaqueToken(token) || ttl < time.Second || ttl > time.Minute {
		return ErrInvalidRequest
	}
	now := store.clock().UTC()
	command, err := store.pool.Exec(ctx, `UPDATE customer_web.sessions SET refresh_lease_until=$2
		WHERE token_digest=$1 AND expires_at>$3 AND (refresh_lease_until IS NULL OR refresh_lease_until<=$3)`, tokenDigest(token), now.Add(ttl), now)
	if err != nil {
		return fmt.Errorf("acquire customer web refresh lease: %w", err)
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customer_web.sessions WHERE token_digest=$1 AND expires_at>$2)`, tokenDigest(token), now).Scan(&exists); err != nil {
		return fmt.Errorf("check customer web refresh lease: %w", err)
	}
	if exists {
		return ErrRefreshInProgress
	}
	return ErrSessionNotFound
}

func (store *PostgresSessionStore) ReleaseRefresh(ctx context.Context, token string) error {
	if !validOpaqueToken(token) {
		return ErrInvalidRequest
	}
	if _, err := store.pool.Exec(ctx, `UPDATE customer_web.sessions SET refresh_lease_until=NULL WHERE token_digest=$1`, tokenDigest(token)); err != nil {
		return fmt.Errorf("release customer web refresh lease: %w", err)
	}
	return nil
}

func (store *PostgresSessionStore) PurgeExpired(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 10000 {
		return 0, ErrInvalidRequest
	}
	command, err := store.pool.Exec(ctx, `DELETE FROM customer_web.sessions WHERE token_digest IN (
		SELECT token_digest FROM customer_web.sessions WHERE expires_at <= $1 ORDER BY expires_at LIMIT $2)`, store.clock().UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("purge customer web sessions: %w", err)
	}
	return command.RowsAffected(), nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
