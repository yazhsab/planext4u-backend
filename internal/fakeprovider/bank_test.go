package fakeprovider

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSyntheticBankRequiresExplicitHostedActionAndScopedIndependentProof(t *testing.T) {
	h := NewHandler()
	input := map[string]any{"tenant_id": "10000000-0000-4000-8000-000000000001", "owner_id": "20000000-0000-4000-8000-000000000001", "country": "IN", "application_role": "VENDOR", "attempt_id": "30000000-0000-4000-8000-000000000001"}
	call := func(path string, body any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		contents, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(contents))
		r.Header.Set("Idempotency-Key", input["attempt_id"].(string))
		if auth {
			r.Header.Set("Authorization", "Bearer "+SyntheticBankToken)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("/v1/bank-accounts/link-sessions", input, false); w.Code != 401 {
		t.Fatal("fixture service auth missing")
	}
	w := call("/v1/bank-accounts/link-sessions", input, true)
	var result map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["status"] != "AWAITING_USER" {
		t.Fatalf("create=%d %s", w.Code, w.Body)
	}
	id := result["provider_session_id"].(string)
	launch, _ := url.Parse(result["launch_url"].(string))
	input["provider_session_id"] = id
	w = call("/v1/bank-accounts/link-sessions:resolve", input, true)
	if !strings.Contains(w.Body.String(), `"status":"AWAITING_USER"`) || strings.Contains(w.Body.String(), `"account"`) {
		t.Fatal("create/resolve inferred verification without explicit hosted input")
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest("GET", launch.RequestURI(), nil))
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Do not enter real bank details") || page.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("synthetic hosted page/capability disclosure missing")
	}
	form := url.Values{"cap": {launch.Query().Get("cap")}, "result": {"VERIFIED"}}
	r := httptest.NewRequest("POST", launch.Path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	page = httptest.NewRecorder()
	h.ServeHTTP(page, r)
	if page.Code != 200 {
		t.Fatalf("hosted completion=%d", page.Code)
	}
	w = call("/v1/bank-accounts/link-sessions:resolve", input, true)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result["status"] != "VERIFIED" {
		t.Fatal("explicit provider result missing")
	}
	account := result["account"].(map[string]any)
	proof := map[string]any{"tenant_id": input["tenant_id"], "owner_id": input["owner_id"], "country": "IN", "reference": account["reference"]}
	if w = call("/v1/bank-accounts/verification:resolve", proof, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"last4":"1234"`) {
		t.Fatalf("independent proof=%d %s", w.Code, w.Body)
	}
	proof["owner_id"] = "20000000-0000-4000-8000-000000000002"
	if w = call("/v1/bank-accounts/verification:resolve", proof, true); w.Code != 404 {
		t.Fatal("another owner resolved fixture proof")
	}
	input["attempt_id"] = "30000000-0000-4000-8000-000000000002"
	delete(input, "provider_session_id")
	if w = call("/v1/bank-accounts/link-sessions:cancel", input, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"CANCELLED"`) || strings.Contains(w.Body.String(), "launch_url") {
		t.Fatal("cancelling an unissued attempt started verification")
	}
}
