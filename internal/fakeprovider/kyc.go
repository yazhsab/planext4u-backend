package fakeprovider

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// SyntheticKYCToken is a public, local-only fixture credential. The fake
// provider is excluded from production images. A matching attestation must be
// explicitly issued for each document set before verification can succeed.
const SyntheticKYCToken = "synthetic-local-kyc-provider-only-token"

type syntheticKYCDocument struct {
	Kind    string `json:"kind"`
	AssetID string `json:"asset_id"`
}

type syntheticKYCScope struct {
	TenantID  string                 `json:"tenant_id"`
	Country   string                 `json:"country"`
	OwnerID   string                 `json:"owner_id"`
	Purpose   string                 `json:"purpose"`
	Documents []syntheticKYCDocument `json:"documents"`
}

type syntheticKYCAttestation struct {
	Scope      syntheticKYCScope
	Status     string
	ID         string
	VerifiedAt time.Time
	ExpiresAt  time.Time
}

type syntheticKYC struct {
	mu           sync.Mutex
	attestations map[string]syntheticKYCAttestation
}

func registerSyntheticKYC(mux *http.ServeMux) {
	provider := &syntheticKYC{attestations: make(map[string]syntheticKYCAttestation)}
	mux.HandleFunc("POST /synthetic/identity-documents/attestations", provider.issue)
	mux.HandleFunc("POST /v1/identity-documents/verification:resolve", provider.resolve)
}

func validSyntheticKYCScope(input syntheticKYCScope) bool {
	if !syntheticBankUUID.MatchString(input.TenantID) || !syntheticBankUUID.MatchString(input.OwnerID) || input.Country != "IN" || input.Purpose != "ONBOARDING_KYC" || len(input.Documents) == 0 || len(input.Documents) > 16 {
		return false
	}
	kinds, assets := make(map[string]bool, len(input.Documents)), make(map[string]bool, len(input.Documents))
	for _, document := range input.Documents {
		if len(document.Kind) < 2 || len(document.Kind) > 64 || document.Kind[0] < 'A' || document.Kind[0] > 'Z' || !syntheticBankUUID.MatchString(document.AssetID) || kinds[document.Kind] || assets[document.AssetID] {
			return false
		}
		for _, char := range document.Kind[1:] {
			if char != '_' && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
				return false
			}
		}
		kinds[document.Kind], assets[document.AssetID] = true, true
	}
	return true
}

func readSyntheticKYC(r *http.Request) (syntheticKYCScope, bool) {
	var input syntheticKYCScope
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validSyntheticKYCScope(input) {
		return syntheticKYCScope{}, false
	}
	sort.Slice(input.Documents, func(i, j int) bool { return input.Documents[i].Kind < input.Documents[j].Kind })
	return input, true
}

func syntheticKYCKey(scope syntheticKYCScope) string {
	encoded, _ := json.Marshal(scope)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func syntheticKYCAuthenticated(r *http.Request) bool {
	value := r.Header.Get("Authorization")
	expected := "Bearer " + SyntheticKYCToken
	return len(value) == len(expected) && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}

func (provider *syntheticKYC) issue(w http.ResponseWriter, r *http.Request) {
	if !syntheticKYCAuthenticated(r) {
		writeProblem(w, http.StatusUnauthorized, "SYNTHETIC_KYC_AUTH_REQUIRED")
		return
	}
	var command struct {
		syntheticKYCScope
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validSyntheticKYCScope(command.syntheticKYCScope) || (command.Status != "VERIFIED" && command.Status != "REJECTED") {
		writeProblem(w, http.StatusBadRequest, "SYNTHETIC_KYC_INPUT_INVALID")
		return
	}
	sort.Slice(command.Documents, func(i, j int) bool { return command.Documents[i].Kind < command.Documents[j].Kind })
	key := syntheticKYCKey(command.syntheticKYCScope)
	now := time.Now().UTC()
	result := syntheticKYCAttestation{Scope: command.syntheticKYCScope, Status: command.Status, ID: "attestation_" + key[:32], VerifiedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	provider.mu.Lock()
	provider.attestations[key] = result
	provider.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"attestation_id": result.ID, "status": result.Status, "expires_at": result.ExpiresAt})
}

func (provider *syntheticKYC) resolve(w http.ResponseWriter, r *http.Request) {
	if !syntheticKYCAuthenticated(r) {
		writeProblem(w, http.StatusUnauthorized, "SYNTHETIC_KYC_AUTH_REQUIRED")
		return
	}
	input, valid := readSyntheticKYC(r)
	if !valid {
		writeProblem(w, http.StatusBadRequest, "SYNTHETIC_KYC_INPUT_INVALID")
		return
	}
	provider.mu.Lock()
	result, exists := provider.attestations[syntheticKYCKey(input)]
	provider.mu.Unlock()
	if !exists || !result.ExpiresAt.After(time.Now()) {
		writeProblem(w, http.StatusNotFound, "SYNTHETIC_KYC_PROOF_NOT_FOUND")
		return
	}
	if result.Status != "VERIFIED" {
		writeProblem(w, http.StatusForbidden, "SYNTHETIC_KYC_REJECTED")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		syntheticKYCScope
		AttestationID string    `json:"attestation_id"`
		Status        string    `json:"status"`
		VerifiedAt    time.Time `json:"verified_at"`
		ExpiresAt     time.Time `json:"expires_at"`
	}{result.Scope, result.ID, result.Status, result.VerifiedAt, result.ExpiresAt})
}
