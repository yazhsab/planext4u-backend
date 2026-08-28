package notification

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestServiceAccountTokenSourceSignsAndCachesShortLivedOAuthToken(t *testing.T) {
	t.Parallel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, _ := x509.MarshalPKCS8PrivateKey(privateKey)
	credentials, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "planext4u-staging",
		"client_email": "firebase-sender@planext4u-staging.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})),
		"token_uri":    googleOAuthTokenEndpoint,
	})
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(request.Body)
		values, _ := url.ParseQuery(string(body))
		parts := strings.Split(values.Get("assertion"), ".")
		if request.URL.String() != googleOAuthTokenEndpoint || len(parts) != 3 {
			t.Fatalf("OAuth request = %s assertion parts=%d", request.URL, len(parts))
		}
		claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		_ = json.Unmarshal(claimsJSON, &claims)
		if claims["scope"] != firebaseMessagingScope || claims["aud"] != googleOAuthTokenEndpoint {
			t.Fatalf("claims = %#v", claims)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"oauth_access_token_0000000000001","token_type":"Bearer","expires_in":3600}`))}, nil
	})}
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	source, err := NewServiceAccountTokenSource(credentials, client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if source.ProjectID() != "planext4u-staging" {
		t.Fatalf("source project=%q", source.ProjectID())
	}
	first, err := source.AccessToken(context.Background(), firebaseMessagingScope)
	second, secondErr := source.AccessToken(context.Background(), firebaseMessagingScope)
	if err != nil || secondErr != nil || first != second || calls != 1 {
		t.Fatalf("tokens equal=%v calls=%d errors=%v/%v", first == second, calls, err, secondErr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
