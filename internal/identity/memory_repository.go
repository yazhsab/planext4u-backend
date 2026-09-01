package identity

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryRepository is a deterministic development and test adapter. Its
// transaction-sized critical sections model the atomicity required from the
// PostgreSQL adapter introduced with service-owned migrations.
type MemoryRepository struct {
	mu                 sync.RWMutex
	identities         map[string]Identity
	identityByProvider map[string]string
	sessions           map[string]Session
	activeRefresh      map[string]string
	consumedRefresh    map[string]string
	consents           map[string][]Consent
	deletions          map[string]DeletionRequest
	auditEvents        []AuditEvent
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		identities:         make(map[string]Identity),
		identityByProvider: make(map[string]string),
		sessions:           make(map[string]Session),
		activeRefresh:      make(map[string]string),
		consumedRefresh:    make(map[string]string),
		consents:           make(map[string][]Consent),
		deletions:          make(map[string]DeletionRequest),
	}
}

func (repository *MemoryRepository) StartSession(_ context.Context, params StartSessionParams) (Identity, Session, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	providerKey := params.TenantID + "\x00" + params.Provider.Provider + "\x00" + params.Provider.Subject
	identityID, exists := repository.identityByProvider[providerKey]
	var account Identity
	if exists {
		account = repository.identities[identityID]
		if account.DisabledAt != nil {
			return Identity{}, Session{}, ErrForbidden
		}
	} else {
		account = Identity{
			ID:              params.IdentityID,
			TenantID:        params.TenantID,
			Provider:        params.Provider.Provider,
			ProviderSubject: params.Provider.Subject,
			Roles:           []Role{RoleCustomer},
			Profile: Profile{
				Locale:    "en",
				TimeZone:  "Asia/Kolkata",
				Version:   1,
				UpdatedAt: params.Now,
			},
			CreatedAt: params.Now,
		}
		repository.identities[account.ID] = cloneIdentity(account)
		repository.identityByProvider[providerKey] = account.ID
		repository.appendAudit(AuditEvent{
			ID:         params.IdentityID + "-created",
			Type:       "IDENTITY_CREATED",
			IdentityID: account.ID,
			Outcome:    "SUCCEEDED",
			OccurredAt: params.Now,
		})
	}
	if _, duplicate := repository.sessions[params.SessionID]; duplicate {
		return Identity{}, Session{}, ErrInvalidInput
	}
	if _, duplicate := repository.activeRefresh[params.RefreshDigest]; duplicate {
		return Identity{}, Session{}, ErrInvalidInput
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
	repository.sessions[session.ID] = session
	repository.activeRefresh[params.RefreshDigest] = session.ID
	repository.appendAudit(AuditEvent{
		ID:         params.SessionID + "-authenticated",
		Type:       "SESSION_AUTHENTICATED",
		IdentityID: account.ID,
		SessionID:  session.ID,
		DeviceRef:  session.DeviceReference,
		Outcome:    "SUCCEEDED",
		OccurredAt: params.Now,
	})
	return cloneIdentity(account), cloneSession(session), nil
}

func (repository *MemoryRepository) RotateSession(_ context.Context, params RotateSessionParams) (Identity, Session, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	if reusedSessionID, reused := repository.consumedRefresh[params.CurrentDigest]; reused {
		repository.revokeLocked(reusedSessionID, params.Now, "REFRESH_REUSE_DETECTED")
		return Identity{}, Session{}, ErrRefreshReuse
	}
	sessionID, exists := repository.activeRefresh[params.CurrentDigest]
	if !exists {
		return Identity{}, Session{}, ErrRefreshInvalid
	}
	session, exists := repository.sessions[sessionID]
	if !exists || session.RefreshDigest != params.CurrentDigest || session.RevokedAt != nil {
		return Identity{}, Session{}, ErrRefreshInvalid
	}
	if !params.Now.Before(session.RefreshExpires) {
		repository.revokeLocked(sessionID, params.Now, "REFRESH_EXPIRED")
		return Identity{}, Session{}, ErrRefreshExpired
	}
	if _, duplicate := repository.activeRefresh[params.NextDigest]; duplicate {
		return Identity{}, Session{}, ErrInvalidInput
	}
	if _, duplicate := repository.consumedRefresh[params.NextDigest]; duplicate {
		return Identity{}, Session{}, ErrInvalidInput
	}
	delete(repository.activeRefresh, params.CurrentDigest)
	repository.consumedRefresh[params.CurrentDigest] = session.ID
	repository.activeRefresh[params.NextDigest] = session.ID
	session.RefreshDigest = params.NextDigest
	session.LastSeenAt = params.Now
	session.Rotation++
	repository.sessions[session.ID] = session
	account, exists := repository.identities[session.IdentityID]
	if !exists || account.DisabledAt != nil {
		repository.revokeLocked(session.ID, params.Now, "IDENTITY_DISABLED")
		return Identity{}, Session{}, ErrForbidden
	}
	repository.appendAudit(AuditEvent{
		ID:         session.ID + "-refresh-" + stringID(session.Rotation),
		Type:       "SESSION_REFRESHED",
		IdentityID: account.ID,
		SessionID:  session.ID,
		DeviceRef:  session.DeviceReference,
		Outcome:    "SUCCEEDED",
		OccurredAt: params.Now,
	})
	return cloneIdentity(account), cloneSession(session), nil
}

func (repository *MemoryRepository) RevokeByRefresh(_ context.Context, digest string, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if sessionID, exists := repository.activeRefresh[digest]; exists {
		repository.revokeLocked(sessionID, now, "SESSION_REVOKED")
		return nil
	}
	if sessionID, exists := repository.consumedRefresh[digest]; exists {
		repository.revokeLocked(sessionID, now, "SESSION_REVOKED")
	}
	// Unknown tokens are intentionally indistinguishable from an idempotent
	// revoke so this endpoint cannot be used as a token oracle.
	return nil
}

func (repository *MemoryRepository) Authenticate(_ context.Context, trusted TrustedIdentity, now time.Time) (Identity, Session, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	session, exists := repository.sessions[trusted.Session]
	if !exists || session.RevokedAt != nil || !now.Before(session.RefreshExpires) {
		return Identity{}, Session{}, ErrSessionInvalid
	}
	account, exists := repository.identities[session.IdentityID]
	if !exists || account.DisabledAt != nil ||
		account.ID != trusted.Subject ||
		account.TenantID != trusted.TenantID ||
		session.TenantID != trusted.TenantID ||
		session.Country != trusted.Country {
		return Identity{}, Session{}, ErrForbidden
	}
	return cloneIdentity(account), cloneSession(session), nil
}

func (repository *MemoryRepository) ListSessions(_ context.Context, identityID string) ([]Session, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if _, exists := repository.identities[identityID]; !exists {
		return nil, ErrNotFound
	}
	result := make([]Session, 0)
	for _, session := range repository.sessions {
		if session.IdentityID == identityID {
			result = append(result, cloneSession(session))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].AuthenticatedAt.After(result[right].AuthenticatedAt)
	})
	return result, nil
}

