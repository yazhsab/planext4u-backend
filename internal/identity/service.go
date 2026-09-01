package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type IDFactory func(string) (string, error)

type ServiceConfig struct {
	TenantID         string
	AllowedCountries []string
	SessionTTL       time.Duration
	Repository       Repository
	ProviderVerifier ProviderVerifier
	TokenIssuer      AccessTokenIssuer
	RefreshFactory   RefreshTokenFactory
	RefreshHasher    RefreshTokenHasher
	Now              func() time.Time
	IDFactory        IDFactory
}

type Service struct {
	tenantID         string
	allowedCountries map[string]struct{}
	sessionTTL       time.Duration
	repository       Repository
	providerVerifier ProviderVerifier
	tokenIssuer      AccessTokenIssuer
	refreshFactory   RefreshTokenFactory
	refreshHasher    RefreshTokenHasher
	now              func() time.Time
	newID            IDFactory
}

func NewService(config ServiceConfig) (*Service, error) {
	if !validSafeIdentifier(config.TenantID, 128) ||
		len(config.AllowedCountries) == 0 ||
		config.SessionTTL < time.Hour || config.SessionTTL > 90*24*time.Hour ||
		config.Repository == nil || config.ProviderVerifier == nil ||
		config.TokenIssuer == nil || config.RefreshFactory == nil ||
		config.RefreshHasher == nil {
		return nil, ErrInvalidConfiguration
	}
	allowedCountries := make(map[string]struct{}, len(config.AllowedCountries))
	for _, country := range config.AllowedCountries {
		if !validCountry(country) {
			return nil, ErrInvalidConfiguration
		}
		allowedCountries[country] = struct{}{}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.IDFactory == nil {
		config.IDFactory = func(_ string) (string, error) {
			return uuid.NewString(), nil
		}
	}
	return &Service{
		tenantID:         config.TenantID,
		allowedCountries: allowedCountries,
		sessionTTL:       config.SessionTTL,
		repository:       config.Repository,
		providerVerifier: config.ProviderVerifier,
		tokenIssuer:      config.TokenIssuer,
		refreshFactory:   config.RefreshFactory,
		refreshHasher:    config.RefreshHasher,
		now:              config.Now,
		newID:            config.IDFactory,
	}, nil
}

func (service *Service) Exchange(ctx context.Context, input ExchangeInput) (Authentication, error) {
	input.Provider = strings.TrimSpace(input.Provider)
	input.Country = strings.ToUpper(strings.TrimSpace(input.Country))
	if !validProvider(input.Provider) ||
		!validSafeIdentifier(input.DeviceID, 128) ||
		!validOpaqueToken(input.ProviderToken, 8, 8192) {
		return Authentication{}, ErrInvalidInput
	}
	if _, allowed := service.allowedCountries[input.Country]; !allowed {
		return Authentication{}, ErrInvalidInput
	}
	verified, err := service.providerVerifier.Verify(ctx, input.Provider, input.ProviderToken)
	if err != nil {
		if errors.Is(err, ErrProviderRejected) {
			return Authentication{}, ErrProviderRejected
		}
		return Authentication{}, ErrProviderUnavailable
	}
	refreshToken, digest, err := service.newRefreshToken()
	if err != nil {
		return Authentication{}, err
	}
	deviceReference, err := service.refreshHasher.DeviceReference(input.DeviceID)
	if err != nil {
		return Authentication{}, err
	}
	identityID, err := service.newID("identity")
	if err != nil {
		return Authentication{}, fmt.Errorf("create identity ID: %w", err)
	}
	sessionID, err := service.newID("session")
	if err != nil {
		return Authentication{}, fmt.Errorf("create session ID: %w", err)
	}
	now := service.now().UTC()
	account, session, err := service.repository.StartSession(ctx, StartSessionParams{
		Provider:        verified,
		IdentityID:      identityID,
		SessionID:       sessionID,
		TenantID:        service.tenantID,
		Country:         input.Country,
		DeviceID:        input.DeviceID,
		DeviceReference: deviceReference,
		RefreshDigest:   digest,
		RefreshExpires:  now.Add(service.sessionTTL),
		Now:             now,
	})
	if err != nil {
		return Authentication{}, err
	}
	result, err := service.authentication(account, session, refreshToken)
	if err != nil {
		_ = service.repository.RevokeSession(ctx, account.ID, session.ID, now)
		return Authentication{}, err
	}
	return result, nil
}

func (service *Service) Refresh(ctx context.Context, presentedToken string) (Authentication, error) {
	currentDigest, err := service.refreshHasher.Digest(presentedToken)
	if err != nil {
		return Authentication{}, ErrRefreshInvalid
	}
	nextToken, nextDigest, err := service.newRefreshToken()
	if err != nil {
		return Authentication{}, err
	}
	now := service.now().UTC()
	account, session, err := service.repository.RotateSession(ctx, RotateSessionParams{
		CurrentDigest: currentDigest,
		NextDigest:    nextDigest,
		Now:           now,
	})
	if err != nil {
		return Authentication{}, err
	}
	result, err := service.authentication(account, session, nextToken)
	if err != nil {
		_ = service.repository.RevokeSession(ctx, account.ID, session.ID, now)
		return Authentication{}, err
	}
	return result, nil
}

func (service *Service) Revoke(ctx context.Context, refreshToken string) error {
	digest, err := service.refreshHasher.Digest(refreshToken)
	if err != nil {
		return nil
	}
	return service.repository.RevokeByRefresh(ctx, digest, service.now().UTC())
}

func (service *Service) Principal(ctx context.Context, trusted TrustedIdentity) (Principal, Profile, error) {
	if !validSafeIdentifier(trusted.Subject, 128) ||
		!validSafeIdentifier(trusted.Session, 128) ||
		!validSafeIdentifier(trusted.TenantID, 128) ||
		!validCountry(trusted.Country) {
		return Principal{}, Profile{}, ErrSessionInvalid
	}
	account, session, err := service.repository.Authenticate(ctx, trusted, service.now().UTC())
	if err != nil {
		return Principal{}, Profile{}, err
	}
	return principalFor(account, session), account.Profile, nil
}

func (service *Service) Authorize(ctx context.Context, trusted TrustedIdentity, required Role) (Principal, error) {
	if !validRole(required) {
		return Principal{}, ErrInvalidInput
	}
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return Principal{}, err
	}
	for _, role := range principal.Roles {
		if role == required {
			return principal, nil
		}
	}
	return Principal{}, ErrForbidden
}

