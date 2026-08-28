package notification

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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const googleOAuthTokenEndpoint = "https://oauth2.googleapis.com/token"

type ServiceAccountTokenSource struct {
	projectID   string
	clientEmail string
	privateKey  *rsa.PrivateKey
	client      *http.Client
	clock       func() time.Time
	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func NewServiceAccountTokenSource(credentials []byte, client *http.Client, clock func() time.Time) (*ServiceAccountTokenSource, error) {
	if len(credentials) == 0 || len(credentials) > 64*1024 || client == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	var value struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(credentials)))
	if decoder.Decode(&value) != nil || value.Type != "service_account" || value.TokenURI != googleOAuthTokenEndpoint ||
		!regexpProjectID.MatchString(value.ProjectID) || !validServiceAccountEmail(value.ClientEmail) {
		return nil, ErrInvalidRequest
	}
	block, _ := pem.Decode([]byte(value.PrivateKey))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, ErrInvalidRequest
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok || privateKey.N.BitLen() < 2048 {
		return nil, ErrInvalidRequest
	}
	return &ServiceAccountTokenSource{projectID: value.ProjectID, clientEmail: value.ClientEmail, privateKey: privateKey, client: client, clock: clock}, nil
}

func (source *ServiceAccountTokenSource) ProjectID() string { return source.projectID }

func (source *ServiceAccountTokenSource) AccessToken(ctx context.Context, scope string) (string, error) {
	if scope != firebaseMessagingScope {
		return "", ErrInvalidRequest
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	now := source.clock().UTC()
	if source.accessToken != "" && now.Add(5*time.Minute).Before(source.expiresAt) {
		return source.accessToken, nil
	}
	assertion, err := source.assertion(now, scope)
	if err != nil {
		return "", ErrInvalidRequest
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, googleOAuthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrProviderRetryable
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := source.client.Do(request)
	if err != nil {
		return "", ErrProviderRetryable
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 32*1024+1))
	if readErr != nil || len(body) > 32*1024 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", ErrProviderRetryable
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &token) != nil || token.TokenType != "Bearer" || len(token.AccessToken) < 20 || len(token.AccessToken) > 8192 || strings.ContainsAny(token.AccessToken, "\r\n") || token.ExpiresIn < 300 || token.ExpiresIn > 7200 {
		return "", ErrProviderRetryable
	}
	source.accessToken, source.expiresAt = token.AccessToken, now.Add(time.Duration(token.ExpiresIn)*time.Second)
	return source.accessToken, nil
}

func (source *ServiceAccountTokenSource) assertion(now time.Time, scope string) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": source.clientEmail, "scope": scope, "aud": googleOAuthTokenEndpoint,
		"iat": now.Unix(), "exp": now.Add(55 * time.Minute).Unix(),
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, source.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func validServiceAccountEmail(value string) bool {
	if len(value) < 6 || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parts := strings.Split(value, "@")
	return len(parts) == 2 && safeID(parts[0]) && strings.HasSuffix(parts[1], ".iam.gserviceaccount.com")
}

var regexpProjectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,29}$`)
