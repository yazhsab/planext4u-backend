package identity

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"firebase.google.com/go/v4/auth"
)

type contactRepositoryStub struct {
	*MemoryRepository
	last                 contactSnapshot
	reads, writes, pages int
}

func (r *contactRepositoryStub) ContactState(_ context.Context, _ TrustedIdentity, s contactMatchingSettings, _ time.Time) (ContactMatchingState, error) {
	r.reads++
	return ContactMatchingState{Status: "EMPTY", MaxContacts: s.maxContacts, PolicyVersion: "contacts-v1"}, nil
}
func (r *contactRepositoryStub) PutContactSnapshot(_ context.Context, _ TrustedIdentity, _ ProviderAccount, snapshot contactSnapshot, s contactMatchingSettings, _ time.Time) (ContactMatchingState, error) {
	r.writes++
	r.last = snapshot
	return ContactMatchingState{Status: "READY", PolicyVersion: snapshot.policyVersion, Revision: 1, ContactCount: len(snapshot.contactHashes), MaxContacts: s.maxContacts, ExpiresAt: &snapshot.expiresAt}, nil
}
func (r *contactRepositoryStub) ContactMatches(_ context.Context, _ TrustedIdentity, _ contactMatchingSettings, revision int64, _ string, _ int, _ ContactPhoneDirectory, _ time.Time) (ContactMatches, error) {
	r.pages++
	return ContactMatches{SnapshotRevision: revision, Items: []ContactMatch{{IdentityID: "member-2", DisplayName: "Member"}}}, nil
}

type contactVerifierStub struct{ *accountProofVerifier }

func (*contactVerifierStub) ContactPhones(context.Context, []string) (map[string]string, error) {
	return map[string]string{"firebase-owner": "+919876543210"}, nil
}

