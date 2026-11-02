package bankverification

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type handlerApplication struct {
	calls                  int
	actor                  Actor
	role, operation, value string
	err                    error
}

func (s *handlerApplication) result(actor Actor, role, operation, value string) (LinkView, error) {
	s.calls++
	s.actor, s.role, s.operation, s.value = actor, role, operation, value
	return LinkView{AttemptID: linkTestInput().AttemptID, ApplicationRole: role, Status: "AWAITING_USER", ExpiresAt: linkTestNow, LaunchURL: "https://bank.example.test/link?secret=synthetic"}, s.err
}
func (s *handlerApplication) Start(_ context.Context, a Actor, r, k string) (LinkView, error) {
	return s.result(a, r, "start", k)
}
func (s *handlerApplication) Current(_ context.Context, a Actor, r string) (LinkView, error) {
	return s.result(a, r, "current", "")
}
func (s *handlerApplication) Resume(_ context.Context, a Actor, r, id string) (LinkView, error) {
	return s.result(a, r, "resume", id)
}
func (s *handlerApplication) Cancel(_ context.Context, a Actor, r, id string) (LinkView, error) {
	return s.result(a, r, "cancel", id)
}

func bankRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	input := linkTestInput()
	r.Header.Set("X-Planext4u-Tenant", input.TenantID)
	r.Header.Set("X-Planext4u-Country", input.Country)
	r.Header.Set("X-Planext4u-Subject", input.OwnerID)
	r.Header.Set("X-Planext4u-Session", "40000000-0000-4000-8000-000000000001")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "link-attempt-0001")
	return r
}

func TestBankLinkHTTPPathsBindRoleAndSourceIdentityAndNeverCacheLaunchCapabilities(t *testing.T) {
	for _, role := range []string{"vendor", "rider"} {
		for _, test := range []struct{ method, suffix, operation, body string }{
			{"POST", "", "start", "{}"}, {"GET", "/current", "current", ""},
			{"POST", "/" + linkTestInput().AttemptID + "/resume", "resume", "{}"},
			{"DELETE", "/" + linkTestInput().AttemptID, "cancel", ""},
		} {
			service := &handlerApplication{}
			h, _ := NewHandler(service)
			w := httptest.NewRecorder()
			r := bankRequest(test.method, "/v1/"+role+"/bank-links"+test.suffix, test.body)
			r.Header.Set("X-Planext4u-Roles", "SUPER_ADMIN")
			h.ServeHTTP(w, r)
			if w.Code != 200 || service.calls != 1 || service.operation != test.operation || service.role != strings.ToUpper(role) || service.actor.OwnerID != linkTestInput().OwnerID || service.actor.SessionID == "" {
				t.Fatalf("route=%s %s response=%d calls=%+v", test.method, r.URL.Path, w.Code, service)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("launch capability caching/referrer controls missing")
			}
		}
	}
}

func TestBankLinkHTTPRejectsClientAuthorityAndMalformedCommandsBeforeApplication(t *testing.T) {
	for _, body := range []string{`{"owner_id":"other"}`, `{"application_role":"ADMIN"}`, `{"provider_session_id":"client-session"}`, `{"account_number":"123456789012"}`, `{"verified":true}`, `null`, `[]`, `{} {}`, strings.Repeat(" ", 1025) + `{}`} {
		service := &handlerApplication{}
		h, _ := NewHandler(service)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, bankRequest("POST", "/v1/vendor/bank-links", body))
		if w.Code != 400 || service.calls != 0 {
			t.Fatalf("body accepted: %q response=%d calls=%d", body, w.Code, service.calls)
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("X-Planext4u-Session") },
		func(r *http.Request) { r.Header.Set("X-Planext4u-Subject", "anonymous") },
		func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		func(r *http.Request) { r.URL.RawQuery = "owner_id=other" },
		func(r *http.Request) { r.Header.Del("Idempotency-Key") },
	} {
		service := &handlerApplication{}
		h, _ := NewHandler(service)
		r := bankRequest("POST", "/v1/vendor/bank-links", "{}")
		mutate(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 || service.calls != 0 {
			t.Fatal("invalid trusted scope/command accepted")
		}
	}
}

func TestBankLinkHTTPErrorsNeverExposeProviderSecrets(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{ErrForbidden, 403}, {ErrInvalidRequest, 400}, {ErrNotFound, 404}, {ErrRejected, 409}, {ErrUnavailable, 503}, {errors.New("secret-token-and-private-launch-url"), 503},
	} {
		h, _ := NewHandler(&handlerApplication{err: test.err})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, bankRequest("POST", "/v1/vendor/bank-links", "{}"))
		if w.Code != test.status || strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "launch_url") {
			t.Fatalf("unsafe failure: %s", w.Body)
		}
	}
}
