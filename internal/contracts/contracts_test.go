package contracts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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

func TestSuccessfulOpenAPIResponsesHaveConcreteJSONSchemas(t *testing.T) {
	t.Parallel()
	contractPaths, err := filepath.Glob(filepath.Join("..", "..", "api", "openapi", "*.openapi.json"))
	if err != nil {
		t.Fatalf("list OpenAPI contracts: %v", err)
	}
	if len(contractPaths) == 0 {
		t.Fatal("no OpenAPI contracts found")
	}

	for _, contractPath := range contractPaths {
		contractPath := contractPath
		t.Run(filepath.Base(contractPath), func(t *testing.T) {
			t.Parallel()
			contents, readErr := os.ReadFile(contractPath)
			if readErr != nil {
				t.Fatalf("read contract: %v", readErr)
			}
			var document map[string]any
			if unmarshalErr := json.Unmarshal(contents, &document); unmarshalErr != nil {
				t.Fatalf("parse contract: %v", unmarshalErr)
			}
			components := document["components"].(map[string]any)["schemas"].(map[string]any)
			assertNoEmptyObjectPlaceholders(t, components, "components.schemas")
			paths := document["paths"].(map[string]any)
			for path, pathValue := range paths {
				pathItem := pathValue.(map[string]any)
				for method, operationValue := range pathItem {
					if !isHTTPMethod(method) {
						continue
					}
					operation := operationValue.(map[string]any)
					responses, _ := operation["responses"].(map[string]any)
					for status, responseValue := range responses {
						if len(status) != 3 || status[0] != '2' || status == "204" {
							continue
						}
						response := responseValue.(map[string]any)
						content, ok := response["content"].(map[string]any)
						if !ok {
							t.Errorf("%s %s response %s has no content", strings.ToUpper(method), path, status)
							continue
						}
						media, ok := content["application/json"].(map[string]any)
						if !ok {
							for _, value := range content {
								candidate, candidateOK := value.(map[string]any)
								if candidateOK && candidate["schema"] != nil {
									media, ok = candidate, true
									break
								}
							}
							if !ok {
								t.Errorf("%s %s response %s has no typed content contract", strings.ToUpper(method), path, status)
								continue
							}
						}
						responseSchema, ok := media["schema"].(map[string]any)
						if !ok || len(responseSchema) == 0 {
							t.Errorf("%s %s response %s has no concrete schema", strings.ToUpper(method), path, status)
							continue
						}
						assertSchemaReferencesExist(t, responseSchema, components, strings.ToUpper(method)+" "+path+" "+status)
					}
				}
			}
		})
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
	guestOperation := paths["/internal/v1/customer-guest-sessions"].(map[string]any)["post"].(map[string]any)
	guestSecurity := guestOperation["security"].([]any)
	if len(guestSecurity) != 1 {
		t.Fatalf("guest exchange security = %v", guestSecurity)
	}
	guestSchemes := guestSecurity[0].(map[string]any)
	if _, exists := guestSchemes["guestExchangeSignature"]; !exists {
		t.Fatal("guest exchange is missing signature authentication")
	}
	if _, exists := guestSchemes["guestExchangeTimestamp"]; !exists {
		t.Fatal("guest exchange is missing timestamp authentication")
	}
	if guestOperation["x-planext4u-session-class"] != "ephemeral-access-only" ||
		guestOperation["x-planext4u-guest-capability"] != "GET storefront allowlist only" {
		t.Fatalf("guest capability metadata = %v", guestOperation)
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
	guestProperties := schemas["GuestSession"].(map[string]any)["properties"].(map[string]any)
	if _, exists := guestProperties["refresh_token"]; exists {
		t.Fatal("guest session contract exposes a refresh token")
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

func TestConfigurationContractDefinesWebBootstrapRefreshSemantics(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/configuration.openapi.json", &document)
	operation := document["paths"].(map[string]any)["/v1/bootstrap"].(map[string]any)["get"].(map[string]any)
	parameters := operation["parameters"].([]any)
	var deploymentParameter map[string]any
	for _, value := range parameters {
		parameter := value.(map[string]any)
		if parameter["name"] == "deployment_id" {
			deploymentParameter = parameter
			break
		}
	}
	if deploymentParameter == nil {
		t.Fatal("WEB bootstrap deployment_id parameter is missing")
	}
	requiredPlatforms, ok := deploymentParameter["x-planext4u-required-for-platforms"].([]any)
	if !ok || !containsAny(requiredPlatforms, "WEB") {
		t.Fatalf("deployment identifier requirement metadata=%v", deploymentParameter)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	platforms := schemas["Platform"].(map[string]any)["enum"].([]any)
	actions := schemas["UpdateAction"].(map[string]any)["enum"].([]any)
	if !containsAny(platforms, "WEB") || !containsAny(actions, "RELOAD") {
		t.Fatalf("platforms=%v actions=%v", platforms, actions)
	}
	bootstrapProperties := schemas["Bootstrap"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"platform", "client_version", "update_gate", "update_action", "client_deployment_id", "latest_deployment_id"} {
		if _, exists := bootstrapProperties[field]; !exists {
			t.Errorf("Bootstrap.%s is missing", field)
		}
	}
}

func TestCatalogContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/catalog.openapi.json", "api/compatibility/catalog-v1-baseline.json")
}

func TestCustomerWebContractCompatibilityAndTokenBoundary(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/customer_web.openapi.json", "api/compatibility/customer_web-v1-baseline.json")
	var document map[string]any
	readJSON(t, "api/openapi/customer_web.openapi.json", &document)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	properties := schemas["CustomerWebSession"].(map[string]any)["properties"].(map[string]any)
	for _, forbidden := range []string{"access_token", "refresh_token", "provider_token"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("browser session projection exposes forbidden field %s", forbidden)
		}
	}
	security := document["components"].(map[string]any)["securitySchemes"].(map[string]any)["customerSession"].(map[string]any)
	if security["in"] != "cookie" || security["name"] != "__Host-p4u_customer" {
		t.Fatalf("customer session security = %#v", security)
	}
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

func TestSocialCommandsDeclareTypedBodiesOrIntentionalBodylessSemantics(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/social.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	expectedBodyless := map[string]bool{
		"purgeSocialRetention":     true,
		"acceptSocialConversation": true,
		"acceptSocialFollow":       true,
		"appealSocialMedia":        true,
		"followSocialProfile":      true,
	}
	typed, bodyless := 0, 0
	for path, rawMethods := range paths {
		methods := rawMethods.(map[string]any)
		for _, method := range []string{"post", "put", "patch"} {
			rawOperation, exists := methods[method]
			if !exists {
				continue
			}
			operation := rawOperation.(map[string]any)
			operationID, _ := operation["operationId"].(string)
			requestBody, hasBody := operation["requestBody"].(map[string]any)
			intentionalBodyless, _ := operation["x-planext4u-bodyless-command"].(bool)
			if hasBody == intentionalBodyless {
				t.Errorf("%s %s %s must declare exactly one of requestBody or x-planext4u-bodyless-command", method, path, operationID)
				continue
			}
			if intentionalBodyless {
				bodyless++
				if !expectedBodyless[operationID] {
					t.Errorf("%s %s unexpectedly bypasses a typed request body", method, path)
				}
				continue
			}
			typed++
			if requestBody["required"] != true {
				t.Errorf("%s %s request body is not required", method, path)
			}
			content, _ := requestBody["content"].(map[string]any)
			jsonContent, _ := content["application/json"].(map[string]any)
			bodySchema, _ := jsonContent["schema"].(map[string]any)
			ref, _ := bodySchema["$ref"].(string)
			const prefix = "#/components/schemas/"
			if !strings.HasPrefix(ref, prefix) {
				t.Errorf("%s %s does not reference a component request schema", method, path)
				continue
			}
			if _, exists := schemas[strings.TrimPrefix(ref, prefix)]; !exists {
				t.Errorf("%s %s references missing request schema %s", method, path, ref)
			}
		}
	}
	if typed != 20 || bodyless != len(expectedBodyless) {
		t.Fatalf("social command coverage typed=%d bodyless=%d, want 20/%d", typed, bodyless, len(expectedBodyless))
	}
}

func TestSocialListsAreBoundedAndProfilesAreDiscoverable(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/social.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	reads := map[string]string{
		"/v1/social/posts/{post_id}/comments":                 "SocialCommentPage",
		"/v1/social/ephemeral":                                "SocialEphemeralPage",
		"/v1/social/conversations":                            "SocialConversationPage",
		"/v1/social/conversations/{conversation_id}/messages": "SocialDirectMessagePage",
	}
	for path, pageSchema := range reads {
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		parameters := operation["parameters"].([]any)
		refs := map[string]bool{}
		for _, raw := range parameters {
			parameter := raw.(map[string]any)
			refs[parameter["$ref"].(string)] = true
		}
		for _, required := range []string{"#/components/parameters/ListCursor", "#/components/parameters/ListLimit"} {
			if !refs[required] {
				t.Errorf("GET %s omits %s", path, required)
			}
		}
		responses := operation["responses"].(map[string]any)
		ok := responses["200"].(map[string]any)
		content := ok["content"].(map[string]any)["application/json"].(map[string]any)
		ref := content["schema"].(map[string]any)["$ref"].(string)
		if ref != "#/components/schemas/"+pageSchema {
			t.Errorf("GET %s response=%s, want %s", path, ref, pageSchema)
		}
		items := schemas[pageSchema].(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)
		if items["maxItems"].(float64) != 50 {
			t.Errorf("%s maxItems=%v, want 50", pageSchema, items["maxItems"])
		}
	}
	if paths["/v1/social/profile-handles/{handle}"] == nil {
		t.Fatal("case-insensitive profile handle resolution is missing")
	}
	profileContent := paths["/v1/social/profiles/{profile_id}/content"].(map[string]any)["get"].(map[string]any)
	responses := profileContent["responses"].(map[string]any)
	ok := responses["200"].(map[string]any)
	content := ok["content"].(map[string]any)["application/json"].(map[string]any)
	if content["schema"].(map[string]any)["$ref"] != "#/components/schemas/SocialProfileContentPage" {
		t.Fatal("profile content does not use the bounded content page")
	}
}

func TestSupplyCommandsDeclareTypedBodies(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/supply.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	typed := 0
	for path, rawMethods := range paths {
		methods := rawMethods.(map[string]any)
		for _, method := range []string{"post", "put", "patch"} {
			rawOperation, exists := methods[method]
			if !exists {
				continue
			}
			operation := rawOperation.(map[string]any)
			operationID := operation["operationId"].(string)
			requestBody, hasBody := operation["requestBody"].(map[string]any)
			if !hasBody || operation["x-planext4u-bodyless-command"] == true {
				t.Errorf("%s %s %s must declare a typed request body", method, path, operationID)
				continue
			}
			typed++
			assertComponentRequestBody(t, method, path, requestBody, schemas)
		}
	}
	if typed != 15 {
		t.Fatalf("supply command coverage typed=%d, want 15", typed)
	}
}

func TestRiderCommandsDeclareTypedBodiesOrIntentionalBodylessSemantics(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/fulfillment.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	expectedBodyless := map[string]bool{
		"acceptRiderOffer": true,
		"endRiderDuty":     true,
		"markRiderPickup":  true,
	}
	typed, bodyless := 0, 0
	for path, rawMethods := range paths {
		if !strings.HasPrefix(path, "/v1/rider/") {
			continue
		}
		methods := rawMethods.(map[string]any)
		rawOperation, exists := methods["post"]
		if !exists {
			continue
		}
		operation := rawOperation.(map[string]any)
		operationID := operation["operationId"].(string)
		requestBody, hasBody := operation["requestBody"].(map[string]any)
		intentionalBodyless, _ := operation["x-planext4u-bodyless-command"].(bool)
		if hasBody == intentionalBodyless {
			t.Errorf("POST %s %s must declare exactly one command style", path, operationID)
			continue
		}
		if intentionalBodyless {
			bodyless++
			if !expectedBodyless[operationID] {
				t.Errorf("POST %s unexpectedly bypasses a typed request body", path)
			}
			continue
		}
		typed++
		assertComponentRequestBody(t, "post", path, requestBody, schemas)
	}
	if typed != 7 || bodyless != len(expectedBodyless) {
		t.Fatalf("rider command coverage typed=%d bodyless=%d, want 7/%d", typed, bodyless, len(expectedBodyless))
	}
}

func assertComponentRequestBody(t *testing.T, method, path string, requestBody, schemas map[string]any) {
	t.Helper()
	if requestBody["required"] != true {
		t.Errorf("%s %s request body is not required", method, path)
		return
	}
	content, ok := requestBody["content"].(map[string]any)["application/json"].(map[string]any)
	if !ok {
		t.Errorf("%s %s does not declare application/json", method, path)
		return
	}
	ref, _ := content["schema"].(map[string]any)["$ref"].(string)
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) || schemas[strings.TrimPrefix(ref, prefix)] == nil {
		t.Errorf("%s %s has invalid request schema %q", method, path, ref)
	}
}

func TestPhase5VerticalContractsCompatibility(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/local_verticals.openapi.json", "api/compatibility/local_verticals-v1-baseline.json")
	assertOpenAPICompatibility(t, "api/openapi/emergency.openapi.json", "api/compatibility/emergency-v1-baseline.json")
	assertOpenAPICompatibility(t, "api/openapi/governance.openapi.json", "api/compatibility/governance-v1-baseline.json")
}

func TestLocalVerticalContractUsesBoundedSafeReadsAndTypedCommands(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/local_verticals.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	expectedBodyless := map[string]bool{
		"expireClassifiedListings": true,
		"publishHomeListing":       true,
		"repostClassifiedListing":  true,
	}
	typed, bodyless := 0, 0
	for path, rawMethods := range paths {
		methods := rawMethods.(map[string]any)
		rawOperation, exists := methods["post"]
		if !exists {
			continue
		}
		operation := rawOperation.(map[string]any)
		operationID := operation["operationId"].(string)
		requestBody, hasBody := operation["requestBody"].(map[string]any)
		intentionalBodyless, _ := operation["x-planext4u-bodyless-command"].(bool)
		if hasBody == intentionalBodyless {
			t.Errorf("POST %s %s must declare exactly one command style", path, operationID)
			continue
		}
		if intentionalBodyless {
			bodyless++
			if !expectedBodyless[operationID] {
				t.Errorf("POST %s unexpectedly bypasses a typed request body", path)
			}
			continue
		}
		typed++
		if requestBody["required"] != true {
			t.Errorf("POST %s request body is not required", path)
		}
		content := requestBody["content"].(map[string]any)["application/json"].(map[string]any)
		ref := content["schema"].(map[string]any)["$ref"].(string)
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(ref, prefix) || schemas[strings.TrimPrefix(ref, prefix)] == nil {
			t.Errorf("POST %s has invalid request schema %q", path, ref)
		}
	}
	if typed != 8 || bodyless != len(expectedBodyless) {
		t.Fatalf("local command coverage typed=%d bodyless=%d, want 8/%d", typed, bodyless, len(expectedBodyless))
	}

	for _, schemaName := range []string{"PublicHomeListing", "PublicClassifiedListing"} {
		properties := schemas[schemaName].(map[string]any)["properties"].(map[string]any)
		for _, sensitive := range []string{"owner_id", "latitude", "longitude", "media_asset_ids", "contact_revealed", "report_count"} {
			if _, exists := properties[sensitive]; exists {
				t.Errorf("%s exposes %s", schemaName, sensitive)
			}
		}
		media := properties["media"].(map[string]any)
		if media["maxItems"].(float64) > 20 {
			t.Errorf("%s media is not bounded", schemaName)
		}
	}
	mediaProperties := schemas["MediaPresentation"].(map[string]any)["properties"].(map[string]any)
	if _, exposed := mediaProperties["asset_id"]; exposed {
		t.Fatal("public local-vertical media exposes opaque asset identifiers")
	}

	reads := []struct {
		path       string
		pageSchema string
		parameters []string
	}{
		{"/v1/homes/listings", "HomeListingPage", []string{"q", "locality", "property_type", "purpose", "min_price", "max_price", "cursor", "limit"}},
		{"/v1/classifieds/listings", "ClassifiedListingPage", []string{"q", "category", "locality", "cursor", "limit"}},
	}
	parameters := document["components"].(map[string]any)["parameters"].(map[string]any)
	for _, read := range reads {
		operation := paths[read.path].(map[string]any)["get"].(map[string]any)
		if operation["x-planext4u-guest-readable"] != true {
			t.Errorf("GET %s is not declared guest-readable", read.path)
		}
		response := operation["responses"].(map[string]any)["200"].(map[string]any)
		ref := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
		if ref != "#/components/schemas/"+read.pageSchema {
			t.Errorf("GET %s response=%v", read.path, ref)
		}
		found := map[string]bool{}
		for _, raw := range operation["parameters"].([]any) {
			parameterRef := raw.(map[string]any)["$ref"].(string)
			parameter := parameters[strings.TrimPrefix(parameterRef, "#/components/parameters/")].(map[string]any)
			found[parameter["name"].(string)] = true
		}
		for _, name := range read.parameters {
			if !found[name] {
				t.Errorf("GET %s is missing %s", read.path, name)
			}
		}
	}
}

func TestPhase5ContractsMarkLocationContactMediaAndIdentitySensitive(t *testing.T) {
	t.Parallel()
	checks := []struct{ file, schema, property string }{
		{"api/openapi/local_verticals.openapi.json", "HomeListing", "latitude"},
		{"api/openapi/local_verticals.openapi.json", "ClassifiedListing", "contact_revealed"},
		{"api/openapi/emergency.openapi.json", "EmergencyLocation", "latitude"},
		{"api/openapi/emergency.openapi.json", "EmergencyRequest", "requester_id"},
		{"api/openapi/social.openapi.json", "SocialDirectMessage", "body"},
	}
	for _, check := range checks {
		var document map[string]any
		readJSON(t, check.file, &document)
		properties := document["components"].(map[string]any)["schemas"].(map[string]any)[check.schema].(map[string]any)["properties"].(map[string]any)
		if properties[check.property].(map[string]any)["x-planext4u-sensitive"] != true {
			t.Errorf("%s %s.%s is not sensitive", check.file, check.schema, check.property)
		}
	}
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

func TestBrowserMediaPresentationContractIsTypedAndURLBounded(t *testing.T) {
	t.Parallel()
	for _, contract := range []string{"api/openapi/media.openapi.json", "api/openapi/catalog.openapi.json"} {
		var document map[string]any
		readJSON(t, contract, &document)
		schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
		presentation := schemas["MediaPresentation"].(map[string]any)
		required := presentation["required"].([]any)
		for _, field := range []string{"asset_id", "url", "content_type", "width", "height", "alt_text", "variants", "expires_at"} {
			if !containsAny(required, field) {
				t.Errorf("%s MediaPresentation.%s is not required", contract, field)
			}
		}
		properties := presentation["properties"].(map[string]any)
		if properties["url"].(map[string]any)["x-planext4u-url-policy"] != "HTTPS or same-origin path" {
			t.Errorf("%s presentation URL policy is missing", contract)
		}
		if properties["alt_text"].(map[string]any)["minLength"] != float64(1) {
			t.Errorf("%s presentation alt text is not required", contract)
		}
	}
}

func TestCMSServiceCollectionContractIsTypedAndServerAuthoritative(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/catalog.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	home := paths["/v1/home"].(map[string]any)["get"].(map[string]any)
	parameters := home["parameters"].([]any)
	foundPostalCode := false
	for _, rawParameter := range parameters {
		parameter, ok := rawParameter.(map[string]any)
		if ok && parameter["name"] == "postal_code" && parameter["in"] == "query" {
			foundPostalCode = true
			break
		}
	}
	if !foundPostalCode {
		t.Fatal("GET /v1/home does not accept a serviceability postal code")
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	item := schemas["ServiceCollectionItem"].(map[string]any)
	required := item["required"].([]any)
	for _, field := range []string{"service_id", "provider_id", "title", "media", "price", "price_display", "serviceable", "trust", "navigation_target"} {
		if !containsAny(required, field) {
			t.Errorf("ServiceCollectionItem.%s is not required", field)
		}
	}
	properties := item["properties"].(map[string]any)
	if properties["price_display"].(map[string]any)["description"] != "Server-owned localized price display" {
		t.Fatal("service collection price display is not server-owned")
	}
	homeSchema := schemas["Home"].(map[string]any)
	if !containsAny(homeSchema["required"].([]any), "service_collections") {
		t.Fatal("Home.service_collections is not required")
	}
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

func TestAdminReportDetailsAndSignedArtifactsAreContracted(t *testing.T) {
	t.Parallel()
	var document map[string]any
	readJSON(t, "api/openapi/admin.openapi.json", &document)
	paths := document["paths"].(map[string]any)
	for _, path := range []string{
		"/admin/api/v1/reports",
		"/admin/api/v1/reports/{report_id}",
		"/admin/api/v1/reports/{report_id}/exports",
		"/admin/api/v1/report-exports/{export_id}",
		"/admin/api/v1/report-exports/{export_id}/download",
	} {
		if paths[path] == nil {
			t.Errorf("missing administrator reporting path %s", path)
		}
	}
	exportOperation := paths["/admin/api/v1/reports/{report_id}/exports"].(map[string]any)["post"].(map[string]any)
	if exportOperation["x-planext4u-fresh-auth-required"] != true || exportOperation["x-planext4u-audit-required"] != true {
		t.Fatalf("report exports must require fresh authentication and audit: %#v", exportOperation)
	}
	download := paths["/admin/api/v1/report-exports/{export_id}/download"].(map[string]any)["get"].(map[string]any)
	responses := download["responses"].(map[string]any)
	content := responses["200"].(map[string]any)["content"].(map[string]any)
	csvSchema := content["text/csv"].(map[string]any)["schema"].(map[string]any)
	if csvSchema["format"] != "binary" {
		t.Fatalf("report artifact is not a typed binary response: %#v", csvSchema)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	reportPage := components["ReportPage"].(map[string]any)
	items := reportPage["properties"].(map[string]any)["items"].(map[string]any)
	if items["maxItems"] != float64(50) {
		t.Fatalf("report page must be bounded to 50 rows: %#v", items)
	}
	exportSchema := components["ReportExport"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"checksum_sha256", "download_url", "expires_at", "content_type"} {
		if exportSchema[field] == nil {
			t.Errorf("report export omits %s", field)
		}
	}
}

func TestNotificationContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/notification.openapi.json", "api/compatibility/notification-v1-baseline.json")
}

func TestSupportContractCompatibilityBaseline(t *testing.T) {
	t.Parallel()
	assertOpenAPICompatibility(t, "api/openapi/support.openapi.json", "api/compatibility/support-v1-baseline.json")
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

func isHTTPMethod(method string) bool {
	switch strings.ToLower(method) {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return true
	default:
		return false
	}
}

func assertSchemaReferencesExist(t *testing.T, value any, components map[string]any, location string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if reference, ok := typed["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if !strings.HasPrefix(reference, prefix) {
				t.Errorf("%s uses unsupported schema reference %q", location, reference)
			} else if _, exists := components[strings.TrimPrefix(reference, prefix)]; !exists {
				t.Errorf("%s references missing schema %q", location, reference)
			}
		}
		for _, child := range typed {
			assertSchemaReferencesExist(t, child, components, location)
		}
	case []any:
		for _, child := range typed {
			assertSchemaReferencesExist(t, child, components, location)
		}
	}
}

func assertNoEmptyObjectPlaceholders(t *testing.T, value any, location string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "object" && typed["properties"] == nil && typed["additionalProperties"] == nil {
			t.Errorf("%s contains an unconstrained object placeholder", location)
		}
		for key, child := range typed {
			assertNoEmptyObjectPlaceholders(t, child, location+"."+key)
		}
	case []any:
		for index, child := range typed {
			assertNoEmptyObjectPlaceholders(t, child, location+"["+strconv.Itoa(index)+"]")
		}
	}
}
