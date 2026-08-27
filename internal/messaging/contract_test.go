package messaging

import (
	"encoding/json"
	"testing"
)

func TestMessageEnvelopeMatchesCommonAsyncAPI(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(syntheticMessage("00000000-0000-4000-8000-000000000001", 1))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"event_id", "event_type", "occurred_at", "producer", "aggregate_type", "aggregate_id",
		"aggregate_version", "tenant_id", "country", "correlation_id", "causation_id", "traceparent", "classification", "schema_version", "data"} {
		if _, exists := envelope[required]; !exists {
			t.Errorf("common AsyncAPI field %s is missing", required)
		}
	}
	for _, prohibited := range []string{"id", "sequence", "payload"} {
		if _, exists := envelope[prohibited]; exists {
			t.Errorf("non-contract field %s is present", prohibited)
		}
	}
}
