package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/gateway"
)

func TestJWTIssuerProducesGatewayCompatibleRS256Claims(t *testing.T) {
	t.Parallel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC)
	issuer, err := NewJWTIssuer(JWTIssuerConfig{
		Issuer:    "https://identity.staging.planext4u.net",
		Audience:  "planext4u-mobile",
		KeyID:     "identity-2026-01",
		Key:       privateKey,
		AccessTTL: 10 * time.Minute,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	token, expiresAt, err := issuer.Issue(Principal{
		Subject:  "identity_synthetic_001",
		Session:  "session_synthetic_001",
		TenantID: "tenant_synthetic_001",
		Country:  "IN",
		DeviceID: "device_synthetic_001",
		Roles:    []Role{RoleVendor, RoleCustomer},
		AuthTime: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt != now.Add(10*time.Minute) {
		t.Fatalf("expiresAt = %v", expiresAt)
	}
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		t.Fatalf("JWT segments = %d", len(segments))
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" || header["kid"] != "identity-2026-01" {
		t.Fatalf("JWT header = %#v", header)
	}

	verifier, err := gateway.NewJWTVerifier(gateway.JWTVerifierConfig{
		Issuer:    "https://identity.staging.planext4u.net",
		Audience:  "planext4u-mobile",
		Keys:      map[string]*rsa.PublicKey{"identity-2026-01": &privateKey.PublicKey},
		ClockSkew: 30 * time.Second,
		Now:       func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("gateway Verify() error = %v", err)
	}
	if principal.Subject != "identity_synthetic_001" ||
		principal.SessionID != "session_synthetic_001" ||
		principal.TenantID != "tenant_synthetic_001" ||
		principal.Country != "IN" ||
		principal.DeviceID != "device_synthetic_001" ||
		strings.Join(principal.Roles, ",") != "CUSTOMER,VENDOR" {
		t.Fatalf("verified principal = %#v", principal)
	}
}

func TestRefreshTokensAreOpaqueHashedAndDomainSeparated(t *testing.T) {
	t.Parallel()
	factory := RandomRefreshTokenFactory{Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))}
	token, err := factory.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !validRefreshToken(token) || strings.Contains(token, "secret") {
		t.Fatalf("generated token is invalid: %q", token)
	}
	hasher, err := NewHMACRefreshHasher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hasher.Digest(token)
	if err != nil {
		t.Fatal(err)
	}
	deviceRef, err := hasher.DeviceReference(token)
	if err != nil {
		t.Fatal(err)
	}
	if digest == token || strings.Contains(digest, token) || strings.Contains(deviceRef, digest[:24]) {
		t.Fatalf("hashing did not separate restricted values: digest=%q device=%q", digest, deviceRef)
	}
	if _, err := hasher.Digest("not-a-refresh-token"); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("malformed digest error = %v", err)
	}
	if _, err := NewHMACRefreshHasher([]byte("short")); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("short HMAC key error = %v", err)
	}
}

func TestPrivateKeyParserAcceptsSupportedPEMAndRejectsUnsafeKeys(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	parsed, err := ParseRSAPrivateKeyPEM(pkcs1)
	if err != nil || parsed.N.Cmp(key.N) != 0 {
		t.Fatalf("parse PKCS#1 = %v", err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})
	if _, err := ParseRSAPrivateKeyPEM(pkcs8); err != nil {
		t.Fatalf("parse PKCS#8 = %v", err)
	}
	if _, err := ParseRSAPrivateKeyPEM(append(pkcs1, []byte("trailing")...)); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("trailing PEM error = %v", err)
	}
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	weakPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weakKey)})
	if _, err := ParseRSAPrivateKeyPEM(weakPEM); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("weak key error = %v", err)
	}
}
