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

type openAPICompatibilityBaseline struct {
	Operations []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"operations"`
	RequiredProperties map[string][]string `json:"required_properties"`
	EnumValues         map[string][]string `json:"enum_values"`
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

	var authentication struct {
		IdentityID string   `json:"identity_id"`
		TenantID   string   `json:"tenant_id"`
		Roles      []string `json:"roles"`
		Tokens     struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(generated.IdentityAuthenticationFixtureJSON), &authentication); err != nil {
		t.Fatalf("identity authentication fixture is invalid JSON: %v", err)
	}
	if !strings.Contains(authentication.IdentityID, "synthetic") ||
		!strings.Contains(authentication.TenantID, "synthetic") ||
		len(authentication.Roles) != 1 || authentication.Roles[0] != "CUSTOMER" ||
		!strings.Contains(authentication.Tokens.AccessToken, "synthetic") ||
		!strings.Contains(authentication.Tokens.RefreshToken, "synthetic") {
		t.Fatalf("identity authentication fixture is incomplete or not synthetic: %#v", authentication)
	}
}

func TestIdentityContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	var document openAPIDocument
	readJSON(t, "api/openapi/identity.openapi.json", &document)
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("identity OpenAPI version = %q, want 3.1.0", document.OpenAPI)
	}
	var baseline openAPICompatibilityBaseline
	readJSON(t, "api/compatibility/identity-v1-baseline.json", &baseline)
	for _, operation := range baseline.Operations {
		methods, exists := document.Paths[operation.Path]
		if !exists {
			t.Errorf("breaking change: identity path %s was removed", operation.Path)
			continue
		}
		if _, exists := methods[strings.ToLower(operation.Method)]; !exists {
			t.Errorf("breaking change: identity operation %s %s was removed", operation.Method, operation.Path)
		}
	}
	assertRequiredProperties(t, "identity OpenAPI", document.Components.Schemas, baseline.RequiredProperties)
	for schemaName, requiredValues := range baseline.EnumValues {
		current, exists := document.Components.Schemas[schemaName]
		if !exists {
			t.Errorf("breaking change: identity enum schema %s was removed", schemaName)
			continue
		}
		for _, requiredValue := range requiredValues {
			if !contains(current.Enum, requiredValue) {
				t.Errorf("breaking change: identity enum %s removed value %s", schemaName, requiredValue)
			}
		}
	}
}

func TestIdentityContractSecurityMetadata(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/identity.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/v1/auth/exchange", "/v1/auth/refresh", "/v1/auth/revoke"} {
		operation := paths[path].(map[string]any)["post"].(map[string]any)
		security, exists := operation["security"].([]any)
		if !exists || len(security) != 0 {
			t.Errorf("anonymous operation %s must explicitly declare empty security", path)
		}
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	exchangeProperties := schemas["ExchangeRequest"].(map[string]any)["properties"].(map[string]any)
	if _, exists := exchangeProperties["role"]; exists {
		t.Fatal("provider exchange contract allows a client-requested role")
	}
	tokenProperties := schemas["TokenPair"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"access_token", "refresh_token"} {
		property := tokenProperties[field].(map[string]any)
		if property["x-planext4u-sensitive"] != true {
			t.Errorf("TokenPair.%s is not marked sensitive", field)
		}
	}
	reviewers := document["x-planext4u-reviewers"].([]any)
	if !containsAny(reviewers, "security") || !containsAny(reviewers, "privacy") {
		t.Fatalf("identity reviewers = %v", reviewers)
	}
}

func TestConfigurationContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/configuration.openapi.json", "api/compatibility/configuration-v1-baseline.json")
}

func TestCatalogContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/catalog.openapi.json", "api/compatibility/catalog-v1-baseline.json")
}

func TestCommerceContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/commerce.openapi.json", "api/compatibility/commerce-v1-baseline.json")
}

func TestTransactionContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/transaction.openapi.json", "api/compatibility/transaction-v1-baseline.json")
}

func TestBookingContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/booking.openapi.json", "api/compatibility/booking-v1-baseline.json")
}

func TestBookingContractProtectsOTPAndFixtureIsSynthetic(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/booking.openapi.json", &document)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	bookingProperties := schemas["ServiceBooking"].(map[string]any)["properties"].(map[string]any)
	if bookingProperties["start_otp"].(map[string]any)["x-planext4u-sensitive"] != true {
		t.Fatal("service booking start OTP is not marked sensitive")
	}
	startProperties := schemas["StartRequest"].(map[string]any)["properties"].(map[string]any)
	if startProperties["otp"].(map[string]any)["x-planext4u-sensitive"] != true {
		t.Fatal("service start request OTP is not marked sensitive")
	}
	var fixture struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Offering struct {
			ProviderID string `json:"provider_id"`
		} `json:"offering"`
		Payment struct {
			Status string `json:"status"`
		} `json:"payment"`
	}
	if err := json.Unmarshal([]byte(generated.ServiceBookingFixtureJSON), &fixture); err != nil {
		t.Fatalf("service booking fixture is invalid JSON: %v", err)
	}
	if !strings.Contains(fixture.ID, "synthetic") || !strings.Contains(fixture.Offering.ProviderID, "synthetic") || fixture.Status != "REQUESTED" || fixture.Payment.Status != "CAPTURED" {
		t.Fatalf("service booking fixture is incomplete or not synthetic: %#v", fixture)
	}
}

func TestSupplyContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/supply.openapi.json", "api/compatibility/supply-v1-baseline.json")
}

func TestFoodContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/food.openapi.json", "api/compatibility/food-v1-baseline.json")
}

func TestFulfillmentContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/fulfillment.openapi.json", "api/compatibility/fulfillment-v1-baseline.json")
}

func TestSocialContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/social.openapi.json", "api/compatibility/social-v1-baseline.json")
}

func TestSocialContractProtectsPrivateMediaAndFixtureIsSynthetic(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/social.openapi.json", &document)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	postProperties := schemas["SocialPost"].(map[string]any)["properties"].(map[string]any)
	mediaItems := postProperties["media_asset_ids"].(map[string]any)["items"].(map[string]any)
	if mediaItems["x-planext4u-sensitive"] != true {
		t.Fatal("social media asset references are not marked sensitive")
	}
	var feed struct {
		RankingVersion string `json:"ranking_version"`
		Items          []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(generated.SocialFeedFixtureJSON), &feed); err != nil || feed.RankingVersion != "socio-feed-v1" || len(feed.Items) != 1 || !strings.Contains(feed.Items[0].ID, "synthetic") || feed.Items[0].Status != "PUBLISHED" {
		t.Fatalf("social fixture=%#v err=%v", feed, err)
	}
}

func TestPhase4ContractsProtectSensitiveEvidenceAndFixturesAreSynthetic(t *testing.T) {
	t.Parallel()
	var fulfillment map[string]any
	readJSON(t, "api/openapi/fulfillment.openapi.json", &fulfillment)
	schemas := fulfillment["components"].(map[string]any)["schemas"].(map[string]any)
	completion := schemas["DeliveryCompletionRequest"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"otp", "blurred_photo_asset_id", "signature_asset_id"} {
		if completion[field].(map[string]any)["x-planext4u-sensitive"] != true {
			t.Errorf("DeliveryCompletionRequest.%s is not sensitive", field)
		}
	}
	var vendor struct {
		Application struct {
			ID       string `json:"id"`
			Verified bool   `json:"verified"`
		} `json:"application"`
	}
	if err := json.Unmarshal([]byte(generated.VendorProgramFixtureJSON), &vendor); err != nil || !strings.Contains(vendor.Application.ID, "synthetic") || !vendor.Application.Verified {
		t.Fatalf("vendor fixture=%#v err=%v", vendor, err)
	}
	var food struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Total  struct {
			AmountMinor int64 `json:"amount_minor"`
		} `json:"total"`
	}
	if err := json.Unmarshal([]byte(generated.FoodOrderFixtureJSON), &food); err != nil || !strings.Contains(food.ID, "synthetic") || food.Status != "PENDING_RESTAURANT" || food.Total.AmountMinor != 43450 {
		t.Fatalf("food fixture=%#v err=%v", food, err)
	}
	var rider struct {
		Profile struct {
			ID string `json:"id"`
		} `json:"profile"`
		Task struct {
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(generated.RiderAssignmentFixtureJSON), &rider); err != nil || !strings.Contains(rider.Profile.ID, "synthetic") || rider.Task.Status != "ASSIGNED" {
		t.Fatalf("rider fixture=%#v err=%v", rider, err)
	}
}

func TestMediaContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/media.openapi.json", "api/compatibility/media-v1-baseline.json")
}

func TestAuditContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/audit.openapi.json", "api/compatibility/audit-v1-baseline.json")
}

func TestAdminContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/admin.openapi.json", "api/compatibility/admin-v1-baseline.json")
}

func TestAdminContractUsesCookieSessionAndCSRF(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/admin.openapi.json", &document)
	components := document["components"].(map[string]any)
	securitySchemes := components["securitySchemes"].(map[string]any)
	session := securitySchemes["adminSession"].(map[string]any)
	if session["in"] != "cookie" || session["name"] != "__Host-p4u_admin" {
		t.Fatalf("admin session must use the secure host-only cookie: %#v", session)
	}
	parameters := components["parameters"].(map[string]any)
	csrf := parameters["CSRFToken"].(map[string]any)
	if csrf["in"] != "header" || csrf["required"] != true {
		t.Fatalf("mutations must require a CSRF header: %#v", csrf)
	}
}

func TestNotificationContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/notification.openapi.json", "api/compatibility/notification-v1-baseline.json")
}

func TestNotificationFixtureUsesOpaqueSyntheticRecipient(t *testing.T) {
	t.Parallel()
	var delivery struct {
		ID           string `json:"id"`
		SubjectID    string `json:"subject_id"`
		RecipientRef string `json:"recipient_ref"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal([]byte(generated.NotificationDeliveryFixtureJSON), &delivery); err != nil {
		t.Fatalf("notification fixture is invalid JSON: %v", err)
	}
	if delivery.ID == "" || !strings.Contains(delivery.SubjectID, "synthetic") || !strings.Contains(delivery.RecipientRef, "synthetic") || delivery.Status != "QUEUED" {
		t.Fatalf("notification fixture is incomplete or not synthetic: %#v", delivery)
	}
}