func (service *Service) Sessions(ctx context.Context, trusted TrustedIdentity) ([]SessionView, error) {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return nil, err
	}
	sessions, err := service.repository.ListSessions(ctx, principal.Subject)
	if err != nil {
		return nil, err
	}
	result := make([]SessionView, len(sessions))
	for index, session := range sessions {
		result[index] = sessionView(session, session.ID == principal.Session)
	}
	return result, nil
}

func (service *Service) RevokeSession(ctx context.Context, trusted TrustedIdentity, sessionID string) error {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return err
	}
	if !validSafeIdentifier(sessionID, 128) {
		return ErrNotFound
	}
	return service.repository.RevokeSession(ctx, principal.Subject, sessionID, service.now().UTC())
}

func (service *Service) UpdateProfile(ctx context.Context, trusted TrustedIdentity, update ProfileUpdate) (Profile, error) {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return Profile{}, err
	}
	update.DisplayName = strings.TrimSpace(update.DisplayName)
	update.Email = strings.ToLower(strings.TrimSpace(update.Email))
	update.Phone = strings.TrimSpace(update.Phone)
	update.Locale = strings.TrimSpace(update.Locale)
	update.TimeZone = strings.TrimSpace(update.TimeZone)
	if !utf8.ValidString(update.DisplayName) ||
		utf8.RuneCountInString(update.DisplayName) < 1 ||
		utf8.RuneCountInString(update.DisplayName) > 100 ||
		!validProfileEmail(update.Email) || !validProfilePhone(update.Phone) ||
		(update.Locale != "en" && update.Locale != "ta") ||
		len(update.TimeZone) > 64 || update.Version < 1 {
		return Profile{}, ErrInvalidInput
	}
	if _, err := time.LoadLocation(update.TimeZone); err != nil {
		return Profile{}, ErrInvalidInput
	}
	return service.repository.UpdateProfile(ctx, principal.Subject, update, service.now().UTC())
}

func validProfileEmail(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && strings.Contains(value, "@")
}

func validProfilePhone(value string) bool {
	if value == "" {
		return true
	}
	if len(value) < 8 || len(value) > 16 || value[0] != '+' || value[1] < '1' || value[1] > '9' {
		return false
	}
	for _, character := range value[2:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (service *Service) Consents(ctx context.Context, trusted TrustedIdentity) ([]Consent, error) {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return nil, err
	}
	return service.repository.ListConsents(ctx, principal.Subject)
}

func (service *Service) RecordConsent(ctx context.Context, trusted TrustedIdentity, update ConsentUpdate) (Consent, error) {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return Consent{}, err
	}
	if !validConsentPurpose(update.Purpose) ||
		!validSafeIdentifier(update.PolicyVersion, 64) {
		return Consent{}, ErrInvalidInput
	}
	evidenceID, err := service.newID("consent")
	if err != nil {
		return Consent{}, fmt.Errorf("create consent evidence ID: %w", err)
	}
	now := service.now().UTC()
	return service.repository.RecordConsent(ctx, principal.Subject, Consent{
		EvidenceID:    evidenceID,
		Purpose:       update.Purpose,
		Granted:       update.Granted,
		PolicyVersion: update.PolicyVersion,
		RecordedAt:    now,
	}, now)
}

