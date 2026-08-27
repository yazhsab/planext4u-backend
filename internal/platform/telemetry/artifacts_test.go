package telemetry

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBEOBS001OperationalArtifactsLinkActionableRunbooks(t *testing.T) {
	t.Parallel()
	alerts := readArtifact(t, "../../../deploy/observability/alerts/phase2-slos.yaml")
	for _, name := range []string{"Planext4uAvailabilityFastBurn", "Planext4uAvailabilitySlowBurn", "Planext4uLatencyBudgetBurn", "Planext4uQueueLagHigh", "Planext4uNotificationProviderFailure"} {
		if !strings.Contains(alerts, "alert: "+name) {
			t.Errorf("missing alert %s", name)
		}
	}
	if strings.Count(alerts, "runbook_url:") != 5 || !strings.Contains(alerts, "[5m]") || !strings.Contains(alerts, "[1h]") || !strings.Contains(alerts, "[6h]") {
		t.Fatal("alerts must use paired burn windows and link every rule to a runbook")
	}
	collector := readArtifact(t, "../../../deploy/observability/otel-collector.yaml")
	for _, marker := range []string{"attributes/privacy:", "key: url.query", "key: http.request.header.authorization", "memory_limiter:", "batch:"} {
		if !strings.Contains(collector, marker) {
			t.Errorf("collector is missing %q", marker)
		}
	}
	var dashboard struct {
		Panels []struct {
			Title string `json:"title"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(readArtifact(t, "../../../deploy/observability/dashboards/service-red.json")), &dashboard); err != nil {
		t.Fatalf("dashboard JSON: %v", err)
	}
	if len(dashboard.Panels) < 6 {
		t.Fatalf("dashboard panels = %d, want at least 6", len(dashboard.Panels))
	}
}

func readArtifact(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
