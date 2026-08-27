package gateway

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidConfiguration = errors.New("invalid gateway configuration")
	ErrTokenInvalid         = errors.New("token invalid")
	ErrTokenExpired         = errors.New("token expired")
)

const maxTokenBytes = 8 * 1024

type Principal struct {
	Subject   string
	SessionID string
	TenantID  string
	Country   string
	DeviceID  string
	Roles     []string
}

type Verifier interface {
	Verify(context.Context, string) (Principal, error)
}

type JWTVerifierConfig struct {
	Issuer    string
	Audience  string
	Keys      map[string]*rsa.PublicKey
	ClockSkew time.Duration
	Now       func() time.Time
}

type JWTVerifier struct {
	config JWTVerifierConfig
}

func NewJWTVerifier(config JWTVerifierConfig) (*JWTVerifier, error) {
	if strings.TrimSpace(config.Issuer) == "" ||
		strings.TrimSpace(config.Audience) == "" ||
		len(config.Keys) == 0 ||
		config.ClockSkew < 0 || config.ClockSkew > 5*time.Minute {
		return nil, ErrInvalidConfiguration
	}
	keys := make(map[string]*rsa.PublicKey, len(config.Keys))
	for keyID, key := range config.Keys {
		if !validIdentifier(keyID, 128) || key == nil || key.N.BitLen() < 2048 {
			return nil, ErrInvalidConfiguration
		}
		keys[keyID] = &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
	}
	config.Keys = keys
	if config.Now == nil {
		config.Now = time.Now
	}
	return &JWTVerifier{config: config}, nil
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type jwtClaims struct {
	Issuer    string        `json:"iss"`
	Audience  audienceClaim `json:"aud"`
	Subject   string        `json:"sub"`
	SessionID string        `json:"sid"`
	TenantID  string        `json:"tenant_id"`
	Country   string        `json:"country"`
	DeviceID  string        `json:"device_id"`
	Roles     []string      `json:"roles"`
	ExpiresAt json.Number   `json:"exp"`
	NotBefore json.Number   `json:"nbf"`
	IssuedAt  json.Number   `json:"iat"`
}

type audienceClaim []string

func (audience *audienceClaim) UnmarshalJSON(value []byte) error {
	var single string
	if err := json.Unmarshal(value, &single); err == nil {
		*audience = []string{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(value, &multiple); err != nil {
		return ErrTokenInvalid
	}
	*audience = multiple
	return nil
}

func (verifier *JWTVerifier) Verify(_ context.Context, token string) (Principal, error) {
	if len(token) == 0 || len(token) > maxTokenBytes {
		return Principal{}, ErrTokenInvalid
	}
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return Principal{}, ErrTokenInvalid
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		return Principal{}, ErrTokenInvalid
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil ||
		header.Algorithm != "RS256" ||
		(header.Type != "" && header.Type != "JWT") {
		return Principal{}, ErrTokenInvalid
	}
	key, exists := verifier.config.Keys[header.KeyID]
	if !exists {
		return Principal{}, ErrTokenInvalid
	}

	signature, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		return Principal{}, ErrTokenInvalid
	}
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return Principal{}, ErrTokenInvalid
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return Principal{}, ErrTokenInvalid
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return Principal{}, ErrTokenInvalid
	}
	if err := verifier.validateClaims(claims); err != nil {
		return Principal{}, err
	}

	return Principal{
		Subject:   claims.Subject,
		SessionID: claims.SessionID,
		TenantID:  claims.TenantID,
		Country:   claims.Country,
		DeviceID:  claims.DeviceID,
		Roles:     append([]string(nil), claims.Roles...),
	}, nil
}

func (verifier *JWTVerifier) validateClaims(claims jwtClaims) error {
	if claims.Issuer != verifier.config.Issuer || !containsString(claims.Audience, verifier.config.Audience) {
		return ErrTokenInvalid
	}
	if !validIdentifier(claims.Subject, 128) ||
		!validIdentifier(claims.SessionID, 128) ||
		!validIdentifier(claims.TenantID, 128) ||
		(claims.DeviceID != "" && !validIdentifier(claims.DeviceID, 128)) ||
		!validCountry(claims.Country) ||
		len(claims.Roles) == 0 || len(claims.Roles) > 8 {
		return ErrTokenInvalid
	}
	for _, role := range claims.Roles {
		if !validRole(role) {
			return ErrTokenInvalid
		}
	}

	now := verifier.config.Now()
	expiresAt, ok := numericDate(claims.ExpiresAt)
	if !ok {
		return ErrTokenInvalid
	}
	if !now.Before(expiresAt.Add(verifier.config.ClockSkew)) {
		return ErrTokenExpired
	}
	if claims.NotBefore != "" {
		notBefore, ok := numericDate(claims.NotBefore)
		if !ok || now.Add(verifier.config.ClockSkew).Before(notBefore) {
			return ErrTokenInvalid
		}
	}
	if claims.IssuedAt != "" {
		issuedAt, ok := numericDate(claims.IssuedAt)
		if !ok || issuedAt.After(now.Add(verifier.config.ClockSkew)) {
			return ErrTokenInvalid
		}
	}
	return nil
}

func ParseRSAPublicKeyPEM(contents []byte) (*rsa.PublicKey, error) {
	block, trailing := pem.Decode(contents)
	if block == nil || len(strings.TrimSpace(string(trailing))) != 0 {
		return nil, fmt.Errorf("parse public key: %w", ErrInvalidConfiguration)
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PublicKey)
		if !ok || key.N.BitLen() < 2048 {
			return nil, fmt.Errorf("public key must be RSA-2048 or stronger: %w", ErrInvalidConfiguration)
		}
		return key, nil
	}
	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil || key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("parse RSA public key: %w", ErrInvalidConfiguration)
	}
	return key, nil
}

func numericDate(number json.Number) (time.Time, bool) {
	if number == "" {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func validIdentifier(value string, maxLength int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLength {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validCountry(value string) bool {
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}

func validRole(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && character != '_' {
			return false
		}
	}
	return true
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
