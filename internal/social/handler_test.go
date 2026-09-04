package social

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBEP5001SocialHTTPStrictAuthRevisionAndMFA(t *testing.T) {
	service := testService(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}

	feed := socialRequest(t, handler, http.MethodGet, "/v1/social/feed?limit=1", "", "customer-synthetic-001", "CUSTOMER", false, "", "")
	if feed.Code != http.StatusOK || feed.Header().Get("Cache-Control") != "no-store" || !strings.Contains(feed.Body.String(), `"ranking_version":"socio-feed-v1"`) {
		t.Fatalf("feed status=%d headers=%v body=%s", feed.Code, feed.Header(), feed.Body.String())
	}
	profile := socialRequest(t, handler, http.MethodGet, "/v1/social/profile-handles/PUBLIC_USER", "", "customer-synthetic-001", "CUSTOMER", false, "", "")
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), `"handle":"public_user"`) {
		t.Fatalf("handle status=%d body=%s", profile.Code, profile.Body.String())
	}
	content := socialRequest(t, handler, http.MethodGet, "/v1/social/profiles/customer-public-001/content?kind=POSTS&limit=1", "", "customer-synthetic-001", "CUSTOMER", false, "", "")
	if content.Code != http.StatusOK || !strings.Contains(content.Body.String(), `"items"`) {
		t.Fatalf("content status=%d body=%s", content.Code, content.Body.String())
	}
	tooManyComments := socialRequest(t, handler, http.MethodGet, "/v1/social/posts/social-post-public-001/comments?limit=51", "", "customer-synthetic-001", "CUSTOMER", false, "", "")
	if tooManyComments.Code != http.StatusUnprocessableEntity {
		t.Fatalf("comments limit status=%d body=%s", tooManyComments.Code, tooManyComments.Body.String())
	}

	invalid := socialRequest(t, handler, http.MethodPost, "/v1/social/posts", `{"body":"hello","media_asset_ids":[],"unknown":true}`, "customer-synthetic-001", "CUSTOMER", false, "post-http-key-0001", "")
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "SOCIAL_REQUEST_INVALID") {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	created := socialRequest(t, handler, http.MethodPost, "/v1/social/posts", `{"body":"Hello #local","media_asset_ids":[]}`, "customer-synthetic-001", "CUSTOMER", false, "post-http-key-0002", "")
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	replay := socialRequest(t, handler, http.MethodPost, "/v1/social/posts", `{"body":"Hello #local","media_asset_ids":[]}`, "customer-synthetic-001", "CUSTOMER", false, "post-http-key-0002", "")
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}

	missingRevision := socialRequest(t, handler, http.MethodPut, "/v1/social/posts/social-post-public-001/like", `{"active":true}`, "customer-synthetic-001", "CUSTOMER", false, "like-http-key-0001", "")
	if missingRevision.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing revision status=%d body=%s", missingRevision.Code, missingRevision.Body.String())
	}

	moderation := socialRequest(t, handler, http.MethodGet, "/v1/moderation/reports", "", "moderator-001", "MODERATOR", false, "", "")
	if moderation.Code != http.StatusForbidden || !strings.Contains(moderation.Body.String(), "SOCIAL_MFA_REQUIRED") {
		t.Fatalf("moderation status=%d body=%s", moderation.Code, moderation.Body.String())
	}
}

func socialRequest(t *testing.T, handler http.Handler, method, path, body, subject, role string, mfa bool, key, revision string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-ID", "corr-social-http-001")
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", subject)
	request.Header.Set("X-Planext4u-Roles", role)
	if mfa {
		request.Header.Set("X-Planext4u-MFA", "verified")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if revision != "" {
		request.Header.Set("If-Match", revision)
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}
