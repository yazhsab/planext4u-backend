package identity

import (
	"context"
	"errors"
	"time"
)

var ErrContactConflict = errors.New("contact snapshot changed; refresh before syncing")
var ErrContactConsent = errors.New("current contact matching consent required")

// ContactPhoneDirectory resolves immutable provider UIDs against the live
// provider, never editable platform profile fields. Missing/disabled users are
// omitted. A provider outage rejects the whole page instead of using old proof.
type ContactPhoneDirectory interface {
	ContactPhones(context.Context, []string) (map[string]string, error)
}

type contactRepository interface {
	ContactState(context.Context, TrustedIdentity, contactMatchingSettings, time.Time) (ContactMatchingState, error)
	PutContactSnapshot(context.Context, TrustedIdentity, ProviderAccount, contactSnapshot, contactMatchingSettings, time.Time) (ContactMatchingState, error)
	ContactMatches(context.Context, TrustedIdentity, contactMatchingSettings, int64, string, int, ContactPhoneDirectory, time.Time) (ContactMatches, error)
}

func (service *Service) ContactState(ctx context.Context, trusted TrustedIdentity) (ContactMatchingState, error) {
	if _, _, err := service.Principal(ctx, trusted); err != nil {
		return ContactMatchingState{}, err
	}
	if service.contacts == nil {
		return ContactMatchingState{Status: "UNAVAILABLE"}, nil
	}
	return service.repository.(contactRepository).ContactState(ctx, trusted, *service.contacts, service.now().UTC())
}

func (service *Service) SyncContacts(ctx context.Context, trusted TrustedIdentity, input ContactSnapshotRequest) (ContactMatchingState, error) {
	if _, _, err := service.Principal(ctx, trusted); err != nil {
		return ContactMatchingState{}, err
	}
	if service.contacts == nil {
		return ContactMatchingState{}, ErrProviderUnavailable
	}
	if !validOpaqueToken(input.ProviderToken, 8, 8192) {
		return ContactMatchingState{}, ErrInvalidInput
	}
	proof, err := service.providerVerifier.(ProviderAccountVerifier).VerifyAccount(ctx, input.ProviderToken)
	if err != nil {
		return ContactMatchingState{}, err
	}
	now := service.now().UTC()
	snapshot, err := service.contacts.prepare(trusted.TenantID, proof, input, now)
	if err != nil {
		return ContactMatchingState{}, err
	}
	return service.repository.(contactRepository).PutContactSnapshot(ctx, trusted, proof, snapshot, *service.contacts, now)
}

func (service *Service) MatchContacts(ctx context.Context, trusted TrustedIdentity, revision int64, cursor string, limit int) (ContactMatches, error) {
	if _, _, err := service.Principal(ctx, trusted); err != nil {
		return ContactMatches{}, err
	}
	if service.contacts == nil {
		return ContactMatches{}, ErrProviderUnavailable
	}
	if revision < 1 || limit < 1 || limit > 50 || cursor != "" && !validSafeIdentifier(cursor, 128) {
		return ContactMatches{}, ErrInvalidInput
	}
	return service.repository.(contactRepository).ContactMatches(ctx, trusted, *service.contacts, revision, cursor, limit, service.providerVerifier.(ContactPhoneDirectory), service.now().UTC())
}
