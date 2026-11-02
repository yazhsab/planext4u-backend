package adminshell

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
)

func TestFulfillmentReaderCannotTurnMissingOwnerCountersIntoZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"region_id":"NORTH"}`, `{"region_id":"NORTH","territories":null}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer owner.Close()
			origin, _ := url.Parse(owner.URL)
			reader, _ := NewHTTPFulfillmentReader(origin, &http.Client{Timeout: time.Second})
			if _, err := reader.Dashboard(context.Background(), fulfillment.Actor{}, "NORTH"); err == nil {
				t.Fatal("incomplete owner response became fabricated counters")
			}
		})
	}
}
