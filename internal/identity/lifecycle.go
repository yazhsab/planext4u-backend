package identity

import (
	"context"
	"time"
)

type AccountLifecycle struct {
	Status   string           `json:"status"`
	Deletion *DeletionRequest `json:"deletion,omitempty"`
}
type AccountLifecycleRepository interface {
	AccountLifecycle(context.Context, string) (AccountLifecycle, error)
	DeactivateAccount(context.Context, TrustedIdentity, time.Time) error
	RecoverAccount(context.Context, string, ProviderIdentity, time.Time) error
	CancelDeletion(context.Context, TrustedIdentity, string, time.Time) error
}

func (service *Service) Lifecycle(ctx context.Context, trusted TrustedIdentity) (AccountLifecycle, error) {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return AccountLifecycle{}, err
	}
	repository, ok := service.repository.(AccountLifecycleRepository)
	if !ok {
		return AccountLifecycle{}, ErrProviderUnavailable
	}
	return repository.AccountLifecycle(ctx, principal.Subject)
}
func (service *Service) Deactivate(ctx context.Context, trusted TrustedIdentity) error {
	principal, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return err
	}
	if service.now().UTC().Sub(principal.AuthTime) > 5*time.Minute {
		return ErrForbidden
	}
	repository, ok := service.repository.(AccountLifecycleRepository)
	if !ok {
		return ErrProviderUnavailable
	}
	return repository.DeactivateAccount(ctx, trusted, service.now().UTC())
}
func (service *Service) CancelDeletion(ctx context.Context, trusted TrustedIdentity, id string) error {
	_, _, err := service.Principal(ctx, trusted)
	if err != nil {
		return err
	}
	if !validSafeIdentifier(id, 128) {
		return ErrInvalidInput
	}
	repository, ok := service.repository.(AccountLifecycleRepository)
	if !ok {
		return ErrProviderUnavailable
	}
	return repository.CancelDeletion(ctx, trusted, id, service.now().UTC())
}

// Recovery requires fresh provider evidence for exactly the original account.
// It never clears a security/operator restriction or an overdue deletion.
func (service *Service) Recover(ctx context.Context, input ExchangeInput) (Authentication, error) {
	if input.EnrollAs != "" || !validProvider(input.Provider) || !validSafeIdentifier(input.DeviceID, 128) || !validOpaqueToken(input.ProviderToken, 8, 8192) {
		return Authentication{}, ErrInvalidInput
	}
	if _, ok := service.allowedCountries[input.Country]; !ok {
		return Authentication{}, ErrInvalidInput
	}
	verified, err := service.providerVerifier.Verify(ctx, input.Provider, input.ProviderToken)
	if err != nil {
		return Authentication{}, err
	}
	if input.Provider != "local" {
		assuranceVerifier, ok := service.providerVerifier.(interface {
			VerifyOperator(context.Context, string) (ProviderAssurance, error)
		})
		if !ok {
			return Authentication{}, ErrProviderUnavailable
		}
		proof, proofErr := assuranceVerifier.VerifyOperator(ctx, input.ProviderToken)
		now := service.now().UTC()
		if proofErr != nil {
			return Authentication{}, proofErr
		}
		if proof.Subject != verified.Subject || proof.AuthenticatedAt.After(now.Add(30*time.Second)) || now.Sub(proof.AuthenticatedAt) > 5*time.Minute {
			return Authentication{}, ErrProviderRejected
		}
	}
	repository, ok := service.repository.(AccountLifecycleRepository)
	if !ok {
		return Authentication{}, ErrProviderUnavailable
	}
	if err = repository.RecoverAccount(ctx, service.tenantID, verified, service.now().UTC()); err != nil {
		return Authentication{}, err
	}
	return service.Exchange(ctx, input)
}
