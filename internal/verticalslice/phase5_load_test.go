package verticalslice

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBEP5012EngagementAndLocalVerticalReadLoadMeetsLatencyBudget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 10, 30, 0, 0, time.UTC)
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
	login := phase3LoadRequest(server.Client(), http.MethodPost, server.URL+"/v1/auth/exchange", `{"provider":"local","provider_token":"synthetic-customer","device_id":"device-phase5-load-001","country":"IN"}`, "", nil, "corr-phase5-load-auth")
	if login.err != nil || login.status != http.StatusCreated {
		t.Fatalf("login status=%d err=%v body=%s", login.status, login.err, login.body)
	}
	var authentication struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(login.body, &authentication); err != nil || authentication.Tokens.AccessToken == "" {
		t.Fatalf("decode authentication: %v", err)
	}

	paths := []string{
		"/v1/social/feed?limit=20",
		"/v1/social/ephemeral",
		"/v1/homes/listings?locality=Chennai",
		"/v1/classifieds/listings?locality=Chennai",
		"/v1/emergency/requests",
	}
	const workers = 64
	start := make(chan struct{})
	results := make(chan phase3Response, workers*len(paths))
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			for pathIndex, path := range paths {
				results <- phase3LoadRequest(
					server.Client(),
					http.MethodGet,
					server.URL+path,
					"",
					authentication.Tokens.AccessToken,
					nil,
					fmt.Sprintf("corr-phase5-load-%03d-%02d", worker, pathIndex),
				)
			}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	latencies := make([]time.Duration, 0, workers*len(paths))
	for result := range results {
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("phase5 read status=%d err=%v body=%s", result.status, result.err, result.body)
		}
		latencies = append(latencies, result.duration)
	}
	if p95 := percentile95(latencies); p95 > 400*time.Millisecond {
		t.Fatalf("phase5 engagement/local-vertical p95=%s, want <=400ms", p95)
	}
}
