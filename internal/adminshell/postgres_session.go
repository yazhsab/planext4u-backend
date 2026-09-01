package adminshell

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminSessionTimeout = 5 * time.Second

type PostgresSessionStore struct {
	pool   *pgxpool.Pool
	clock  func() time.Time
	random func([]byte) error
}

func NewPostgresSessionStore(pool *pgxpool.Pool, clock func() time.Time) (*PostgresSessionStore, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresSessionStore{pool: pool, clock: clock, random: func(value []byte) error { _, err := rand.Read(value); return err }}, nil
}

func (store *PostgresSessionStore) Ready(ctx context.Context) error {
	var ready bool
	if err := store.pool.QueryRow(ctx, `SELECT to_regclass('admin.sessions') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check admin session readiness: %w", err)
	}
	if !ready {
		return errors.New("admin session schema is unavailable")
	}
	return nil
}

func (store *PostgresSessionStore) Issue(principal Principal, ttl time.Duration) (string, error) {
	if ttl < time.Minute || ttl > 24*time.Hour || !validPrincipal(principal) || !canonicalUUID(principal.SubjectID) || !canonicalUUID(principal.SessionID) || !canonicalUUID(principal.TenantID) {
		return "", ErrInvalidRequest
	}
	token, err := randomToken(store.random, 32)
	if err != nil {
		return "", err
	}
	csrf, err := randomToken(store.random, 24)
	if err != nil {
		return "", err
	}
	roles := make([]string, len(principal.Roles))
	for index, role := range principal.Roles {
		roles[index] = string(role)
	}
	now := store.clock().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), adminSessionTimeout)
	defer cancel()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO admin.sessions
		(token_digest,session_id,subject_id,tenant_id,display_name,roles,allowed_countries,selected_country,authenticated_at,auth_methods,csrf_token,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		tokenDigest(token), principal.SessionID, principal.SubjectID, principal.TenantID, principal.DisplayName, roles,
		principal.AllowedCountries, principal.SelectedCountry, principal.AuthenticatedAt, principal.AuthMethods, csrf, now.Add(ttl), now); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" {
			return "", ErrSessionExists
		}
		return "", fmt.Errorf("issue admin session: %w", err)
	}
	return token, nil
}

func (store *PostgresSessionStore) Resolve(request *http.Request) (ResolvedSession, error) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || len(cookie.Value) < 32 || len(cookie.Value) > 256 {
		return ResolvedSession{}, ErrAuthenticationRequired
	}
	ctx, cancel := context.WithTimeout(request.Context(), adminSessionTimeout)
	defer cancel()
	var principal Principal
	var roles []string
	var csrf string
	err = store.pool.QueryRow(ctx, `
		SELECT subject_id::text,session_id::text,tenant_id::text,display_name,roles,allowed_countries,
		       selected_country,authenticated_at,auth_methods,csrf_token
		FROM admin.sessions WHERE token_digest=$1 AND expires_at>$2`, tokenDigest(cookie.Value), store.clock().UTC()).Scan(
		&principal.SubjectID, &principal.SessionID, &principal.TenantID, &principal.DisplayName, &roles, &principal.AllowedCountries,
		&principal.SelectedCountry, &principal.AuthenticatedAt, &principal.AuthMethods, &csrf)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedSession{}, ErrAuthenticationRequired
	}
	if err != nil {
		return ResolvedSession{}, fmt.Errorf("resolve admin session: %w", err)
	}
	principal.Roles = make([]Role, len(roles))
	for index, role := range roles {
		principal.Roles[index] = Role(role)
	}
	if !validPrincipal(principal) {
		return ResolvedSession{}, ErrAuthenticationRequired
	}
	return ResolvedSession{Principal: principal, CSRFToken: csrf}, nil
}

func (store *PostgresSessionStore) SetCountry(ctx context.Context, sessionID, country string) error {
	if !canonicalUUID(sessionID) || !validCountry(country) {
		return ErrInvalidRequest
	}
	command, err := store.pool.Exec(ctx, `
		UPDATE admin.sessions SET selected_country=$2
		WHERE session_id=$1 AND expires_at>$3 AND $2=ANY(allowed_countries)`, sessionID, country, store.clock().UTC())
	if err != nil {
		return fmt.Errorf("update admin session country: %w", err)
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.sessions WHERE session_id=$1 AND expires_at>$2)`, sessionID, store.clock().UTC()).Scan(&exists); err != nil {
		return fmt.Errorf("check admin session: %w", err)
	}
	if exists {
		return ErrForbidden
	}
	return ErrSessionNotFound
}

func (store *PostgresSessionStore) Revoke(ctx context.Context, sessionID string) error {
	if !canonicalUUID(sessionID) {
		return ErrInvalidRequest
	}
	command, err := store.pool.Exec(ctx, `DELETE FROM admin.sessions WHERE session_id=$1`, sessionID)
	if err != nil {
		return fmt.Errorf("revoke admin session: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrSessionNotFound
	}
	return nil
}

func (store *PostgresSessionStore) PurgeExpired(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 10000 {
		return 0, ErrInvalidRequest
	}
	command, err := store.pool.Exec(ctx, `
		DELETE FROM admin.sessions WHERE token_digest IN (
			SELECT token_digest FROM admin.sessions WHERE expires_at <= $1 ORDER BY expires_at LIMIT $2
		)`, store.clock().UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("purge expired admin sessions: %w", err)
	}
	return command.RowsAffected(), nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
