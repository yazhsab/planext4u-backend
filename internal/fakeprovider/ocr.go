package fakeprovider

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"
)

// SyntheticOCRToken is a public, local-only fixture credential. An exact
// document-set decision must be issued before the resolver can return proof.
const SyntheticOCRToken = "synthetic-local-ocr-provider-only-token"

var syntheticOCRKind = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
var syntheticOCRCountry = regexp.MustCompile(`^[A-Z]{2}$`)

type syntheticOCRDocument struct {
	Kind    string `json:"kind"`
	AssetID string `json:"asset_id"`
}

type syntheticOCRScope struct {
	TenantID         string                 `json:"tenant_id"`
	Country          string                 `json:"country"`
	OwnerID          string                 `json:"owner_id"`
	DocumentRevision int64                  `json:"document_revision"`
	Purpose          string                 `json:"purpose"`
	Documents        []syntheticOCRDocument `json:"documents"`
}

type syntheticOCRDecision struct {
	Scope      syntheticOCRScope
	Status     string
	ID         string
	VerifiedAt time.Time
	ExpiresAt  time.Time
}

type syntheticOCR struct {
	mu        sync.Mutex
	decisions map[string]syntheticOCRDecision
}

func registerSyntheticOCR(mux *http.ServeMux) {
	provider := &syntheticOCR{decisions: make(map[string]syntheticOCRDecision)}
	mux.HandleFunc("POST /synthetic/documents/ocr-attestations", provider.issue)
	mux.HandleFunc("POST /v1/documents/ocr-verification:resolve", provider.resolve)
}

func validSyntheticOCRScope(scope syntheticOCRScope) bool {
	if !syntheticBankUUID.MatchString(scope.TenantID) || !syntheticBankUUID.MatchString(scope.OwnerID) ||
		!syntheticOCRCountry.MatchString(scope.Country) || scope.Purpose != "VENDOR_ONBOARDING_OCR" ||
		scope.DocumentRevision < 1 || len(scope.Documents) == 0 || len(scope.Documents) > 10 {
		return false
	}
	kinds := make(map[string]bool, len(scope.Documents))
	assets := make(map[string]bool, len(scope.Documents))
	for _, document := range scope.Documents {
		if !syntheticOCRKind.MatchString(document.Kind) || !syntheticBankUUID.MatchString(document.AssetID) ||
			kinds[document.Kind] || assets[document.AssetID] {
			return false
		}
		kinds[document.Kind], assets[document.AssetID] = true, true
	}
	return true
}

func canonicalSyntheticOCR(scope syntheticOCRScope) syntheticOCRScope {
	scope.Documents = append([]syntheticOCRDocument(nil), scope.Documents...)
	sort.Slice(scope.Documents, func(i, j int) bool { return scope.Documents[i].Kind < scope.Documents[j].Kind })
	return scope
}

func syntheticOCRKey(scope syntheticOCRScope) string {
	encoded, _ := json.Marshal(scope)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func syntheticOCRAuthenticated(request *http.Request) bool {
	value := request.Header.Get("Authorization")
	expected := "Bearer " + SyntheticOCRToken
	return len(value) == len(expected) && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}

func readSyntheticOCR(request *http.Request) (syntheticOCRScope, bool) {
	var scope syntheticOCRScope
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&scope) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validSyntheticOCRScope(scope) {
		return syntheticOCRScope{}, false
	}
	return canonicalSyntheticOCR(scope), true
}

func (provider *syntheticOCR) issue(writer http.ResponseWriter, request *http.Request) {
	if !syntheticOCRAuthenticated(request) {
		writeProblem(writer, http.StatusUnauthorized, "SYNTHETIC_OCR_AUTH_REQUIRED")
		return
	}
	var command struct {
		syntheticOCRScope
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validSyntheticOCRScope(command.syntheticOCRScope) ||
		(command.Status != "VERIFIED" && command.Status != "REJECTED") {
		writeProblem(writer, http.StatusBadRequest, "SYNTHETIC_OCR_INPUT_INVALID")
		return
	}
	scope := canonicalSyntheticOCR(command.syntheticOCRScope)
	key := syntheticOCRKey(scope)
	now := time.Now().UTC()
	decision := syntheticOCRDecision{Scope: scope, Status: command.Status,
		ID: "ocr_attestation_" + key[:32], VerifiedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	provider.mu.Lock()
	provider.decisions[key] = decision
	provider.mu.Unlock()
	writeJSON(writer, http.StatusCreated, map[string]any{
		"attestation_id": decision.ID, "status": decision.Status, "expires_at": decision.ExpiresAt,
	})
}

func (provider *syntheticOCR) resolve(writer http.ResponseWriter, request *http.Request) {
	if !syntheticOCRAuthenticated(request) {
		writeProblem(writer, http.StatusUnauthorized, "SYNTHETIC_OCR_AUTH_REQUIRED")
		return
	}
	scope, valid := readSyntheticOCR(request)
	if !valid {
		writeProblem(writer, http.StatusBadRequest, "SYNTHETIC_OCR_INPUT_INVALID")
		return
	}
	provider.mu.Lock()
	decision, exists := provider.decisions[syntheticOCRKey(scope)]
	provider.mu.Unlock()
	if !exists || !decision.ExpiresAt.After(time.Now()) {
		writeProblem(writer, http.StatusNotFound, "SYNTHETIC_OCR_PROOF_NOT_FOUND")
		return
	}
	if decision.Status != "VERIFIED" {
		writeProblem(writer, http.StatusForbidden, "SYNTHETIC_OCR_REJECTED")
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		syntheticOCRScope
		AttestationID string    `json:"attestation_id"`
		Status        string    `json:"status"`
		VerifiedAt    time.Time `json:"verified_at"`
		ExpiresAt     time.Time `json:"expires_at"`
	}{decision.Scope, decision.ID, decision.Status, decision.VerifiedAt, decision.ExpiresAt})
}
