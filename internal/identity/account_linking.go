package identity

import (
	"context"
	"net/http"
	"sort"
	"time"
)

// ProviderAccount is read from the trusted provider, never from a client profile.
// The provider owns credential linking; the platform binding remains immutable.
type ProviderAccount struct {
	Subject         string
	AuthenticatedAt time.Time
	Methods         []string
	VerifiedEmail   string
	VerifiedPhone   string
}

type SignInMethods struct {
	ProviderSubject string   `json:"provider_subject"`
	Methods         []string `json:"methods"`
	EmailVerified   bool     `json:"email_verified"`
}

type ProviderAccountVerifier interface {
	VerifyAccount(context.Context, string) (ProviderAccount, error)
}

type ProviderAccountRepository interface {
	ConfirmProviderAccount(context.Context, TrustedIdentity, ProviderAccount, time.Time) error
}

// VerifySignInMethods proves that a freshly reauthenticated provider user still
// belongs to this live platform session. It never merges two provider UIDs, even
// if their email addresses match, and never grants participation or admin roles.
func (service *Service) VerifySignInMethods(ctx context.Context, trusted TrustedIdentity, raw string) (SignInMethods, error) {
	return service.verifySignInMethods(ctx, trusted, raw, true)
}

func (service *Service) verifySignInMethods(ctx context.Context, trusted TrustedIdentity, raw string, fresh bool) (SignInMethods, error) {
	if !validOpaqueToken(raw, 8, 8192) {
		return SignInMethods{}, ErrInvalidInput
	}
	if _, _, err := service.Principal(ctx, trusted); err != nil {
		return SignInMethods{}, err
	}
	verifier, ok := service.providerVerifier.(ProviderAccountVerifier)
	if !ok {
		return SignInMethods{}, ErrProviderUnavailable
	}
	proof, err := verifier.VerifyAccount(ctx, raw)
	if err != nil {
		return SignInMethods{}, err
	}
	now := service.now().UTC()
	if !validProviderAccount(proof) || proof.AuthenticatedAt.After(now.Add(30*time.Second)) || fresh && now.Sub(proof.AuthenticatedAt) > 5*time.Minute {
		return SignInMethods{}, ErrProviderRejected
	}
	repository, ok := service.repository.(ProviderAccountRepository)
	if !ok {
		return SignInMethods{}, ErrProviderUnavailable
	}
	// Revalidate and commit evidence under repository locks after the network call.
	if err = repository.ConfirmProviderAccount(ctx, trusted, proof, now); err != nil {
		return SignInMethods{}, err
	}
	methods := append([]string{}, proof.Methods...)
	sort.Strings(methods)
	return SignInMethods{ProviderSubject: proof.Subject, Methods: methods, EmailVerified: proof.VerifiedEmail != ""}, nil
}

func validProviderAccount(proof ProviderAccount) bool {
	if !validSafeIdentifier(proof.Subject, 128) || proof.AuthenticatedAt.IsZero() || len(proof.Methods) < 1 || len(proof.Methods) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, method := range proof.Methods {
		if seen[method] || method != "password" && method != "phone" && method != "google.com" && method != "apple.com" {
			return false
		}
		seen[method] = true
	}
	return validProfileEmail(proof.VerifiedEmail) && validProfilePhone(proof.VerifiedPhone)
}

func (handler *Handler) handleSignInMethods(writer http.ResponseWriter, request *http.Request) {
	trusted, err := trustedIdentity(request)
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	var input struct {
		ProviderToken string `json:"provider_token"`
	}
	if err = decodeJSON(request, &input); err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	// Binding permits an older authentication time solely to select the correct
	// existing user before SDK reauthentication. The verify endpoint requires
	// recent proof before the UI can offer linking.
	result, err := handler.service.verifySignInMethods(request.Context(), trusted, input.ProviderToken, request.URL.Path != "/v1/me/sign-in-methods/binding")
	if err != nil {
		handler.writeFailure(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
