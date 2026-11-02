package fakeprovider

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SyntheticBankToken is a public local fixture credential, never a real bank
// secret. The fake-provider executable is excluded from production API images.
const SyntheticBankToken = "synthetic-local-bank-provider-only-token"

type syntheticBankScope struct {
	TenantID string `json:"tenant_id"`
	Country  string `json:"country"`
	OwnerID  string `json:"owner_id"`
}
type syntheticBankInput struct {
	syntheticBankScope
	ApplicationRole string `json:"application_role"`
	AttemptID       string `json:"attempt_id"`
	ProviderSession string `json:"provider_session_id,omitempty"`
}
type syntheticBankSession struct {
	input               syntheticBankInput
	capability          string
	status              string
	expires, verifiedAt time.Time
}
type syntheticBank struct {
	mu       sync.Mutex
	sessions map[string]*syntheticBankSession
}

var syntheticBankUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var syntheticBankPage = template.Must(template.New("bank").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Synthetic bank verification</title><body><h1>Local test bank verification</h1><p>This is a synthetic provider. Do not enter real bank details. No money is moved.</p>{{if .Finished}}<p>Test result saved. Return to Planext4u and check the result.</p>{{else}}<form method="post"><input type="hidden" name="cap" value="{{.Capability}}"><button name="result" value="VERIFIED">Simulate verified bank ending 1234</button><button name="result" value="DECLINED">Simulate declined verification</button></form>{{end}}</body></html>`))

func registerSyntheticBank(mux *http.ServeMux) {
	b := &syntheticBank{sessions: map[string]*syntheticBankSession{}}
	mux.HandleFunc("POST /v1/bank-accounts/link-sessions", b.command)
	mux.HandleFunc("POST /v1/bank-accounts/link-sessions:resolve", b.command)
	mux.HandleFunc("POST /v1/bank-accounts/link-sessions:cancel", b.command)
	mux.HandleFunc("POST /v1/bank-accounts/verification:resolve", b.proof)
	mux.HandleFunc("GET /synthetic/bank/{session_id}", b.page)
	mux.HandleFunc("POST /synthetic/bank/{session_id}", b.page)
}

func (b *syntheticBank) command(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+SyntheticBankToken {
		writeProblem(w, 401, "SYNTHETIC_BANK_AUTH_REQUIRED")
		return
	}
	var input syntheticBankInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !syntheticBankUUID.MatchString(input.TenantID) || !syntheticBankUUID.MatchString(input.OwnerID) || !syntheticBankUUID.MatchString(input.AttemptID) || input.Country != "IN" || (input.ApplicationRole != "VENDOR" && input.ApplicationRole != "RIDER") || r.Header.Get("Idempotency-Key") != input.AttemptID {
		writeProblem(w, 400, "SYNTHETIC_BANK_INPUT_INVALID")
		return
	}
	key := input.TenantID + ":" + input.Country + ":" + input.OwnerID + ":" + input.ApplicationRole + ":" + input.AttemptID
	digest := sha256.Sum256([]byte(key))
	id := "session_" + hex.EncodeToString(digest[:16])
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	cancel := strings.HasSuffix(r.URL.Path, ":cancel")
	resolve := strings.HasSuffix(r.URL.Path, ":resolve")
	if input.ProviderSession != "" && input.ProviderSession != id {
		writeProblem(w, 404, "SYNTHETIC_BANK_NOT_FOUND")
		return
	}
	if s == nil {
		if resolve {
			writeProblem(w, 404, "SYNTHETIC_BANK_NOT_FOUND")
			return
		}
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			writeProblem(w, 503, "SYNTHETIC_BANK_UNAVAILABLE")
			return
		}
		input.ProviderSession = id
		s = &syntheticBankSession{input: input, capability: hex.EncodeToString(random), status: "AWAITING_USER", expires: time.Now().UTC().Add(10 * time.Minute)}
		b.sessions[id] = s
	}
	if !s.expires.After(time.Now()) {
		s.status = "EXPIRED"
	}
	if cancel && s.status != "VERIFIED" && s.status != "DECLINED" && s.status != "EXPIRED" {
		s.status = "CANCELLED"
	}
	result := map[string]any{"tenant_id": input.TenantID, "country": input.Country, "owner_id": input.OwnerID, "application_role": input.ApplicationRole, "attempt_id": input.AttemptID, "provider_session_id": id, "status": s.status, "expires_at": s.expires}
	if s.status == "AWAITING_USER" {
		result["launch_url"] = "http://127.0.0.1:18081/synthetic/bank/" + id + "?cap=" + s.capability
	}
	if s.status == "VERIFIED" {
		result["account"] = syntheticBankAccount(id)
		result["verification_id"] = "verification_" + id
		result["verified_at"] = s.verifiedAt
	}
	writeJSON(w, 200, result)
}

func syntheticBankAccount(id string) map[string]any {
	return map[string]any{"reference": "bankref_" + strings.TrimPrefix(id, "session_"), "holder_name": "Synthetic Local Owner", "last4": "1234", "ifsc": "TEST0123456"}
}

func (b *syntheticBank) proof(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+SyntheticBankToken {
		writeProblem(w, 401, "SYNTHETIC_BANK_AUTH_REQUIRED")
		return
	}
	var input struct {
		syntheticBankScope
		Reference  string `json:"reference"`
		HolderName string `json:"holder_name,omitempty"`
		Last4      string `json:"last4,omitempty"`
		IFSC       string `json:"ifsc,omitempty"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !strings.HasPrefix(input.Reference, "bankref_") {
		writeProblem(w, 400, "SYNTHETIC_BANK_INPUT_INVALID")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := "session_" + strings.TrimPrefix(input.Reference, "bankref_")
	s := b.sessions[id]
	if s == nil || s.status != "VERIFIED" || s.input.syntheticBankScope != input.syntheticBankScope || time.Since(s.verifiedAt) > 15*time.Minute {
		writeProblem(w, 404, "SYNTHETIC_BANK_PROOF_NOT_FOUND")
		return
	}
	result := syntheticBankAccount(id)
	result["tenant_id"], result["country"], result["owner_id"] = input.TenantID, input.Country, input.OwnerID
	result["verification_id"], result["status"], result["verified_at"], result["expires_at"] = "verification_"+id, "VERIFIED", s.verifiedAt, s.verifiedAt.Add(time.Hour)
	writeJSON(w, 200, result)
}

func (b *syntheticBank) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	capability := r.URL.Query().Get("cap")
	if r.Method == http.MethodPost {
		if r.ParseForm() != nil || len(r.PostForm) != 2 {
			http.Error(w, "Invalid synthetic command", 400)
			return
		}
		capability = r.PostForm.Get("cap")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[r.PathValue("session_id")]
	if s == nil || len(capability) != 64 || subtle.ConstantTimeCompare([]byte(capability), []byte(s.capability)) != 1 || !s.expires.After(time.Now()) {
		http.Error(w, "Synthetic link unavailable", 404)
		return
	}
	if r.Method == http.MethodPost && s.status == "AWAITING_USER" {
		status := r.PostForm.Get("result")
		if status != "VERIFIED" && status != "DECLINED" {
			http.Error(w, "Invalid synthetic result", 400)
			return
		}
		s.status = status
		s.verifiedAt = time.Now().UTC()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = syntheticBankPage.Execute(w, struct {
		Capability string
		Finished   bool
	}{s.capability, s.status != "AWAITING_USER"})
}
