package adminshell

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
)

const sessionCookieName = "__Host-p4u_admin"

var (
	ErrAuthenticationRequired = errors.New("administrator authentication required")
	ErrForbidden              = errors.New("administrator operation forbidden")
	ErrInvalidRequest         = errors.New("administrator request invalid")
	ErrSessionNotFound        = errors.New("administrator session not found")
)

type SessionResolver interface {
	Resolve(*http.Request) (ResolvedSession, error)
	SetCountry(context.Context, string, string) error
}

type sessionRecord struct {
	principal Principal
	csrfToken string
	expiresAt time.Time
}

type MemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]sessionRecord
	clock    func() time.Time
	random   func([]byte) error
}

func NewMemorySessionStore(clock func() time.Time) (*MemorySessionStore, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	return &MemorySessionStore{
		sessions: map[string]sessionRecord{},
		clock:    clock,
		random: func(value []byte) error {
			_, err := rand.Read(value)
			return err
		},
	}, nil
}

func (store *MemorySessionStore) Issue(principal Principal, ttl time.Duration) (string, error) {
	if ttl < time.Minute || ttl > 24*time.Hour || !validPrincipal(principal) {
		return "", ErrInvalidRequest
	}
	token, err := randomToken(store.random, 32)
	if err != nil {
		return "", err
	}
	csrfToken, err := randomToken(store.random, 24)
	if err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sessions[tokenDigest(token)] = sessionRecord{
		principal: clonePrincipal(principal),
		csrfToken: csrfToken,
		expiresAt: store.clock().UTC().Add(ttl),
	}
	return token, nil
}

func (store *MemorySessionStore) Resolve(request *http.Request) (ResolvedSession, error) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || len(cookie.Value) < 32 || len(cookie.Value) > 256 {
		return ResolvedSession{}, ErrAuthenticationRequired
	}
	store.mu.RLock()
	record, exists := store.sessions[tokenDigest(cookie.Value)]
	store.mu.RUnlock()
	if !exists || !store.clock().UTC().Before(record.expiresAt) {
		return ResolvedSession{}, ErrAuthenticationRequired
	}
	return ResolvedSession{Principal: clonePrincipal(record.principal), CSRFToken: record.csrfToken}, nil
}

func (store *MemorySessionStore) SetCountry(_ context.Context, sessionID, country string) error {
	if !safeIdentifier(sessionID) || !validCountry(country) {
		return ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for digest, record := range store.sessions {
		if record.principal.SessionID != sessionID || !store.clock().UTC().Before(record.expiresAt) {
			continue
		}
		if !contains(record.principal.AllowedCountries, country) {
			return ErrForbidden
		}
		record.principal.SelectedCountry = country
		store.sessions[digest] = record
		return nil
	}
	return ErrSessionNotFound
}

func SetSessionCookie(writer http.ResponseWriter, token string, expiresAt time.Time) error {
	if len(token) < 32 || len(token) > 256 || expiresAt.IsZero() {
		return ErrInvalidRequest
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt.UTC(),
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func ClearSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func capabilities(roles []Role) map[string]bool {
	result := map[string]bool{}
	for _, role := range roles {
		result[CapabilityShellRead] = true
		switch role {
		case RoleSuperAdmin:
			result[CapabilityAuditRead] = true
			grantAllOperations(result)
			result[CapabilityContentManage] = true
			result[CapabilitySupportManage] = true
			result[CapabilityConfigManage] = true
			result[CapabilityGovernanceRead] = true
		case RoleCountryAdmin:
			result[CapabilityAuditRead] = true
			grantAllOperations(result)
			result[CapabilityContentManage] = true
			result[CapabilitySupportManage] = true
			result[CapabilityConfigManage] = true
			result[CapabilityGovernanceRead] = true
		case RoleContentAdmin:
			result[CapabilityOperationsRead] = true
			result[adminops.CapabilityCatalog] = true
			result[adminops.CapabilityCampaign] = true
			result[CapabilityContentManage] = true
			result[CapabilityConfigManage] = true
			result[CapabilityGovernanceRead] = true
			result[adminops.CapabilityPolicy] = true
			result[adminops.CapabilityIntelligence] = true
		case RoleSupportAdmin:
			result[CapabilityOperationsRead] = true
			result[adminops.CapabilitySupport] = true
			result[CapabilitySupportManage] = true
		case RoleAuditor:
			result[CapabilityAuditRead] = true
			result[CapabilityOperationsRead] = true
			result[adminops.CapabilityReporting] = true
			result[CapabilityGovernanceRead] = true
		}
	}
	return result
}

func grantAllOperations(result map[string]bool) {
	result[CapabilityOperationsRead] = true
	for _, capability := range []string{
		adminops.CapabilityCatalog,
		adminops.CapabilityOrder,
		adminops.CapabilityPayment,
		adminops.CapabilityWallet,
		adminops.CapabilityCampaign,
		adminops.CapabilityCMS,
		adminops.CapabilitySupport,
		adminops.CapabilityReporting,
		adminops.CapabilitySupply,
		adminops.CapabilityRestaurant,
		adminops.CapabilityDispatch,
		adminops.CapabilitySettlement,
		adminops.CapabilityFranchise,
		adminops.CapabilityContent,
		adminops.CapabilityPolicy,
		adminops.CapabilityCountry,
		adminops.CapabilityEmergency,
		adminops.CapabilityIntelligence,
	} {
		result[capability] = true
	}
}

func capabilityList(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value, allowed := range values {
		if allowed {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func validPrincipal(principal Principal) bool {
	if !safeIdentifier(principal.SubjectID) || !safeIdentifier(principal.SessionID) || !safeIdentifier(principal.TenantID) ||
		strings.TrimSpace(principal.DisplayName) == "" || len(principal.DisplayName) > 128 || len(principal.Roles) == 0 ||
		len(principal.Roles) > 8 || len(principal.AllowedCountries) == 0 || len(principal.AllowedCountries) > 32 ||
		!contains(principal.AllowedCountries, principal.SelectedCountry) || principal.AuthenticatedAt.IsZero() {
		return false
	}
	for _, role := range principal.Roles {
		if !validRole(role) {
			return false
		}
	}
	for _, country := range principal.AllowedCountries {
		if !validCountry(country) {
			return false
		}
	}
	return true
}

func validRole(role Role) bool {
	switch role {
	case RoleSuperAdmin, RoleCountryAdmin, RoleContentAdmin, RoleSupportAdmin, RoleAuditor:
		return true
	default:
		return false
	}
}

func clonePrincipal(principal Principal) Principal {
	principal.Roles = append([]Role(nil), principal.Roles...)
	principal.AllowedCountries = append([]string(nil), principal.AllowedCountries...)
	principal.AuthMethods = append([]string(nil), principal.AuthMethods...)
	return principal
}

func randomToken(randomSource func([]byte) error, size int) (string, error) {
	value := make([]byte, size)
	if err := randomSource(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}

func validCountry(country string) bool {
	return len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z'
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