func (repository *MemoryRepository) RevokeSession(_ context.Context, identityID, sessionID string, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	session, exists := repository.sessions[sessionID]
	if !exists || session.IdentityID != identityID {
		return ErrNotFound
	}
	repository.revokeLocked(sessionID, now, "SESSION_REVOKED")
	return nil
}

func (repository *MemoryRepository) UpdateProfile(_ context.Context, identityID string, update ProfileUpdate, now time.Time) (Profile, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	account, exists := repository.identities[identityID]
	if !exists {
		return Profile{}, ErrNotFound
	}
	if account.Profile.Version != update.Version {
		return Profile{}, ErrVersionConflict
	}
	account.Profile = Profile{
		DisplayName: update.DisplayName,
		Email:       update.Email,
		Phone:       update.Phone,
		Locale:      update.Locale,
		TimeZone:    update.TimeZone,
		Version:     account.Profile.Version + 1,
		UpdatedAt:   now,
	}
	repository.identities[identityID] = cloneIdentity(account)
	repository.appendAudit(AuditEvent{
		ID:         identityID + "-profile-" + stringID(account.Profile.Version),
		Type:       "PROFILE_UPDATED",
		IdentityID: identityID,
		Outcome:    "SUCCEEDED",
		OccurredAt: now,
	})
	return account.Profile, nil
}

func (repository *MemoryRepository) ListConsents(_ context.Context, identityID string) ([]Consent, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if _, exists := repository.identities[identityID]; !exists {
		return nil, ErrNotFound
	}
	history := repository.consents[identityID]
	current := make(map[ConsentPurpose]Consent)
	for _, consent := range history {
		if latest, exists := current[consent.Purpose]; !exists || latest.Version < consent.Version {
			current[consent.Purpose] = consent
		}
	}
	result := make([]Consent, 0, len(current))
	for _, consent := range current {
		result = append(result, consent)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Purpose < result[right].Purpose })
	return result, nil
}

