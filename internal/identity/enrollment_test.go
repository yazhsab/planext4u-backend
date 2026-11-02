package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParticipantEnrollmentCannotGrantPrivilegedRoles(t *testing.T) {
	fixture := newServiceFixture(t)
	var id string
	for _, role := range []Role{RoleVendor, RoleRider, RoleVendor} {
		result, err := fixture.service.Exchange(context.Background(), ExchangeInput{Provider: "local", ProviderToken: "participant-account", DeviceID: "participant-device", Country: "IN", EnrollAs: role})
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id = result.IdentityID
		}
		if result.IdentityID != id {
			t.Fatal("duplicate account created")
		}
		found := false
		for _, granted := range result.Roles {
			if granted == RoleAdmin {
				t.Fatal("privileged role granted")
			}
			if granted == role {
				found = true
			}
		}
		if !found {
			t.Fatalf("participation missing: %+v", result.Roles)
		}
	}
	for _, role := range []Role{RoleAdmin, RoleGuest, "SUPER_ADMIN", "COUNTRY_ADMIN"} {
		if _, err := fixture.service.Exchange(context.Background(), ExchangeInput{Provider: "local", ProviderToken: "participant-account", DeviceID: "participant-device", Country: "IN", EnrollAs: role}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("privileged participation accepted: %s %v", role, err)
		}
	}
	handler, _ := NewHandler(fixture.service, nil)
	for _, input := range []struct{ path, body string }{
		{"/v1/auth/exchange", `{"provider":"local","provider_token":"participant-account","device_id":"participant-device","country":"IN","enroll_as":"ADMIN"}`},
		{"/v1/auth/enroll", `{"provider":"local","provider_token":"participant-account","device_id":"participant-device","country":"IN"}`},
	} {
		request := httptest.NewRequest(http.MethodPost, input.path, strings.NewReader(input.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 422 {
			t.Fatalf("path=%s code=%d %s", input.path, response.Code, response.Body.String())
		}
	}
}
