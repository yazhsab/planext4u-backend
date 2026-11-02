package adminshell

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCountryReviewerPrivateGrantRequiresCurrentSessionAndCSRF(t *testing.T) {
	var ownerCalls atomic.Int64
	var unsafe atomic.Bool
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerCalls.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 256))
		if err != nil || string(body) != `{"purpose":"ONBOARDING_REVIEW"}` || r.Header.Get("Content-Type") != "application/json" || r.Method != http.MethodPost || r.URL.Path != "/v1/vendor/applications/"+participantTestID+"/review/documents/OWNER_IDENTITY/access" || r.Header.Get("X-Planext4u-Roles") != "OPS_ADMIN" || r.Header.Get("X-Planext4u-Subject") != "10000000-0000-4000-8000-000000000001" || r.Header.Get("X-Planext4u-Operator-Session") != "40000000-0000-4000-8000-000000000001" || r.Header.Get("X-Planext4u-Operator-Revision") != "7" {
			t.Error("owner reviewer scope or stored-document path changed")
		}
		grantURL := "https://private.example/identity?signature=synthetic-secret"
		if unsafe.Load() {
			grantURL = "javascript:alert(1)"
		}
		_ = json.NewEncoder(w).Encode(ParticipantReviewAccess{Kind: "OWNER_IDENTITY", URL: grantURL, ExpiresAt: time.Now().UTC().Add(time.Minute)})
	}))
	defer owner.Close()
	endpoint, _ := url.Parse(owner.URL)
	reader, _ := NewHTTPFulfillmentReader(endpoint, &http.Client{Timeout: time.Second})
	handler, store := testHandler(t, true, reader)
	principal := testPrincipal(RoleCountryAdmin)
	principal.SubjectID = "10000000-0000-4000-8000-000000000001"
	principal.SessionID = "40000000-0000-4000-8000-000000000001"
	principal.OperatorRevision = 7
	token := issue(t, store, principal)
	var session SessionView
	if response := serve(handler, "GET", "/admin/api/v1/session", nil, token, "", ""); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil {
		t.Fatal("admin session unavailable")
	}
	path := "/admin/api/v1/participants/VENDOR/" + participantTestID + "/review/documents/OWNER_IDENTITY/access"
	request := func(path, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		r.Header.Set("Origin", "https://admin.planext4u.net")
		r.Header.Set("X-CSRF-Token", csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	if response := request(path, "wrong"); response.Code != 403 || ownerCalls.Load() != 0 {
		t.Fatalf("missing CSRF reached owner: %d calls=%d", response.Code, ownerCalls.Load())
	}
	if response := request(strings.Replace(path, "OWNER_IDENTITY", "../../forged", 1), session.CSRFToken); response.Code == 200 || ownerCalls.Load() != 0 {
		t.Fatal("invalid document selector reached owner")
	}
	response := request(path, session.CSRFToken)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || ownerCalls.Load() != 1 || strings.Contains(response.Body.String(), "asset_id") || !strings.Contains(response.Body.String(), "signature=") {
		t.Fatalf("grant response=%d body=%s calls=%d", response.Code, response.Body.String(), ownerCalls.Load())
	}
	unsafe.Store(true)
	if response = request(path, session.CSRFToken); response.Code == 200 || strings.Contains(response.Body.String(), "javascript:") {
		t.Fatalf("unsafe media URL returned: %d %s", response.Code, response.Body.String())
	}
	regional := testPrincipal(RoleRegionalStaff)
	regional.SubjectID = "10000000-0000-4000-8000-000000000003"
	regional.SessionID = "40000000-0000-4000-8000-000000000003"
	regional.OperatorRevision = 1
	regionalToken := issue(t, store, regional)
	before := ownerCalls.Load()
	if response = serve(handler, http.MethodPost, path, nil, regionalToken, "", ""); response.Code != 403 || ownerCalls.Load() != before {
		t.Fatalf("regional reviewer crossed country boundary: %d calls=%d", response.Code, ownerCalls.Load())
	}
}