func (repository *MemoryRepository) RecordConsent(_ context.Context, identityID string, consent Consent, _ time.Time) (Consent, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.identities[identityID]; !exists {
		return Consent{}, ErrNotFound
	}
	history := repository.consents[identityID]
	var version int64 = 1
	for _, existing := range history {
		if existing.Purpose == consent.Purpose && existing.Version >= version {
			version = existing.Version + 1
		}
	}
	consent.Version = version
	repository.consents[identityID] = append(history, consent)
	repository.appendAudit(AuditEvent{
		ID:            consent.EvidenceID,
		Type:          "CONSENT_CHANGED",
		IdentityID:    identityID,
		Purpose:       consent.Purpose,
		Outcome:       consentOutcome(consent.Granted),
		OccurredAt:    consent.RecordedAt,
		SecurityEvent: true,
	})
	return consent, nil
}

func (repository *MemoryRepository) CreateDeletionRequest(_ context.Context, request DeletionRequest) (DeletionRequest, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.identities[request.IdentityID]; !exists {
		return DeletionRequest{}, ErrNotFound
	}
	if existing, exists := repository.deletions[request.IdentityID]; exists && existing.Status == "SCHEDULED" {
		return existing, nil
	}
	repository.deletions[request.IdentityID] = request
	repository.appendAudit(AuditEvent{ID: request.ID, Type: "ACCOUNT_DELETION_REQUESTED", IdentityID: request.IdentityID, Outcome: "SCHEDULED", OccurredAt: request.RequestedAt, SecurityEvent: true})
	return request, nil
}

func (repository *MemoryRepository) AuditEvents() []AuditEvent {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return append([]AuditEvent(nil), repository.auditEvents...)
}

// ProvisionRole models an out-of-band, audited supply/admin provisioning flow.
// Public sign-in never accepts a requested role.
func (repository *MemoryRepository) ProvisionRole(identityID string, role Role, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	account, exists := repository.identities[identityID]
	if !exists {
		return ErrNotFound
	}
	for _, existing := range account.Roles {
		if existing == role {
			return nil
		}
	}
	account.Roles = append(account.Roles, role)
	sort.Slice(account.Roles, func(left, right int) bool { return account.Roles[left] < account.Roles[right] })
	repository.identities[identityID] = cloneIdentity(account)
	repository.appendAudit(AuditEvent{
		ID:            identityID + "-role-" + string(role),
		Type:          "ROLE_GRANTED",
		IdentityID:    identityID,
		Outcome:       "SUCCEEDED",
		OccurredAt:    now,
		SecurityEvent: true,
	})
	return nil
}

func (repository *MemoryRepository) revokeLocked(sessionID string, now time.Time, eventType string) {
	session, exists := repository.sessions[sessionID]
	if !exists {
		return
	}
	if session.RevokedAt != nil {
		return
	}
	revokedAt := now
	session.RevokedAt = &revokedAt
	repository.sessions[sessionID] = session
	delete(repository.activeRefresh, session.RefreshDigest)
	repository.appendAudit(AuditEvent{
		ID:            session.ID + "-" + eventType,
		Type:          eventType,
		IdentityID:    session.IdentityID,
		SessionID:     session.ID,
		DeviceRef:     session.DeviceReference,
		Outcome:       "SUCCEEDED",
		OccurredAt:    now,
		SecurityEvent: true,
	})
}

func (repository *MemoryRepository) appendAudit(event AuditEvent) {
	repository.auditEvents = append(repository.auditEvents, event)
}

func cloneIdentity(value Identity) Identity {
	value.Roles = append([]Role(nil), value.Roles...)
	if value.DisabledAt != nil {
		disabledAt := *value.DisabledAt
		value.DisabledAt = &disabledAt
	}
	return value
}

func cloneSession(value Session) Session {
	if value.RevokedAt != nil {
		revokedAt := *value.RevokedAt
		value.RevokedAt = &revokedAt
	}
	return value
}

func consentOutcome(granted bool) string {
	if granted {
		return "GRANTED"
	}
	return "WITHDRAWN"
}

func stringID(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
