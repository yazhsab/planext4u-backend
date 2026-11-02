package identity

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

// FirebaseAuth deliberately exposes only verification, never platform role claims.
type FirebaseAuth interface {
	VerifyIDTokenAndCheckRevoked(context.Context, string) (*auth.Token, error)
}

type FirebaseVerifier struct {
	client     FirebaseAuth
	authTenant string
}

type ProviderAssurance struct {
	Subject         string
	AuthenticatedAt time.Time
	MFA             bool
}

func NewFirebaseVerifier(client FirebaseAuth) (*FirebaseVerifier, error) {
	if client == nil {
		return nil, ErrInvalidConfiguration
	}
	return &FirebaseVerifier{client: client}, nil
}

// OpenFirebaseVerifier uses the official Admin SDK. Unsigned emulator tokens
// must never become an authentication option in a deployed service.
func OpenFirebaseVerifier(ctx context.Context, project, credentialsFile string) (*FirebaseVerifier, error) {
	return OpenFirebaseTenantVerifier(ctx, project, credentialsFile, "")
}

func OpenFirebaseTenantVerifier(ctx context.Context, project, credentialsFile, authTenant string) (*FirebaseVerifier, error) {
	if strings.TrimSpace(project) == "" || os.Getenv("FIREBASE_AUTH_EMULATOR_HOST") != "" {
		return nil, ErrInvalidConfiguration
	}
	var options []option.ClientOption
	if credentialsFile != "" {
		options = append(options, option.WithCredentialsFile(credentialsFile))
	}
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: project}, options...)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	client, err := app.Auth(ctx)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	var verificationClient FirebaseAuth = client
	if authTenant != "" {
		tenantClient, tenantErr := client.TenantManager.AuthForTenant(authTenant)
		if tenantErr != nil {
			return nil, ErrInvalidConfiguration
		}
		verificationClient = tenantClient
	}
	verifier, err := NewFirebaseVerifier(verificationClient)
	if err != nil {
		return nil, err
	}
	verifier.authTenant = authTenant
	return verifier, nil
}

func (verifier *FirebaseVerifier) Verify(ctx context.Context, provider, raw string) (ProviderIdentity, error) {
	if provider != "firebase" || !validOpaqueToken(raw, 8, 8192) {
		return ProviderIdentity{}, ErrProviderRejected
	}
	token, err := verifier.verifyToken(ctx, raw)
	if err != nil {
		return ProviderIdentity{}, err
	}
	return ProviderIdentity{Provider: provider, Subject: token.UID}, nil
}

func (verifier *FirebaseVerifier) VerifyOperator(ctx context.Context, raw string) (ProviderAssurance, error) {
	if !validOpaqueToken(raw, 8, 8192) {
		return ProviderAssurance{}, ErrProviderRejected
	}
	token, err := verifier.verifyToken(ctx, raw)
	if err != nil {
		return ProviderAssurance{}, err
	}
	firebaseClaims, _ := token.Claims["firebase"].(map[string]any)
	factor, _ := firebaseClaims["sign_in_second_factor"].(string)
	return ProviderAssurance{Subject: token.UID, AuthenticatedAt: time.Unix(token.AuthTime, 0).UTC(), MFA: factor == "phone" || factor == "totp"}, nil
}

func (verifier *FirebaseVerifier) VerifyAccount(ctx context.Context, raw string) (ProviderAccount, error) {
	if !validOpaqueToken(raw, 8, 8192) {
		return ProviderAccount{}, ErrProviderRejected
	}
	token, err := verifier.verifyToken(ctx, raw)
	if err != nil {
		return ProviderAccount{}, err
	}
	reader, ok := verifier.client.(interface {
		GetUser(context.Context, string) (*auth.UserRecord, error)
	})
	if !ok {
		return ProviderAccount{}, ErrProviderUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	user, err := reader.GetUser(bounded, token.UID)
	if err != nil {
		if auth.IsUserNotFound(err) || auth.IsUserDisabled(err) {
			return ProviderAccount{}, ErrProviderRejected
		}
		return ProviderAccount{}, ErrProviderUnavailable
	}
	if user == nil || user.UserInfo == nil || user.UID != token.UID || user.Disabled || user.TenantID != verifier.authTenant {
		return ProviderAccount{}, ErrProviderRejected
	}
	proof := ProviderAccount{Subject: token.UID, AuthenticatedAt: time.Unix(token.AuthTime, 0).UTC(), Methods: []string{}}
	seen := map[string]bool{}
	for _, provider := range user.ProviderUserInfo {
		if provider == nil {
			continue
		}
		method := provider.ProviderID
		if (method == "password" || method == "phone" || method == "google.com" || method == "apple.com") && !seen[method] {
			proof.Methods = append(proof.Methods, method)
			seen[method] = true
		}
	}
	if user.EmailVerified {
		proof.VerifiedEmail = user.Email
	}
	// A phone number is trusted only while it is attached to a phone credential.
	if seen["phone"] {
		proof.VerifiedPhone = user.PhoneNumber
	}
	sort.Strings(proof.Methods)
	if !validProviderAccount(proof) {
		return ProviderAccount{}, ErrProviderRejected
	}
	return proof, nil
}

func (verifier *FirebaseVerifier) verifyToken(ctx context.Context, raw string) (*auth.Token, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := verifier.client.VerifyIDTokenAndCheckRevoked(bounded, raw)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrProviderUnavailable
		}
		if auth.IsIDTokenExpired(err) || auth.IsIDTokenRevoked(err) || auth.IsUserDisabled(err) ||
			auth.IsIDTokenInvalid(err) || auth.IsUserNotFound(err) {
			return nil, ErrProviderRejected
		}
		return nil, ErrProviderUnavailable
	}
	if token == nil || !validSafeIdentifier(token.UID, 128) || token.Firebase.Tenant != verifier.authTenant {
		return nil, ErrProviderRejected
	}
	// Firebase links email/phone/Google credentials under one UID. Never merge
	// accounts using an unverified profile email or a client supplied identifier.
	return token, nil
}

func (verifier *FirebaseVerifier) ContactPhones(ctx context.Context, subjects []string) (map[string]string, error) {
	if len(subjects) < 1 || len(subjects) > 100 {
		return nil, ErrInvalidInput
	}
	reader, ok := verifier.client.(interface {
		GetUsers(context.Context, []auth.UserIdentifier) (*auth.GetUsersResult, error)
	})
	if !ok {
		return nil, ErrProviderUnavailable
	}
	identifiers := make([]auth.UserIdentifier, 0, len(subjects))
	requested := map[string]bool{}
	for _, subject := range subjects {
		if !validSafeIdentifier(subject, 128) {
			return nil, ErrInvalidInput
		}
		if !requested[subject] {
			requested[subject] = true
			identifiers = append(identifiers, auth.UIDIdentifier{UID: subject})
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := reader.GetUsers(bounded, identifiers)
	if err != nil || result == nil {
		return nil, ErrProviderUnavailable
	}
	phones := map[string]string{}
	for _, user := range result.Users {
		if user == nil || user.UserInfo == nil || !requested[user.UID] || user.TenantID != verifier.authTenant {
			return nil, ErrProviderUnavailable
		}
		if user.Disabled {
			continue
		}
		phoneCredential := false
		for _, provider := range user.ProviderUserInfo {
			if provider != nil && provider.ProviderID == "phone" {
				phoneCredential = true
			}
		}
		if phoneCredential && contactPhonePattern.MatchString(user.PhoneNumber) {
			phones[user.UID] = user.PhoneNumber
		}
	}
	return phones, nil
}
