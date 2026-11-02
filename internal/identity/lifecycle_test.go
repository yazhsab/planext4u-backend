package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recoveryProofVerifier struct {
	proof ProviderAssurance
	err   error
}

func (verifier *recoveryProofVerifier) Verify(context.Context, string, string) (ProviderIdentity, error) {
	return ProviderIdentity{Provider: "firebase", Subject: "verified-owner"}, verifier.err
}
func (verifier *recoveryProofVerifier) VerifyOperator(context.Context, string) (ProviderAssurance, error) {
	return verifier.proof, verifier.err
}

func TestLifecycleFailsClosedWithoutOwnerAdapterOrFreshMatchingProviderProof(t *testing.T) {
	ctx := context.Background()
	fixture := newServiceFixture(t)
	auth := fixture.exchange(t, "provider-lifecycle-unit", "device-lifecycle")
	trusted := TrustedIdentity{Subject: auth.IdentityID, Session: auth.Session.ID, TenantID: auth.TenantID, Country: auth.Country}
	if _, err := fixture.service.Lifecycle(ctx, trusted); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	if err := fixture.service.Deactivate(ctx, trusted); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	if err := fixture.service.CancelDeletion(ctx, trusted, "valid-request"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	if err := fixture.service.CancelDeletion(ctx, trusted, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	bad := trusted
	bad.Session = "not-a-session"
	if _, err := fixture.service.Lifecycle(ctx, bad); err == nil {
		t.Fatal("unverified lifecycle read")
	}
	if err := fixture.service.Deactivate(ctx, bad); err == nil {
		t.Fatal("unverified deactivation")
	}
	if err := fixture.service.CancelDeletion(ctx, bad, "valid-request"); err == nil {
		t.Fatal("unverified deletion cancellation")
	}
	proof := &recoveryProofVerifier{proof: ProviderAssurance{Subject: "verified-owner", AuthenticatedAt: fixture.clock.Now()}}
	fixture.service.providerVerifier = proof
	input := ExchangeInput{Provider: "firebase", ProviderToken: "verified-provider-token", DeviceID: "device-lifecycle", Country: "IN"}
	if _, err := fixture.service.Recover(ctx, input); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("missing owner adapter fabricated recovery")
	}
	for _, value := range []ProviderAssurance{{Subject: "other-owner", AuthenticatedAt: fixture.clock.Now()}, {Subject: "verified-owner", AuthenticatedAt: fixture.clock.Now().Add(-6 * time.Minute)}, {Subject: "verified-owner", AuthenticatedAt: fixture.clock.Now().Add(time.Minute)}} {
		proof.proof = value
		if _, err := fixture.service.Recover(ctx, input); !errors.Is(err, ErrProviderRejected) {
			t.Fatalf("stale/mismatched proof accepted: %+v %v", value, err)
		}
	}
	proof.err = ErrProviderRejected
	if _, err := fixture.service.Recover(ctx, input); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
	badInput := input
	badInput.EnrollAs = RoleAdmin
	if _, err := fixture.service.Recover(ctx, badInput); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("recovery role escalation accepted")
	}
	handler, _ := NewHandler(fixture.service, nil)
	for _, path := range []string{"/v1/me/lifecycle", "/v1/me/deactivation", "/v1/me/deletion-requests/request/cancel"} {
		method := http.MethodPost
		if path == "/v1/me/lifecycle" {
			method = http.MethodGet
		}
		request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("anonymous lifecycle status=%d", response.Code)
		}
	}
}
