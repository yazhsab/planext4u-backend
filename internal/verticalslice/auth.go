package verticalslice

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/gateway"
)

const (
	syntheticTenant  = "11111111-1111-4111-8111-111111111111"
	syntheticSubject = "customer-synthetic-001"
	accessTokenTTL   = 10 * time.Minute
)

type tokenClaims struct {
	Subject     string   `json:"sub"`
	SessionID   string   `json:"sid"`
	TenantID    string   `json:"tenant_id"`
	Country     string   `json:"country"`
	DeviceID    string   `json:"device_id"`
	Roles       []string `json:"roles"`
	MFAVerified bool     `json:"mfa_verified,omitempty"`
	IssuedAt    int64    `json:"iat"`
	ExpiresAt   int64    `json:"exp"`
}

type tokenService struct {
	key   []byte
	clock func() time.Time
}

func newTokenService(key []byte, clock func() time.Time) (*tokenService, error) {
	if len(key) < 32 || len(key) > 1024 || clock == nil {
		return nil, errors.New("invalid synthetic token configuration")
	}
	return &tokenService{key: append([]byte(nil), key...), clock: clock}, nil
}

func (service *tokenService) issue(deviceID, country string) (string, string, time.Time, error) {
	return service.issueFor(syntheticSubject, []string{"CUSTOMER"}, false, deviceID, country)
}

