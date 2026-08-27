package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"sync"
	"testing"
	"time"
)

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
	testKeyErr  error
)

func gatewayTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		testKey, testKeyErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if testKeyErr != nil {
		t.Fatalf("generate RSA key: %v", testKeyErr)
	}
	return testKey
}

func TestJWTVerifierAcceptsValidRS256Claims(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_788_000_000, 0)
	verifier := newTestJWTVerifier(t, now)
	token := signGatewayToken(t, gatewayTestKey(t), "test-key", validClaims(now))

	principal, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if principal.Subject != "customer-synthetic-001" ||
		principal.SessionID != "session-synthetic-001" ||
		principal.TenantID != "tenant-synthetic-001" ||
		principal.Country != "IN" ||
		principal.DeviceID != "device-synthetic-001" ||
		len(principal.Roles) != 1 || principal.Roles[0] != "CUSTOMER" {
		t.Fatalf("Verify() principal = %#v", principal)
	}
}

func TestJWTVerifierRejectsUnsafeOrInvalidTokens(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_788_000_000, 0)
	key := gatewayTestKey(t)
	verifier := newTestJWTVerifier(t, now)

	tests := []struct {
		name      string
		token     func() string
		wantError error
	}{
		{
			name: "expired",
			token: func() string {
				claims := validClaims(now)
				claims["exp"] = now.Add(-2 * time.Minute).Unix()
				return signGatewayToken(t, key, "test-key", claims)
			},
			wantError: ErrTokenExpired,
		},
		{
			name: "wrong audience",
			token: func() string {
				claims := validClaims(now)
				claims["aud"] = "another-client"
				return signGatewayToken(t, key, "test-key", claims)
			},
			wantError: ErrTokenInvalid,
		},
		{
			name: "future not-before",
			token: func() string {
				claims := validClaims(now)
				claims["nbf"] = now.Add(10 * time.Minute).Unix()
				return signGatewayToken(t, key, "test-key", claims)
			},
			wantError: ErrTokenInvalid,
		},
		{
			name: "unknown key",
			token: func() string {
				return signGatewayToken(t, key, "retired-key", validClaims(now))
			},
			wantError: ErrTokenInvalid,
		},
		{
			name: "unsigned algorithm",
			token: func() string {
				header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
				claims, _ := json.Marshal(validClaims(now))
				return encodeSegment(header) + "." + encodeSegment(claims) + "."
			},
			wantError: ErrTokenInvalid,
		},
		{
			name: "tampered payload",
			token: func() string {
				signed := signGatewayToken(t, key, "test-key", validClaims(now))
				segments := splitToken(t, signed)
				claims := validClaims(now)
				claims["roles"] = []string{"ADMIN"}
				payload, _ := json.Marshal(claims)
				return segments[0] + "." + encodeSegment(payload) + "." + segments[2]
			},
			wantError: ErrTokenInvalid,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := verifier.Verify(context.Background(), test.token())
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Verify() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestParseRSAPublicKeyPEM(t *testing.T) {
	t.Parallel()

	encoded, err := x509.MarshalPKIXPublicKey(&gatewayTestKey(t).PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	contents := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded})
	parsed, err := ParseRSAPublicKeyPEM(contents)
	if err != nil {
		t.Fatalf("ParseRSAPublicKeyPEM() error = %v", err)
	}
	if parsed.N.Cmp(gatewayTestKey(t).N) != 0 {
		t.Fatal("parsed public key differs from source key")
	}
	if _, err := ParseRSAPublicKeyPEM([]byte("not a key")); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid PEM error = %v", err)
	}
}

func newTestJWTVerifier(t *testing.T, now time.Time) *JWTVerifier {
	t.Helper()
	verifier, err := NewJWTVerifier(JWTVerifierConfig{
		Issuer:    "https://identity.staging.planext4u.net",
		Audience:  "planext4u-mobile",
		Keys:      map[string]*rsa.PublicKey{"test-key": &gatewayTestKey(t).PublicKey},
		ClockSkew: 30 * time.Second,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewJWTVerifier() error = %v", err)
	}
	return verifier
}

func validClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":       "https://identity.staging.planext4u.net",
		"aud":       []string{"planext4u-mobile", "planext4u-api"},
		"sub":       "customer-synthetic-001",
		"sid":       "session-synthetic-001",
		"tenant_id": "tenant-synthetic-001",
		"country":   "IN",
		"device_id": "device-synthetic-001",
		"roles":     []string{"CUSTOMER"},
		"iat":       now.Add(-time.Minute).Unix(),
		"nbf":       now.Add(-time.Minute).Unix(),
		"exp":       now.Add(15 * time.Minute).Unix(),
	}
}

func signGatewayToken(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	headerBytes, err := json.Marshal(map[string]string{"alg": "RS256", "kid": keyID, "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := encodeSegment(headerBytes) + "." + encodeSegment(claimsBytes)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signingInput + "." + encodeSegment(signature)
}

func encodeSegment(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func splitToken(t *testing.T, token string) []string {
	t.Helper()
	segments := make([]string, 0, 3)
	start := 0
	for index := 0; index <= len(token); index++ {
		if index == len(token) || token[index] == '.' {
			segments = append(segments, token[start:index])
			start = index + 1
		}
	}
	if len(segments) != 3 {
		t.Fatalf("token has %d segments", len(segments))
	}
	return segments
}
