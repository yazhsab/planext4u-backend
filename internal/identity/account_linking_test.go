package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"firebase.google.com/go/v4/auth"
)

type accountProofVerifier struct {
	staticProviderVerifier
	proof  ProviderAccount
	err    error
	before func()
}

func (v *accountProofVerifier) VerifyAccount(context.Context, string) (ProviderAccount, error) {
	if v.before != nil {
		v.before()
	}
	return v.proof, v.err
}

func linkingFixture(t *testing.T) (*serviceFixture, Authentication, *accountProofVerifier) {
	t.Helper()
	f := newServiceFixture(t)
	v := &accountProofVerifier{proof: ProviderAccount{Subject: "firebase-owner", AuthenticatedAt: f.clock.Now(), Methods: []string{"phone", "password"}, VerifiedEmail: "verified@example.test", VerifiedPhone: "+919876543210"}}
	f.service.providerVerifier = v
	a, err := f.service.Exchange(context.Background(), ExchangeInput{Provider: "firebase", ProviderToken: v.proof.Subject, DeviceID: "linking-device", Country: "IN"})
	if err != nil {
		t.Fatal(err)
	}
	return f, a, v
}

func TestAccountLinkingKeepsImmutableUIDRolesAndSession(t *testing.T) {
	f, a, _ := linkingFixture(t)
	result, err := f.service.VerifySignInMethods(context.Background(), trustedFor(a), "fresh-provider-token")
	if err != nil || result.ProviderSubject != "firebase-owner" || strings.Join(result.Methods, ",") != "password,phone" || !result.EmailVerified {
		t.Fatalf("%+v %v", result, err)
	}
	principal, profile, err := f.service.Principal(context.Background(), trustedFor(a))
	if err != nil || profile.Email != "verified@example.test" || profile.Phone != "+919876543210" || profile.Version != 2 || principal.Session != a.Session.ID || len(principal.Roles) != 1 || principal.Roles[0] != RoleCustomer {
		t.Fatalf("%+v %+v %v", principal, profile, err)
	}
	if !containsAuditType(f.repository.AuditEvents(), "SIGN_IN_METHODS_VERIFIED") {
		t.Fatal("missing security evidence")
	}
	if _, err = f.service.VerifySignInMethods(context.Background(), trustedFor(a), "fresh-provider-token"); err != nil {
		t.Fatal(err)
	}
	_, unchanged, _ := f.service.Principal(context.Background(), trustedFor(a))
	if unchanged.Version != profile.Version {
		t.Fatal("unchanged contacts churned revision")
	}
}

func TestAccountLinkingRejectsForeignAndStaleProofAndRevokedSessions(t *testing.T) {
	for _, name := range []string{"foreign UID", "foreign tenant", "foreign country", "old proof", "future proof", "no methods", "duplicate method", "unknown method", "invalid email", "invalid phone", "provider unavailable", "revoked during provider call", "disabled during provider call", "local binding"} {
		t.Run(name, func(t *testing.T) {
			f, a, v := linkingFixture(t)
			trusted := trustedFor(a)
			switch name {
			case "foreign UID":
				v.proof.Subject = "foreign-uid"
			case "foreign tenant":
				trusted.TenantID = "foreign-tenant"
			case "foreign country":
				trusted.Country = "GB"
			case "old proof":
				v.proof.AuthenticatedAt = f.clock.Now().Add(-5*time.Minute - time.Second)
			case "future proof":
				v.proof.AuthenticatedAt = f.clock.Now().Add(31 * time.Second)
			case "no methods":
				v.proof.Methods = nil
			case "duplicate method":
				v.proof.Methods = []string{"phone", "phone"}
			case "unknown method":
				v.proof.Methods = []string{"admin"}
			case "invalid email":
				v.proof.VerifiedEmail = "x\r\n@example.test"
			case "invalid phone":
				v.proof.VerifiedPhone = "not-a-phone"
			case "provider unavailable":
				v.err = ErrProviderUnavailable
			case "revoked during provider call":
				v.before = func() { _ = f.service.Revoke(context.Background(), a.Tokens.RefreshToken) }
			case "disabled during provider call":
				v.before = func() {
					f.repository.mu.Lock()
					defer f.repository.mu.Unlock()
					value := f.repository.identities[a.IdentityID]
					now := f.clock.Now()
					value.DisabledAt = &now
					f.repository.identities[a.IdentityID] = value
				}
			case "local binding":
				f.repository.mu.Lock()
				value := f.repository.identities[a.IdentityID]
				value.Provider = "local"
				f.repository.identities[a.IdentityID] = value
				f.repository.mu.Unlock()
			}
			if _, err := f.service.VerifySignInMethods(context.Background(), trusted, "fresh-provider-token"); err == nil {
				t.Fatal("unsafe proof accepted")
			}
			if containsAuditType(f.repository.AuditEvents(), "SIGN_IN_METHODS_VERIFIED") {
				t.Fatal("denial wrote success evidence")
			}
			if f.repository.identities[a.IdentityID].Profile.Email != "" {
				t.Fatal("denial changed contacts")
			}
		})
	}
}