func (service *Service) ExportData(ctx context.Context, trusted TrustedIdentity) (DataExport, error) {
	principal, profile, err := service.Principal(ctx, trusted)
	if err != nil {
		return DataExport{}, err
	}
	sessions, err := service.repository.ListSessions(ctx, principal.Subject)
	if err != nil {
		return DataExport{}, err
	}
	consents, err := service.repository.ListConsents(ctx, principal.Subject)
	if err != nil {
		return DataExport{}, err
	}
	views := make([]SessionView, len(sessions))
	for index, session := range sessions {
		views[index] = sessionView(session, session.ID == principal.Session)
	}
	return DataExport{GeneratedAt: service.now().UTC(), IdentityID: principal.Subject, TenantID: principal.TenantID, Country: principal.Country, Roles: append([]Role(nil), principal.Roles...), Profile: profile, Sessions: views, Consents: consents}, nil
}

func (service *Service) RequestDeletion(ctx context.Context, trusted TrustedIdentity, reason string) (DeletionRequest, error) {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return DeletionRequest{}, err
	}
	reason = strings.TrimSpace(reason)
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 500 {
		return DeletionRequest{}, ErrInvalidInput
	}
	id, err := service.newID("deletion")
	if err != nil {
		return DeletionRequest{}, fmt.Errorf("create deletion request ID: %w", err)
	}
	now := service.now().UTC()
	return service.repository.CreateDeletionRequest(ctx, DeletionRequest{ID: id, IdentityID: principal.Subject, Status: "SCHEDULED", Reason: reason, RequestedAt: now, EffectiveAt: now.Add(30 * 24 * time.Hour)})
}

func (service *Service) authentication(account Identity, session Session, refreshToken string) (Authentication, error) {
	accessToken, accessExpiry, err := service.tokenIssuer.Issue(principalFor(account, session))
	if err != nil {
		return Authentication{}, fmt.Errorf("issue access token: %w", err)
	}
	roles := append([]Role(nil), account.Roles...)
	sort.Slice(roles, func(left, right int) bool { return roles[left] < roles[right] })
	return Authentication{
		IdentityID: account.ID,
		TenantID:   account.TenantID,
		Country:    session.Country,
		Tokens: TokenPair{
			AccessToken:      accessToken,
			AccessExpiresAt:  accessExpiry,
			RefreshToken:     refreshToken,
			RefreshExpiresAt: session.RefreshExpires,
			TokenType:        "Bearer",
		},
		Profile: account.Profile,
		Roles:   roles,
		Session: sessionView(session, true),
	}, nil
}

func (service *Service) newRefreshToken() (string, string, error) {
	token, err := service.refreshFactory.Generate()
	if err != nil {
		return "", "", fmt.Errorf("generate refresh token: %w", err)
	}
	digest, err := service.refreshHasher.Digest(token)
	if err != nil {
		return "", "", fmt.Errorf("hash refresh token: %w", err)
	}
	return token, digest, nil
}

func principalFor(account Identity, session Session) Principal {
	return Principal{
		Subject:  account.ID,
		Session:  session.ID,
		TenantID: account.TenantID,
		Country:  session.Country,
		DeviceID: session.DeviceID,
		Roles:    append([]Role(nil), account.Roles...),
		AuthTime: session.AuthenticatedAt,
	}
}

func sessionView(session Session, current bool) SessionView {
	return SessionView{
		ID:              session.ID,
		DeviceReference: session.DeviceReference,
		Country:         session.Country,
		AuthenticatedAt: session.AuthenticatedAt,
		LastSeenAt:      session.LastSeenAt,
		ExpiresAt:       session.RefreshExpires,
		RevokedAt:       session.RevokedAt,
		Current:         current,
	}
}

func validConsentPurpose(purpose ConsentPurpose) bool {
	switch purpose {
	case ConsentAnalytics, ConsentMarketing, ConsentLocationServiceability, ConsentLocationDelivery:
		return true
	default:
		return false
	}
}
