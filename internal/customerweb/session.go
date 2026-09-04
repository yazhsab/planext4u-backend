package customerweb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/identity"
)

type memoryRecord struct {
	session Session
}

type MemorySessionStore struct {
	mu            sync.RWMutex
	sessions      map[string]memoryRecord
	refreshLeases map[string]time.Time
	clock         func() time.Time
	random        func([]byte) error
}

func NewMemorySessionStore(clock func() time.Time) (*MemorySessionStore, error) {
	if clock == nil {
		return nil, ErrInvalidConfiguration
	}
	return &MemorySessionStore{
		sessions: map[string]memoryRecord{}, refreshLeases: map[string]time.Time{}, clock: clock,
		random: func(value []byte) error { _, err := rand.Read(value); return err },
	}, nil
}

func (store *MemorySessionStore) Create(_ context.Context, session Session) (string, error) {
	if !validSession(session) {
		return "", ErrInvalidRequest
	}
	token, err := opaqueToken(store.random)
	if err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.sessions {
		if record.session.ID == session.ID {
			return "", ErrSessionExists
		}
	}
	store.sessions[tokenDigest(token)] = memoryRecord{session: cloneSession(session)}
	return token, nil
}

func (store *MemorySessionStore) Resolve(_ context.Context, token string) (Session, error) {
	if !validOpaqueToken(token) {
		return Session{}, ErrAuthenticationRequired
	}
	store.mu.RLock()
	record, found := store.sessions[tokenDigest(token)]
	store.mu.RUnlock()
	if !found || !store.clock().UTC().Before(record.session.ExpiresAt) {
		return Session{}, ErrAuthenticationRequired
	}
	return cloneSession(record.session), nil
}

func (store *MemorySessionStore) Replace(_ context.Context, oldToken string, session Session) (string, error) {
	if !validOpaqueToken(oldToken) || !validSession(session) {
		return "", ErrInvalidRequest
	}
	token, err := opaqueToken(store.random)
	if err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	oldDigest := tokenDigest(oldToken)
	if _, found := store.sessions[oldDigest]; !found {
		return "", ErrSessionNotFound
	}
	delete(store.sessions, oldDigest)
	delete(store.refreshLeases, oldDigest)
	store.sessions[tokenDigest(token)] = memoryRecord{session: cloneSession(session)}
	return token, nil
}

func (store *MemorySessionStore) Delete(_ context.Context, token string) error {
	if !validOpaqueToken(token) {
		return ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	digest := tokenDigest(token)
	if _, found := store.sessions[digest]; !found {
		return ErrSessionNotFound
	}
	delete(store.sessions, digest)
	delete(store.refreshLeases, digest)
	return nil
}

func (store *MemorySessionStore) AcquireRefresh(_ context.Context, token string, ttl time.Duration) error {
	if !validOpaqueToken(token) || ttl < time.Second || ttl > time.Minute {
		return ErrInvalidRequest
	}
	digest := tokenDigest(token)
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.sessions[digest]; !found {
		return ErrSessionNotFound
	}
	if expiresAt, found := store.refreshLeases[digest]; found && store.clock().UTC().Before(expiresAt) {
		return ErrRefreshInProgress
	}
	store.refreshLeases[digest] = store.clock().UTC().Add(ttl)
	return nil
}

func (store *MemorySessionStore) ReleaseRefresh(_ context.Context, token string) error {
	if !validOpaqueToken(token) {
		return ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.refreshLeases, tokenDigest(token))
	return nil
}

func opaqueToken(random func([]byte) error) (string, error) {
	value := make([]byte, 32)
	if err := random(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validOpaqueToken(value string) bool { return len(value) >= 40 && len(value) <= 256 }

func cloneSession(session Session) Session {
	session.Roles = append([]identity.Role(nil), session.Roles...)
	return session
}