func TestAccountLinkingHTTPContractAndUnsupportedProvider(t *testing.T) {
	f, a, _ := linkingFixture(t)
	handler, _ := NewHandler(f.service, nil)
	for _, tc := range []struct {
		method, body string
		headers      http.Header
		want         int
	}{
		{"POST", `{"provider_token":"fresh-provider-token"}`, trustedHeaders(a), 200},
		{"POST", `{"provider_token":"fresh-provider-token","subject":"foreign"}`, trustedHeaders(a), 422},
		{"POST", `{"provider_token":"short"}`, trustedHeaders(a), 422},
		{"POST", `{"provider_token":"fresh-provider-token"}`, nil, 401},
		{"GET", "", trustedHeaders(a), 405},
	} {
		r := identityRequest(handler, tc.method, "/v1/me/sign-in-methods/verify", tc.body, tc.headers)
		if r.Code != tc.want || strings.Contains(r.Body.String(), "fresh-provider-token") || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	}
	f.service.providerVerifier = &staticProviderVerifier{}
	if _, err := f.service.VerifySignInMethods(context.Background(), trustedFor(a), "fresh-provider-token"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
}

type firebaseAccountStub struct {
	firebaseAuthStub
	user    *auth.UserRecord
	userErr error
}

func (s *firebaseAccountStub) GetUser(context.Context, string) (*auth.UserRecord, error) {
	return s.user, s.userErr
}

func TestFirebaseAccountProofUsesLiveProviderRecord(t *testing.T) {
	s := &firebaseAccountStub{firebaseAuthStub: firebaseAuthStub{token: &auth.Token{UID: "owner", AuthTime: 100}}, user: &auth.UserRecord{UserInfo: &auth.UserInfo{UID: "owner", Email: "unverified@example.test", PhoneNumber: "+919876543210"}, ProviderUserInfo: []*auth.UserInfo{{ProviderID: "google.com"}, {ProviderID: "phone"}, {ProviderID: "phone"}, nil, {ProviderID: "untrusted"}}}}
	v, _ := NewFirebaseVerifier(s)
	proof, err := v.VerifyAccount(context.Background(), "fresh-provider-token")
	if err != nil || proof.VerifiedEmail != "" || proof.VerifiedPhone != "+919876543210" || strings.Join(proof.Methods, ",") != "google.com,phone" || proof.AuthenticatedAt.Unix() != 100 {
		t.Fatalf("%+v %v", proof, err)
	}
	s.user.EmailVerified = true
	proof, err = v.VerifyAccount(context.Background(), "fresh-provider-token")
	if err != nil || proof.VerifiedEmail != "unverified@example.test" {
		t.Fatal(proof, err)
	}
	for _, change := range []func(){func() { s.user.Disabled = true }, func() { s.user.Disabled = false; s.user.TenantID = "foreign" }, func() { s.user.TenantID = ""; s.user.UID = "other" }, func() { s.user = nil }} {
		change()
		if _, err = v.VerifyAccount(context.Background(), "fresh-provider-token"); !errors.Is(err, ErrProviderRejected) {
			t.Fatal(err)
		}
	}
	s.userErr = context.DeadlineExceeded
	if _, err = v.VerifyAccount(context.Background(), "fresh-provider-token"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	if _, err = v.VerifyAccount(context.Background(), "short"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
	plain, _ := NewFirebaseVerifier(&firebaseAuthStub{token: &auth.Token{UID: "owner"}})
	if _, err = plain.VerifyAccount(context.Background(), "fresh-provider-token"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
}
