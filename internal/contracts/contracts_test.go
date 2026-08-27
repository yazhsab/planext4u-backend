package contracts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yazhsab/planext4u-backend/internal/contracts/generated"
)

type schema struct {
	Required []string `json:"required"`
	Enum     []string `json:"enum"`
}

type openAPIDocument struct {
	OpenAPI    string                    `json:"openapi"`
	Paths      map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]schema `json:"schemas"`
	} `json:"components"`
}

type asyncAPIDocument struct {
	AsyncAPI   string `json:"asyncapi"`
	Components struct {
		Messages map[string]any    `json:"messages"`
		Schemas  map[string]schema `json:"schemas"`
	} `json:"components"`
}

type compatibilityBaseline struct {
	OpenAPI struct {
		Operations []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"operations"`
		RequiredProperties map[string][]string `json:"required_properties"`
		EnumValues         map[string][]string `json:"enum_values"`
	} `json:"openapi"`
	AsyncAPI struct {
		Messages           []string            `json:"messages"`
		RequiredProperties map[string][]string `json:"required_properties"`
	} `json:"asyncapi"`
}

func TestBEContract001CompatibilityBaseline(t *testing.T) {
	t.Parallel()

	var openAPI openAPIDocument
	readJSON(t, "api/openapi/common.openapi.json", &openAPI)
	if openAPI.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", openAPI.OpenAPI)
	}

	var asyncAPI asyncAPIDocument
	readJSON(t, "api/asyncapi/common.asyncapi.json", &asyncAPI)
	if asyncAPI.AsyncAPI != "3.0.0" {
		t.Fatalf("AsyncAPI version = %q, want 3.0.0", asyncAPI.AsyncAPI)
	}

	var baseline compatibilityBaseline
	readJSON(t, "api/compatibility/common-v1-baseline.json", &baseline)

	for _, operation := range baseline.OpenAPI.Operations {
		methods, exists := openAPI.Paths[operation.Path]
		if !exists {
			t.Errorf("breaking change: OpenAPI path %s was removed", operation.Path)
			continue
		}
		if _, exists := methods[strings.ToLower(operation.Method)]; !exists {
			t.Errorf("breaking change: OpenAPI operation %s %s was removed", operation.Method, operation.Path)
		}
	}
	assertRequiredProperties(t, "OpenAPI", openAPI.Components.Schemas, baseline.OpenAPI.RequiredProperties)

	for schemaName, requiredValues := range baseline.OpenAPI.EnumValues {
		current, exists := openAPI.Components.Schemas[schemaName]
		if !exists {
			t.Errorf("breaking change: OpenAPI enum schema %s was removed", schemaName)
			continue
		}
		for _, requiredValue := range requiredValues {
			if !contains(current.Enum, requiredValue) {
				t.Errorf("breaking change: OpenAPI enum %s removed value %s", schemaName, requiredValue)
			}
		}
	}

	for _, message := range baseline.AsyncAPI.Messages {
		if _, exists := asyncAPI.Components.Messages[message]; !exists {
			t.Errorf("breaking change: AsyncAPI message %s was removed", message)
		}
	}
	assertRequiredProperties(t, "AsyncAPI", asyncAPI.Components.Schemas, baseline.AsyncAPI.RequiredProperties)
}

func TestGeneratedFixturesAreSyntheticAndStructurallyValid(t *testing.T) {
	t.Parallel()

	var problem struct {
		Error struct {
			Code          string `json:"code"`
			CorrelationID string `json:"correlation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(generated.ProblemFixtureJSON), &problem); err != nil {
		t.Fatalf("problem fixture is invalid JSON: %v", err)
	}
	if problem.Error.Code == "" || !strings.HasPrefix(problem.Error.CorrelationID, "corr-synthetic-") {
		t.Fatalf("problem fixture is incomplete or not synthetic: %#v", problem)
	}

	var event struct {
		EventID       string         `json:"event_id"`
		EventType     string         `json:"event_type"`
		CorrelationID string         `json:"correlation_id"`
		Data          map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(generated.DomainEventFixtureJSON), &event); err != nil {
		t.Fatalf("domain event fixture is invalid JSON: %v", err)
	}
	if event.EventID == "" || !strings.HasSuffix(event.EventType, ".v1") || event.Data == nil {
		t.Fatalf("domain event fixture is incomplete: %#v", event)
	}
	if !strings.HasPrefix(event.CorrelationID, "corr-synthetic-") {
		t.Fatalf("domain event fixture correlation is not synthetic: %q", event.CorrelationID)
	}
}

func assertRequiredProperties(
	t *testing.T,
	documentName string,
	current map[string]schema,
	baseline map[string][]string,
) {
	t.Helper()
	for schemaName, requiredProperties := range baseline {
		currentSchema, exists := current[schemaName]
		if !exists {
			t.Errorf("breaking change: %s schema %s was removed", documentName, schemaName)
			continue
		}
		for _, property := range requiredProperties {
			if !contains(currentSchema.Required, property) {
				t.Errorf("breaking change: %s schema %s no longer requires %s", documentName, schemaName, property)
			}
		}
	}
}

func readJSON(t *testing.T, relativePath string, target any) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		t.Fatalf("parse %s: %v", relativePath, err)
	}
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
