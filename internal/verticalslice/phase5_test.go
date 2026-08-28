package verticalslice

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBEP5012GatewaySocioPrivacyEngagementAndModeration(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	application, err := New(Config{
		SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"),
		Clock:      func() time.Time { return now },
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application)
	t.Cleanup(server.Close)
	client := server.Client()
	customer := verticalLoginProvider(t, client, server.URL, "synthetic-customer", "device-social-e2e-001")
	moderator := verticalLoginProvider(t, client, server.URL, "synthetic-moderator", "device-moderator-e2e-001")

	feed := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/social/feed?limit=20", "", customer)
	assertVerticalResponse(t, feed, http.StatusOK, `"ranking_version":"socio-feed-v1"`, `"id":"social-post-synthetic-001"`)

	pending := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/posts", `{"body":"manual-review content awaiting policy","media_asset_ids":[]}`, customer, map[string]string{"Idempotency-Key": "e2e-social-pending-0001"})
	assertVerticalResponse(t, pending, http.StatusCreated, `"status":"PENDING_REVIEW"`, `"moderation_reason":"AUTOMATED_POLICY_REVIEW"`)

	created := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/posts", `{"body":"New local update #Chennai for @local_guide","media_asset_ids":["asset-private-social-e2e"]}`, customer, map[string]string{"Idempotency-Key": "e2e-social-post-000001"})
	assertVerticalResponse(t, created, http.StatusCreated, `"status":"PUBLISHED"`, `"chennai"`, `"local_guide"`)
	var post struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, created, &post)

	liked := verticalRequestWithHeaders(t, client, http.MethodPut, server.URL+"/v1/social/posts/"+post.ID+"/like", `{"active":true}`, customer, mutationHeaders("e2e-social-like-0000001", post.Revision))
	assertVerticalResponse(t, liked, http.StatusOK, `"liked":true`, `"like_count":1`)
	decodeVerticalJSON(t, liked, &post)
	comment := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/posts/"+post.ID+"/comments", `{"body":"First accessible reply"}`, customer, map[string]string{"Idempotency-Key": "e2e-social-comment-001"})
	assertVerticalResponse(t, comment, http.StatusCreated, `"depth":0`, `"status":"PUBLISHED"`)

	report := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/posts/social-post-synthetic-001/reports", `{"reason":"SPAM","details":"Repeated misleading promotion in feed"}`, customer, map[string]string{"Idempotency-Key": "e2e-social-report-0001"})
	assertVerticalResponse(t, report, http.StatusCreated, `"status":"OPEN"`)
	var reportPayload struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, report, &reportPayload)
	queue := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/moderation/reports", "", moderator)
	assertVerticalResponse(t, queue, http.StatusOK, `"id":"`+reportPayload.ID+`"`, `"reason":"SPAM"`)
	decision := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/moderation/reports/"+reportPayload.ID+"/decision", `{"decision":"REMOVE","note":"Confirmed repetitive commercial spam"}`, moderator, map[string]string{"Idempotency-Key": "e2e-social-moderate-001"})
	assertVerticalResponse(t, decision, http.StatusOK, `"status":"DECIDED"`, `"decision":"REMOVE"`)

	feed = verticalRequest(t, client, http.MethodGet, server.URL+"/v1/social/feed?limit=20", "", customer)
	body, _ := io.ReadAll(feed.Body)
	_ = feed.Body.Close()
	if feed.StatusCode != http.StatusOK || !contains(string(body), `"id":"`+post.ID+`"`) || contains(string(body), `"id":"social-post-synthetic-001"`) || contains(string(body), `"status":"PENDING_REVIEW"`) {
		t.Fatalf("moderated or pending post leaked in feed: %s", body)
	}
}

func contains(value, token string) bool {
	for index := 0; index+len(token) <= len(value); index++ {
		if value[index:index+len(token)] == token {
			return true
		}
	}
	return false
}