func (service *tokenService) issueFor(subject string, roles []string, mfa bool, deviceID, country string) (string, string, time.Time, error) {
	if !safeID(subject) || !safeID(deviceID) || country != "IN" || len(roles) != 1 || !safeID(roles[0]) {
		return "", "", time.Time{}, errors.New("invalid synthetic identity request")
	}
	sessionID, err := secureID("session")
	if err != nil {
		return "", "", time.Time{}, err
	}
	now := service.clock().UTC().Truncate(time.Second)
	expiresAt := now.Add(accessTokenTTL)
	claims := tokenClaims{
		Subject: subject, SessionID: sessionID, TenantID: syntheticTenant, Country: country,
		DeviceID: deviceID, Roles: append([]string(nil), roles...), MFAVerified: mfa, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := service.sign(encoded)
	refreshReference := service.sign("refresh\x00" + sessionID)
	return "p4us_v1." + encoded + "." + signature, "p4ur_synthetic_" + refreshReference, expiresAt, nil
}

func (service *tokenService) Verify(_ context.Context, token string) (gateway.Principal, error) {
	if len(token) < 64 || len(token) > 8192 {
		return gateway.Principal{}, gateway.ErrTokenInvalid
	}
	segments := strings.Split(token, ".")
	if len(segments) != 3 || segments[0] != "p4us_v1" {
		return gateway.Principal{}, gateway.ErrTokenInvalid
	}
	expected := service.sign(segments[1])
	if len(expected) != len(segments[2]) || !hmac.Equal([]byte(expected), []byte(segments[2])) {
		return gateway.Principal{}, gateway.ErrTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil || len(payload) > 4096 {
		return gateway.Principal{}, gateway.ErrTokenInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var claims tokenClaims
	if decoder.Decode(&claims) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validClaims(claims) {
		return gateway.Principal{}, gateway.ErrTokenInvalid
	}
	now := service.clock().UTC()
	if !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return gateway.Principal{}, gateway.ErrTokenExpired
	}
	if time.Unix(claims.IssuedAt, 0).After(now.Add(time.Minute)) || time.Unix(claims.ExpiresAt, 0).Sub(time.Unix(claims.IssuedAt, 0)) != accessTokenTTL {
		return gateway.Principal{}, gateway.ErrTokenInvalid
	}
	return gateway.Principal{
		Subject: claims.Subject, SessionID: claims.SessionID, TenantID: claims.TenantID,
		Country: claims.Country, DeviceID: claims.DeviceID, Roles: append([]string(nil), claims.Roles...),
		MFAVerified: claims.MFAVerified,
	}, nil
}

func (service *tokenService) sign(value string) string {
	mac := hmac.New(sha256.New, service.key)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type authHandler struct {
	tokens *tokenService
	clock  func() time.Time
}

func (handler authHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/auth/exchange" {
		writeAuthProblem(writer, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The requested resource was not found.")
		return
	}
	defer request.Body.Close()
	var input struct {
		Provider      string `json:"provider"`
		ProviderToken string `json:"provider_token"`
		DeviceID      string `json:"device_id"`
		Country       string `json:"country"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Provider != "local" {
		writeAuthProblem(writer, request, http.StatusUnauthorized, "PROVIDER_TOKEN_INVALID", "Sign in again to continue.")
		return
	}
	identity, ok := syntheticIdentities[input.ProviderToken]
	if !ok {
		writeAuthProblem(writer, request, http.StatusUnauthorized, "PROVIDER_TOKEN_INVALID", "Sign in again to continue.")
		return
	}
	accessToken, refreshToken, accessExpiresAt, err := handler.tokens.issueFor(identity.subject, []string{identity.role}, identity.mfa, input.DeviceID, strings.ToUpper(input.Country))
	if err != nil {
		writeAuthProblem(writer, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Check the submitted values.")
		return
	}
	now := handler.clock().UTC().Truncate(time.Second)
	writeAuthJSON(writer, http.StatusCreated, map[string]any{
		"identity_id": identity.subject,
		"tenant_id":   syntheticTenant,
		"country":     "IN",
		"tokens": map[string]any{
			"access_token": accessToken, "access_expires_at": accessExpiresAt,
			"refresh_token": refreshToken, "refresh_expires_at": now.Add(24 * time.Hour), "token_type": "Bearer",
		},
		"profile": map[string]any{"display_name": identity.displayName, "locale": "en", "time_zone": "Asia/Kolkata", "version": 1, "updated_at": now},
		"roles":   []string{identity.role},
		"session": map[string]any{
			"id": claimsSessionID(accessToken), "device_reference": "device-synthetic", "country": "IN",
			"authenticated_at": now, "last_seen_at": now, "expires_at": now.Add(24 * time.Hour), "current": true,
		},
	})
}

func claimsSessionID(token string) string {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return "session-unavailable"
	}
	payload, _ := base64.RawURLEncoding.DecodeString(segments[1])
	var claims tokenClaims
	_ = json.Unmarshal(payload, &claims)
	return claims.SessionID
}

func validClaims(claims tokenClaims) bool {
	return safeID(claims.Subject) && claims.TenantID == syntheticTenant && claims.Country == "IN" &&
		safeID(claims.SessionID) && safeID(claims.DeviceID) && len(claims.Roles) == 1 && safeID(claims.Roles[0]) &&
		claims.IssuedAt > 0 && claims.ExpiresAt > claims.IssuedAt
}

type syntheticIdentity struct {
	subject     string
	role        string
	displayName string
	mfa         bool
}

var syntheticIdentities = map[string]syntheticIdentity{
	"synthetic-customer":      {subject: "customer-synthetic-001", role: "CUSTOMER", displayName: "Synthetic Customer"},
	"synthetic-vendor":        {subject: "vendor-synthetic-001", role: "VENDOR", displayName: "Synthetic Vendor"},
	"synthetic-restaurant":    {subject: "restaurant-owner-001", role: "RESTAURANT_VENDOR", displayName: "Synthetic Restaurant"},
	"synthetic-rider":         {subject: "rider-synthetic-001", role: "RIDER", displayName: "Synthetic Rider"},
	"synthetic-ops-admin":     {subject: "ops-admin-001", role: "OPS_ADMIN", displayName: "Synthetic Operations Admin", mfa: true},
	"synthetic-field-officer": {subject: "field-officer-001", role: "FIELD_OFFICER", displayName: "Synthetic Field Officer"},
	"synthetic-finance-one":   {subject: "finance-approver-001", role: "FINANCE", displayName: "Synthetic Finance One", mfa: true},
	"synthetic-finance-two":   {subject: "finance-approver-002", role: "FINANCE", displayName: "Synthetic Finance Two", mfa: true},
	"synthetic-dispatch":      {subject: "dispatch-001", role: "DISPATCH", displayName: "Synthetic Dispatch", mfa: true},
	"synthetic-franchise":     {subject: "franchise-chennai-001", role: "FRANCHISE_ADMIN", displayName: "Synthetic Franchise", mfa: true},
}

func secureID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}

func writeAuthProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	writeAuthJSON(writer, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "correlation_id": request.Header.Get("X-Correlation-ID"),
		"retryable": false, "field_errors": []any{}, "details": map[string]any{},
	}})
}

func writeAuthJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
