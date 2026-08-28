package verticalslice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestBEP3010ConcurrentCheckoutMeetsControlledSuccessAndLatencyBudget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 30, 0, 0, time.UTC)
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

	authentication := phase3LoadRequest(client, http.MethodPost, server.URL+"/v1/auth/exchange", `{"provider":"local","provider_token":"synthetic-customer","device_id":"device-phase3-load-001","country":"IN"}`, "", nil, "corr-phase3-load-auth")
	if authentication.err != nil || authentication.status != http.StatusCreated {
		t.Fatalf("authenticate status=%d err=%v body=%s", authentication.status, authentication.err, authentication.body)
	}
	var authPayload struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(authentication.body, &authPayload); err != nil || authPayload.Tokens.AccessToken == "" {
		t.Fatalf("decode authentication: %v", err)
	}
	added := phase3LoadRequest(client, http.MethodPut, server.URL+"/v1/cart/items/variant-milk-1l", `{"quantity":1}`, authPayload.Tokens.AccessToken, map[string]string{"Idempotency-Key": "idem-phase3-load-cart-001", "If-Match": `"0"`}, "corr-phase3-load-cart")
	if added.err != nil || added.status != http.StatusOK {
		t.Fatalf("seed cart status=%d err=%v body=%s", added.status, added.err, added.body)
	}

	const concurrentOrders = 25
	start := make(chan struct{})
	results := make(chan phase3JourneyResult, concurrentOrders)
	var workers sync.WaitGroup
	for index := 0; index < concurrentOrders; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			quote := phase3LoadRequest(client, http.MethodPost, server.URL+"/v1/checkout/quotes", `{"cart_revision":1,"address_id":"address-home-001","delivery_slot_id":"slot-standard-001"}`, authPayload.Tokens.AccessToken, map[string]string{"Idempotency-Key": fmt.Sprintf("idem-phase3-load-quote-%03d", worker)}, fmt.Sprintf("corr-phase3-load-quote-%03d", worker))
			if quote.err != nil || quote.status != http.StatusCreated {
				results <- phase3JourneyResult{quote: quote, err: fmt.Errorf("quote status %d: %w", quote.status, quote.err)}
				return
			}
			var payload struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(quote.body, &payload); err != nil || payload.ID == "" {
				results <- phase3JourneyResult{quote: quote, err: fmt.Errorf("decode quote: %w", err)}
				return
			}
			placeBody, _ := json.Marshal(map[string]string{"quote_id": payload.ID, "payment_method": "COD"})
			place := phase3LoadRequest(client, http.MethodPost, server.URL+"/v1/checkout/orders", string(placeBody), authPayload.Tokens.AccessToken, map[string]string{"Idempotency-Key": fmt.Sprintf("idem-phase3-load-place-%03d", worker)}, fmt.Sprintf("corr-phase3-load-place-%03d", worker))
			if place.err != nil || place.status != http.StatusCreated || !bytes.Contains(place.body, []byte(`"status":"PLACED"`)) {
				results <- phase3JourneyResult{quote: quote, place: place, err: fmt.Errorf("place status %d: %w", place.status, place.err)}
				return
			}
			results <- phase3JourneyResult{quote: quote, place: place}
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)

	var quoteLatencies, placeLatencies []time.Duration
	successes := 0
	for result := range results {
		quoteLatencies = append(quoteLatencies, result.quote.duration)
		if result.place.duration > 0 {
			placeLatencies = append(placeLatencies, result.place.duration)
		}
		if result.err == nil {
			successes++
		} else {
			t.Log(result.err)
		}
	}
	controlledSuccess := float64(successes) / concurrentOrders
	if controlledSuccess < 0.97 {
		t.Fatalf("controlled checkout success=%.2f, want >=0.97", controlledSuccess)
	}
	for name, latency := range map[string]time.Duration{"quote": percentile95(quoteLatencies), "place": percentile95(placeLatencies)} {
		if latency > 400*time.Millisecond {
			t.Fatalf("%s p95=%s, want <=400ms", name, latency)
		}
	}

	orders := phase3LoadRequest(client, http.MethodGet, server.URL+"/v1/orders", "", authPayload.Tokens.AccessToken, nil, "corr-phase3-load-orders")
	var orderPage struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	decodeErr := json.Unmarshal(orders.body, &orderPage)
	if orders.err != nil || orders.status != http.StatusOK || decodeErr != nil || len(orderPage.Items) != successes {
		t.Fatalf("order reconciliation status=%d request_err=%v decode_err=%v count=%d want=%d", orders.status, orders.err, decodeErr, len(orderPage.Items), successes)
	}
	for _, item := range orderPage.Items {
		if item.Status != "PLACED" {
			t.Fatalf("reconciled order status=%s, want PLACED", item.Status)
		}
	}
}

func TestBEP3010DependencyReadinessFailsClosedWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	application, err := New(Config{
		SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"),
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Readiness:  func() error { return errors.New("provider password=must-not-leak") },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !stringsContain(response.Body.Bytes(), `"status":"not_ready"`) || bytes.Contains(response.Body.Bytes(), []byte("password")) {
		t.Fatalf("readiness=%d body=%s", response.Code, response.Body.String())
	}
}

type phase3Response struct {
	status   int
	body     []byte
	duration time.Duration
	err      error
}

type phase3JourneyResult struct {
	quote phase3Response
	place phase3Response
	err   error
}

func phase3LoadRequest(client *http.Client, method, target, body, accessToken string, headers map[string]string, correlationID string) phase3Response {
	started := time.Now()
	request, err := http.NewRequest(method, target, bytes.NewBufferString(body))
	if err != nil {
		return phase3Response{duration: time.Since(started), err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-ID", correlationID)
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return phase3Response{duration: time.Since(started), err: err}
	}
	defer response.Body.Close()
	contents, readErr := io.ReadAll(response.Body)
	return phase3Response{status: response.StatusCode, body: contents, duration: time.Since(started), err: readErr}
}

func percentile95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	index := int(math.Ceil(float64(len(values))*0.95)) - 1
	return values[index]
}

func stringsContain(value []byte, expected string) bool {
	return bytes.Contains(value, []byte(expected))
}
