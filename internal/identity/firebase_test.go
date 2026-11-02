package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"firebase.google.com/go/v4/auth"
)

type firebaseAuthStub struct {
	token *auth.Token
	err   error
	calls int
}

func (stub *firebaseAuthStub) VerifyIDTokenAndCheckRevoked(context.Context, string) (*auth.Token, error) {
	stub.calls++
	return stub.token, stub.err
}

func TestFirebaseVerifierUsesStableUIDAndDiscardsCustomRoles(t *testing.T) {
	client := &firebaseAuthStub{token: &auth.Token{UID: "linked-firebase-user", Claims: map[string]any{"roles": []string{"ADMIN", "VENDOR"}}}}
	verifier, err := NewFirebaseVerifier(client)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := verifier.Verify(context.Background(), "firebase", "verified-provider-token")
	if err != nil || identity != (ProviderIdentity{Provider: "firebase", Subject: "linked-firebase-user"}) || client.calls != 1 {
		t.Fatalf("identity=%+v calls=%d err=%v", identity, client.calls, err)
	}
	if _, err := verifier.Verify(context.Background(), "local", "verified-provider-token"); !errors.Is(err, ErrProviderRejected) || client.calls != 1 {
		t.Fatalf("provider substitution accepted: %v", err)
	}
	client.token = nil
	if _, err := verifier.Verify(context.Background(), "firebase", "verified-provider-token"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
	client.err = context.DeadlineExceeded
	if _, err := verifier.Verify(context.Background(), "firebase", "verified-provider-token"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
}

func TestFirebaseUIDCannotCrossAuthenticationTenants(t *testing.T) {
	client := &firebaseAuthStub{token: &auth.Token{UID: "same-uid", Firebase: auth.FirebaseInfo{Tenant: "other-auth-tenant"}}}
	verifier, _ := NewFirebaseVerifier(client)
	if _, err := verifier.Verify(context.Background(), "firebase", "verified-provider-token"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal("foreign auth tenant accepted")
	}
	verifier.authTenant = "expected-auth-tenant"
	if _, err := verifier.Verify(context.Background(), "firebase", "verified-provider-token"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal("UID collision crossed auth tenants")
	}
	client.token.Firebase.Tenant = "expected-auth-tenant"
	if _, err := verifier.Verify(context.Background(), "firebase", "verified-provider-token"); err != nil {
		t.Fatal(err)
	}
}

func TestFirebaseRuntimeRefusesEmulatorTrust(t *testing.T) {
	t.Setenv("FIREBASE_AUTH_EMULATOR_HOST", "localhost:9099")
	if _, err := OpenFirebaseVerifier(context.Background(), "test-project", ""); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal(err)
	}
}

func TestFirebaseOperatorAssuranceComesFromVerifiedSecondFactor(t *testing.T) {
	if _, err := NewFirebaseVerifier(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal(err)
	}
	client := &firebaseAuthStub{token: &auth.Token{UID: "operator-user", AuthTime: time.Now().Unix(), Claims: map[string]any{"mfa": true, "roles": []string{"SUPER_ADMIN"}}}}
	verifier, _ := NewFirebaseVerifier(client)
	result, err := verifier.VerifyOperator(context.Background(), "verified-provider-token")
	if err != nil || result.MFA || result.Subject != "operator-user" {
		t.Fatalf("custom assurance accepted: %+v %v", result, err)
	}
	for _, factor := range []string{"phone", "totp"} {
		client.token.Claims["firebase"] = map[string]any{"sign_in_second_factor": factor}
		result, err = verifier.VerifyOperator(context.Background(), "verified-provider-token")
		if err != nil || !result.MFA || result.AuthenticatedAt.Unix() != client.token.AuthTime {
			t.Fatalf("MFA=%+v %v", result, err)
		}
	}
	client.token.UID = "invalid identity with spaces"
	if _, err = verifier.VerifyOperator(context.Background(), "verified-provider-token"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
	if _, err = verifier.VerifyOperator(context.Background(), "short"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
	client.err = errors.New("provider transport failure")
	if _, err = verifier.VerifyOperator(context.Background(), "verified-provider-token"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	t.Setenv("FIREBASE_AUTH_EMULATOR_HOST", "")
	if _, err = OpenFirebaseVerifier(context.Background(), "test-project", t.TempDir()+"/missing-credentials.json"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal(err)
	}
}
