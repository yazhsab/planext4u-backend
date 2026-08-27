package identity

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type JWTIssuerConfig struct {
	Issuer    string
	Audience  string
	KeyID     string
	Key       *rsa.PrivateKey
	AccessTTL time.Duration
	Now       func() time.Time
	Random    io.Reader
}

type JWTIssuer struct {
	config JWTIssuerConfig
}

func NewJWTIssuer(config JWTIssuerConfig) (*JWTIssuer, error) {
	if strings.TrimSpace(config.Issuer) == "" ||
		strings.TrimSpace(config.Audience) == "" ||
		!validSafeIdentifier(config.KeyID, 128) ||
		config.Key == nil || config.Key.N.BitLen() < 2048 ||
		config.AccessTTL < time.Minute || config.AccessTTL > 15*time.Minute {
		return nil, ErrInvalidConfiguration
	}
	if err := config.Key.Validate(); err != nil {
		return nil, ErrInvalidConfiguration
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	return &JWTIssuer{config: config}, nil
}

type accessTokenClaims struct {
	Issuer     string   `json:"iss"`
	Audience   string   `json:"aud"`
	Subject    string   `json:"sub"`
	SessionID  string   `json:"sid"`
	TenantID   string   `json:"tenant_id"`
	Country    string   `json:"country"`
	DeviceID   string   `json:"device_id,omitempty"`
	Roles      []string `json:"roles"`
	AuthTime   int64    `json:"auth_time"`
	IssuedAt   int64    `json:"iat"`
	NotBefore  int64    `json:"nbf"`
	ExpiresAt  int64    `json:"exp"`
	TokenID    string   `json:"jti"`
	Assurance  string   `json:"acr"`
	AuthMethod []string `json:"amr"`
}

func (issuer *JWTIssuer) Issue(principal Principal) (string, time.Time, error) {
	if !validSafeIdentifier(principal.Subject, 128) ||
		!validSafeIdentifier(principal.Session, 128) ||
		!validSafeIdentifier(principal.TenantID, 128) ||
		!validCountry(principal.Country) ||
		(principal.DeviceID != "" && !validSafeIdentifier(principal.DeviceID, 128)) ||
		len(principal.Roles) == 0 || len(principal.Roles) > 8 {
		return "", time.Time{}, ErrInvalidInput
	}
	roles := make([]string, len(principal.Roles))
	for index, role := range principal.Roles {
		if !validRole(role) {
			return "", time.Time{}, ErrInvalidInput
		}
		roles[index] = string(role)
	}
	sort.Strings(roles)
	now := issuer.config.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(issuer.config.AccessTTL)
	tokenID, err := randomIdentifier(issuer.config.Random, "atk", 16)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create access token identifier: %w", err)
	}
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}{Algorithm: "RS256", KeyID: issuer.config.KeyID, Type: "JWT"})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode access token header: %w", err)
	}
	claims, err := json.Marshal(accessTokenClaims{
		Issuer:     issuer.config.Issuer,
		Audience:   issuer.config.Audience,
		Subject:    principal.Subject,
		SessionID:  principal.Session,
		TenantID:   principal.TenantID,
		Country:    principal.Country,
		DeviceID:   principal.DeviceID,
		Roles:      roles,
		AuthTime:   principal.AuthTime.UTC().Unix(),
		IssuedAt:   now.Unix(),
		NotBefore:  now.Unix(),
		ExpiresAt:  expiresAt.Unix(),
		TokenID:    tokenID,
		Assurance:  "urn:planext4u:loa:provider",
		AuthMethod: []string{"provider_token"},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode access token claims: %w", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(issuer.config.Random, issuer.config.Key, crypto.SHA256, digest[:])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), expiresAt, nil
}

type RandomRefreshTokenFactory struct {
	Random io.Reader
}

func (factory RandomRefreshTokenFactory) Generate() (string, error) {
	randomSource := factory.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(randomSource, value); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return "p4ur_v1_" + base64.RawURLEncoding.EncodeToString(value), nil
}

type HMACRefreshHasher struct {
	key []byte
}

func NewHMACRefreshHasher(key []byte) (*HMACRefreshHasher, error) {
	if len(key) < 32 || len(key) > 1024 {
		return nil, ErrInvalidConfiguration
	}
	return &HMACRefreshHasher{key: append([]byte(nil), key...)}, nil
}

func (hasher *HMACRefreshHasher) Digest(token string) (string, error) {
	if !validRefreshToken(token) {
		return "", ErrRefreshInvalid
	}
	return hasher.digest("refresh", token), nil
}

func (hasher *HMACRefreshHasher) DeviceReference(deviceID string) (string, error) {
	if !validSafeIdentifier(deviceID, 128) {
		return "", ErrInvalidInput
	}
	return "device_" + hasher.digest("device", deviceID)[:24], nil
}

func (hasher *HMACRefreshHasher) digest(purpose, value string) string {
	mac := hmac.New(sha256.New, hasher.key)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func ParseRSAPrivateKeyPEM(contents []byte) (*rsa.PrivateKey, error) {
	block, trailing := pem.Decode(contents)
	if block == nil || len(strings.TrimSpace(string(trailing))) != 0 {
		return nil, ErrInvalidConfiguration
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		if key.N.BitLen() < 2048 || key.Validate() != nil {
			return nil, ErrInvalidConfiguration
		}
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, ErrInvalidConfiguration
	}
	return key, nil
}

func randomIdentifier(randomSource io.Reader, prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(randomSource, value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

func validRefreshToken(token string) bool {
	const prefix = "p4ur_v1_"
	if !strings.HasPrefix(token, prefix) || len(token) > 128 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, prefix))
	return err == nil && len(decoded) == 32
}

func validRole(role Role) bool {
	switch role {
	case RoleCustomer, RoleVendor, RoleRider, RoleAdmin:
		return true
	default:
		return false
	}
}

func validCountry(country string) bool {
	return len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z'
}

func validSafeIdentifier(value string, maxLength int) bool {
	if value == "" || len(value) > maxLength || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validOpaqueToken(value string, minLength, maxLength int) bool {
	if len(value) < minLength || len(value) > maxLength {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