func TestContactHTTPConfiguredServiceUsesOwnerOnlyAndNeverReturnsPhoneOrToken(t *testing.T) {
	f, account, proof := linkingFixture(t)
	repository := &contactRepositoryStub{MemoryRepository: f.repository}
	settings := ContactMatchingSettings{HMACKey: bytes.Repeat([]byte{42}, 32), KeyID: "test-v1", SnapshotTTL: time.Hour, MaxContacts: 100}
	service, err := NewService(ServiceConfig{
		TenantID: f.service.tenantID, AllowedCountries: []string{"IN"}, SessionTTL: f.service.sessionTTL,
		Repository: repository, ProviderVerifier: &contactVerifierStub{proof}, TokenIssuer: f.service.tokenIssuer,
		RefreshFactory: f.service.refreshFactory, RefreshHasher: f.service.refreshHasher,
		Now: f.clock.Now, IDFactory: f.service.newID, ContactMatching: &settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := trustedHeaders(account)
	state := identityRequest(handler, "GET", "/v1/me/contact-matching", "", headers)
	if state.Code != 200 || repository.reads != 1 {
		t.Fatalf("state %d %s", state.Code, state.Body.String())
	}
	sync := identityRequest(handler, "PUT", "/v1/me/contact-matching", `{"provider_token":"fresh-provider-token","policy_version":"contacts-v1","expected_revision":0,"phones":["+919876543211"]}`, headers)
	if sync.Code != 200 || repository.writes != 1 || len(repository.last.contactHashes) != 1 {
		t.Fatalf("sync %d %s", sync.Code, sync.Body.String())
	}
	page := identityRequest(handler, "GET", "/v1/me/contact-matching/matches?revision=1&limit=10", "", headers)
	if page.Code != 200 || repository.pages != 1 || !strings.Contains(page.Body.String(), "member-2") {
		t.Fatalf("matches %d %s", page.Code, page.Body.String())
	}
	for _, response := range []string{state.Body.String(), sync.Body.String(), page.Body.String()} {
		for _, private := range []string{"+919876543211", "fresh-provider-token", repository.last.contactHashes[0]} {
			if strings.Contains(response, private) {
				t.Fatal("contact secret leaked in HTTP response")
			}
		}
	}
}

type firebaseContactStub struct {
	firebaseAuthStub
	result *auth.GetUsersResult
	err    error
	calls  int
}

func (s *firebaseContactStub) GetUsers(_ context.Context, ids []auth.UserIdentifier) (*auth.GetUsersResult, error) {
	s.calls++
	if len(ids) > 100 {
		panic("unbounded provider lookup")
	}
	return s.result, s.err
}

func TestFirebaseContactDirectoryRejectsUnverifiedDisabledForeignAndUnboundedClaims(t *testing.T) {
	phoneUser := func(uid, phone string) *auth.UserRecord {
		return &auth.UserRecord{UserInfo: &auth.UserInfo{UID: uid, PhoneNumber: phone}, ProviderUserInfo: []*auth.UserInfo{{ProviderID: "phone"}}}
	}
	valid := phoneUser("alice", "+919876543210")
	disabled := phoneUser("bob", "+919876543211")
	disabled.Disabled = true
	unverified := phoneUser("charlie", "+919876543212")
	unverified.ProviderUserInfo = []*auth.UserInfo{{ProviderID: "password"}}
	stub := &firebaseContactStub{result: &auth.GetUsersResult{Users: []*auth.UserRecord{valid, disabled, unverified}}}
	verifier, _ := NewFirebaseVerifier(stub)
	result, err := verifier.ContactPhones(context.Background(), []string{"alice", "bob", "charlie"})
	if err != nil || len(result) != 1 || result["alice"] != "+919876543210" {
		t.Fatalf("%+v %v", result, err)
	}
	valid.TenantID = "foreign-auth-tenant"
	if _, err = verifier.ContactPhones(context.Background(), []string{"alice", "bob", "charlie"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("foreign provider tenant accepted")
	}
	valid.TenantID = ""
	valid.UID = "unrequested-user"
	if _, err = verifier.ContactPhones(context.Background(), []string{"alice", "bob", "charlie"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("unrequested UID accepted")
	}
	stub.err = errors.New("provider unavailable")
	if _, err = verifier.ContactPhones(context.Background(), []string{"alice"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	for _, ids := range [][]string{nil, make([]string, 101), {"invalid uid"}} {
		calls := stub.calls
		if _, err = verifier.ContactPhones(context.Background(), ids); !errors.Is(err, ErrInvalidInput) || stub.calls != calls {
			t.Fatal("invalid lookup reached provider")
		}
	}
}

func TestContactHTTPFailsClosedAndRejectsClientAuthorityAndUnboundedPages(t *testing.T) {
	fixture, account, _ := linkingFixture(t)
	handler, _ := NewHandler(fixture.service, nil)
	headers := trustedHeaders(account)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/v1/me/contact-matching", "", 200},
		{"PUT", "/v1/me/contact-matching", `{"provider_token":"provider-token","policy_version":"v1","expected_revision":0,"phones":[]}`, 503},
		{"PUT", "/v1/me/contact-matching", `{"identity_id":"another-account","phones":[]}`, 422},
		{"PUT", "/v1/me/contact-matching?owner=another-account", `{}`, 422},
		{"GET", "/v1/me/contact-matching/matches?revision=1&limit=51", "", 503},
		{"GET", "/v1/me/contact-matching/matches?revision=1&limit=1&limit=2", "", 422},
		{"POST", "/v1/me/contact-matching", `{}`, 405},
	} {
		response := identityRequest(handler, test.method, test.path, test.body, headers)
		if response.Code != test.status {
			t.Fatalf("%s %s: %d %s", test.method, test.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "provider-token") {
			t.Fatal("private endpoint response unsafe")
		}
	}
	unauthenticated := identityRequest(handler, http.MethodGet, "/v1/me/contact-matching", "", nil)
	if unauthenticated.Code != 401 {
		t.Fatal("anonymous private contact state accepted")
	}
}