func TestAuditFixtureIsSyntheticAndHasIntegrityEvidence(t *testing.T) {
	t.Parallel()
	var entry struct {
		ID       string `json:"id"`
		Hash     string `json:"hash"`
		Sequence int64  `json:"sequence"`
	}
	if err := json.Unmarshal([]byte(generated.AuditEntryFixtureJSON), &entry); err != nil {
		t.Fatalf("audit fixture is invalid JSON: %v", err)
	}
	if !strings.Contains(entry.ID, "synthetic") || len(entry.Hash) != 64 || entry.Sequence != 1 {
		t.Fatalf("audit fixture is incomplete: %#v", entry)
	}
}

func TestMediaFixtureIsSyntheticAndTerminal(t *testing.T) {
	t.Parallel()
	var asset struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(generated.MediaAssetFixtureJSON), &asset); err != nil {
		t.Fatalf("media fixture is invalid JSON: %v", err)
	}
	if !strings.Contains(asset.ID, "synthetic") || asset.State != "READY" {
		t.Fatalf("media fixture is incomplete: %#v", asset)
	}
}

func TestCommerceFixtureUsesIntegerServerTotals(t *testing.T) {
	t.Parallel()
	var cart struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Total    struct {
			AmountMinor int64  `json:"amount_minor"`
			Currency    string `json:"currency"`
		} `json:"total"`
	}
	if err := json.Unmarshal([]byte(generated.CommerceCartFixtureJSON), &cart); err != nil {
		t.Fatalf("commerce fixture is invalid JSON: %v", err)
	}
	if !strings.Contains(cart.ID, "synthetic") || cart.Revision != 1 || cart.Total.AmountMinor != 13000 || cart.Total.Currency != "INR" {
		t.Fatalf("commerce fixture is incomplete: %#v", cart)
	}
}

func assertOpenAPICompatibility(t *testing.T, contractPath, baselinePath string) {
	t.Helper()
	var document openAPIDocument
	readJSON(t, contractPath, &document)
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("%s OpenAPI version = %q", contractPath, document.OpenAPI)
	}
	var baseline openAPICompatibilityBaseline
	readJSON(t, baselinePath, &baseline)
	for _, operation := range baseline.Operations {
		methods, exists := document.Paths[operation.Path]
		if !exists {
			t.Errorf("breaking change: %s path %s was removed", contractPath, operation.Path)
			continue
		}
		if _, exists := methods[strings.ToLower(operation.Method)]; !exists {
			t.Errorf("breaking change: %s operation %s %s was removed", contractPath, operation.Method, operation.Path)
		}
	}
	assertRequiredProperties(t, contractPath, document.Components.Schemas, baseline.RequiredProperties)
	for schemaName, values := range baseline.EnumValues {
		current, exists := document.Components.Schemas[schemaName]
		if !exists {
			t.Errorf("breaking change: enum %s removed", schemaName)
			continue
		}
		for _, value := range values {
			if !contains(current.Enum, value) {
				t.Errorf("breaking change: enum %s removed value %s", schemaName, value)
			}
		}
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

func containsAny(values []any, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
