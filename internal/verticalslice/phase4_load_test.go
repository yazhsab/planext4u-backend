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

func TestBEP4012Phase4DiscoveryLoadMeetsControlledLatencyBudget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 30, 0, 0, time.UTC)
	application, err := New(Config{SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"), Clock: func() time.Time { return now }, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application)
	t.Cleanup(server.Close)
	login := phase3LoadRequest(server.Client(), http.MethodPost, server.URL+"/v1/auth/exchange", `{"provider":"local","provider_token":"synthetic-customer","device_id":"device-phase4-load-001","country":"IN"}`, "", nil, "corr-phase4-load-auth")
	if login.status != http.StatusCreated || login.err != nil {
		t.Fatalf("login status=%d err=%v body=%s", login.status, login.err, login.body)
	}
	var authentication struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(login.body, &authentication); err != nil || authentication.Tokens.AccessToken == "" {
		t.Fatalf("decode login: %v", err)
	}

	const workers = 64
	start := make(chan struct{})
	results := make(chan phase3Response, workers*2)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results <- phase3LoadRequest(server.Client(), http.MethodGet, server.URL+"/v1/restaurants?postal_code=600001", "", authentication.Tokens.AccessToken, nil, fmt.Sprintf("corr-phase4-load-restaurants-%03d", index))
			results <- phase3LoadRequest(server.Client(), http.MethodGet, server.URL+"/v1/restaurants/restaurant-saravana/menu?postal_code=600001", "", authentication.Tokens.AccessToken, nil, fmt.Sprintf("corr-phase4-load-menu-%03d", index))
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	latencies := make([]time.Duration, 0, workers*2)
	for result := range results {
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("phase4 read status=%d err=%v body=%s", result.status, result.err, result.body)
		}
		latencies = append(latencies, result.duration)
	}
	if p95 := percentile95(latencies); p95 > 400*time.Millisecond {
		t.Fatalf("phase4 discovery p95=%s, want <=400ms", p95)
	}
}
