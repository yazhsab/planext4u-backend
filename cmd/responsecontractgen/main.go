// Command responsecontractgen fills successful OpenAPI response schemas from
// the Go response types used by the HTTP handlers. It is intentionally
// additive: existing hand-authored schemas and operation metadata win.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/booking"
	"github.com/yazhsab/planext4u-backend/internal/checkout"
	"github.com/yazhsab/planext4u-backend/internal/emergency"
	"github.com/yazhsab/planext4u-backend/internal/food"
	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
	"github.com/yazhsab/planext4u-backend/internal/governance"
	"github.com/yazhsab/planext4u-backend/internal/localverticals"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/social"
	"github.com/yazhsab/planext4u-backend/internal/supply"
)

type responseSpec struct {
	direct   reflect.Type
	list     reflect.Type
	countKey string
	fields   map[string]reflect.Type
}

func typeOf[T any]() reflect.Type   { return reflect.TypeOf((*T)(nil)).Elem() }
func direct[T any]() responseSpec   { return responseSpec{direct: typeOf[T]()} }
func list[T any]() responseSpec     { return responseSpec{list: typeOf[T]()} }
func count(key string) responseSpec { return responseSpec{countKey: key} }

func object(fields map[string]reflect.Type) responseSpec {
	return responseSpec{fields: fields}
}

type schemaBuilder struct {
	components map[string]any
	aliases    map[reflect.Type]string
	building   map[string]bool
}

func (builder *schemaBuilder) response(spec responseSpec) map[string]any {
	switch {
	case spec.direct != nil:
		return builder.schema(spec.direct)
	case spec.list != nil:
		return map[string]any{
			"type":                 "object",
			"required":             []string{"items"},
			"additionalProperties": false,
			"properties": map[string]any{
				"items": map[string]any{"type": "array", "items": builder.schema(spec.list)},
			},
		}
	case spec.countKey != "":
		return map[string]any{
			"type":                 "object",
			"required":             []string{spec.countKey},
			"additionalProperties": false,
			"properties": map[string]any{
				spec.countKey: map[string]any{"type": "integer", "minimum": 0},
			},
		}
	case len(spec.fields) > 0:
		properties := make(map[string]any, len(spec.fields))
		required := make([]string, 0, len(spec.fields))
		for name, fieldType := range spec.fields {
			properties[name] = builder.schema(fieldType)
			required = append(required, name)
		}
		sort.Strings(required)
		return map[string]any{
			"type":                 "object",
			"required":             required,
			"additionalProperties": false,
			"properties":           properties,
		}
	default:
		panic("empty response specification")
	}
}

func (builder *schemaBuilder) schema(valueType reflect.Type) map[string]any {
	if valueType.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{builder.schema(valueType.Elem()), map[string]any{"type": "null"}}}
	}
	if valueType == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if valueType == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}

	if componentName, ok := builder.aliases[valueType]; ok {
		builder.mergeComponent(componentName, valueType)
		return map[string]any{"$ref": "#/components/schemas/" + componentName}
	}
	if valueType.Name() != "" && valueType.Kind() == reflect.Struct {
		componentName := valueType.Name()
		builder.addComponent(componentName, valueType)
		return map[string]any{"$ref": "#/components/schemas/" + componentName}
	}

	switch valueType.Kind() {
	case reflect.Struct:
		return builder.structSchema(valueType)
	case reflect.Slice, reflect.Array:
		if valueType.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": builder.schema(valueType.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": builder.schema(valueType.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	default:
		return map[string]any{}
	}
}

func (builder *schemaBuilder) addComponent(name string, valueType reflect.Type) {
	if _, exists := builder.components[name]; exists || builder.building[name] {
		return
	}
	builder.building[name] = true
	if valueType.Kind() == reflect.Struct {
		builder.components[name] = builder.structSchema(valueType)
	} else {
		builder.components[name] = primitiveSchema(valueType)
	}
	delete(builder.building, name)
}

func primitiveSchema(valueType reflect.Type) map[string]any {
	switch valueType.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{}
	}
}

func (builder *schemaBuilder) mergeComponent(name string, valueType reflect.Type) {
	if builder.building[name] {
		return
	}
	existing, exists := builder.components[name]
	if !exists {
		builder.addComponent(name, valueType)
		return
	}
	existingSchema, ok := existing.(map[string]any)
	if !ok || valueType.Kind() != reflect.Struct {
		return
	}
	builder.building[name] = true
	generated := builder.structSchema(valueType)
	mergeSchema(existingSchema, generated)
	delete(builder.building, name)
}

func mergeSchema(target, source map[string]any) {
	if isEmptyObjectPlaceholder(target) {
		clear(target)
		for key, value := range source {
			target[key] = value
		}
		return
	}
	for key, sourceValue := range source {
		targetValue, exists := target[key]
		if !exists {
			target[key] = sourceValue
			continue
		}
		if key == "required" {
			target[key] = unionStrings(targetValue, sourceValue)
			continue
		}
		targetMap, targetIsMap := targetValue.(map[string]any)
		sourceMap, sourceIsMap := sourceValue.(map[string]any)
		if targetIsMap && sourceIsMap {
			mergeSchema(targetMap, sourceMap)
		}
	}
}

func isEmptyObjectPlaceholder(value map[string]any) bool {
	return len(value) == 1 && value["type"] == "object"
}

func unionStrings(left, right any) []any {
	seen := map[string]bool{}
	result := []any{}
	for _, values := range []any{left, right} {
		list := reflect.ValueOf(values)
		if list.Kind() != reflect.Slice {
			continue
		}
		for index := 0; index < list.Len(); index++ {
			text, ok := list.Index(index).Interface().(string)
			if ok && !seen[text] {
				seen[text] = true
				result = append(result, text)
			}
		}
	}
	return result
}

func (builder *schemaBuilder) structSchema(valueType reflect.Type) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for index := 0; index < valueType.NumField(); index++ {
		field := valueType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		optional := false
		for _, option := range parts[1:] {
			optional = optional || option == "omitempty"
		}
		properties[name] = builder.schema(field.Type)
		if !optional {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	result := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func aliases() map[reflect.Type]string {
	return map[reflect.Type]string{
		typeOf[booking.Money](): "Money", typeOf[booking.PaymentMode](): "PaymentMode",
		typeOf[booking.Offering](): "ServiceOffering", typeOf[booking.Slot](): "ServiceSlot",
		typeOf[booking.HoldStatus](): "SlotHoldStatus", typeOf[booking.SlotHold](): "SlotHold",
		typeOf[booking.Status](): "BookingStatus", typeOf[booking.TimelineEvent](): "TimelineEvent",
		typeOf[booking.CompletionEvidence](): "CompletionEvidence", typeOf[booking.Booking](): "ServiceBooking",

		typeOf[emergency.Location](): "EmergencyLocation", typeOf[emergency.Request](): "EmergencyRequest",
		typeOf[emergency.Message](): "EmergencyMessage", typeOf[emergency.SLAReport](): "EmergencySLAReport",

		typeOf[food.Money](): "Money", typeOf[food.Cart](): "FoodCart", typeOf[food.Order](): "FoodOrder",
		typeOf[food.Payment](): "FoodPayment", typeOf[food.OrderStatus](): "FoodOrderStatus",

		typeOf[fulfillment.RiderStatus](): "RiderStatus", typeOf[fulfillment.RiderProfile](): "RiderProfile",
		typeOf[fulfillment.DeliveryTask](): "DeliveryTask", typeOf[fulfillment.Conversation](): "Conversation",
		typeOf[fulfillment.ChatMessage](): "ChatMessage", typeOf[fulfillment.LedgerEntry](): "LedgerEntry",
		typeOf[fulfillment.Payout](): "Payout",

		typeOf[governance.ReportCard](): "GovernanceReport", typeOf[governance.MapCell](): "GovernanceMapCell",
		typeOf[governance.LeaderboardEntry](): "GovernanceLeaderboardEntry", typeOf[governance.Insight](): "GovernanceInsight",

		typeOf[localverticals.Money](): "Money", typeOf[localverticals.HomeEstimate](): "HomeEstimate",
		typeOf[localverticals.HomeListing](): "HomeListing", typeOf[localverticals.ClassifiedListing](): "ClassifiedListing",

		typeOf[social.Profile](): "SocialProfile", typeOf[social.PostStatus](): "SocialPostStatus",
		typeOf[social.Post](): "SocialPost", typeOf[social.Comment](): "SocialComment",
		typeOf[social.Report](): "SocialReport", typeOf[social.MediaState](): "SocialMediaState",
		typeOf[social.MediaJob](): "SocialMediaJob", typeOf[social.EphemeralContent](): "SocialEphemeral",
		typeOf[social.Conversation](): "SocialConversation", typeOf[social.DirectMessage](): "SocialDirectMessage",

		typeOf[supply.ApplicationStatus](): "ApplicationStatus", typeOf[supply.Application](): "VendorApplication",
		typeOf[supply.Document](): "VendorDocument", typeOf[supply.BankAccount](): "BankAccount",
		typeOf[supply.CatalogKind](): "CatalogKind", typeOf[supply.CatalogItem](): "VendorCatalogItem",

		typeOf[checkout.Money](): "Money", typeOf[checkout.Address](): "Address", typeOf[checkout.Quote](): "Quote",
		typeOf[checkout.PlaceResult](): "PlaceOrderResult", typeOf[payment.Method](): "PaymentMethod",
		typeOf[payment.Status](): "PaymentStatus", typeOf[payment.ClientHandoff](): "ClientHandoff",
		typeOf[payment.Payment](): "Payment", typeOf[order.Status](): "OrderStatus", typeOf[order.Order](): "Order",
	}
}

func responseSpecs() map[string]responseSpec {
	specs := map[string]responseSpec{}
	add := func(spec responseSpec, operationIDs ...string) {
		for _, operationID := range operationIDs {
			specs[operationID] = spec
		}
	}

	add(direct[booking.Booking](), "bookingProviderTransition", "bookingStart", "bookingSubmitCompletion", "bookingConfirmCompletion", "bookingReportNoShow", "bookingOpenDispute")

	add(list[emergency.Request](), "listEmergencyRequests")
	add(direct[emergency.Request](), "createEmergencyRequest", "getEmergencyRequest", "acceptEmergencyRequest", "updateEmergencyLocation", "transitionEmergencyRequest")
	add(list[emergency.Message](), "listEmergencyCommunications")
	add(direct[emergency.Message](), "sendEmergencyCommunication")
	add(count("escalated"), "runEmergencyEscalations")
	add(direct[emergency.SLAReport](), "getEmergencySLAReport")

	add(list[food.Restaurant](), "listRestaurants")
	add(list[food.MenuItem](), "getRestaurantMenu")
	add(direct[food.Cart](), "priceFoodCart")
	add(list[food.Order](), "listFoodOrders")
	add(direct[food.Order](), "createFoodOrder", "getFoodOrder", "transitionRestaurantOrder", "transitionFoodDispatch")

	add(direct[fulfillment.RiderProfile](), "registerRider", "getRiderProfile", "reviewRider")
	add(direct[fulfillment.DutySession](), "startRiderDuty", "getRiderDuty", "endRiderDuty")
	add(list[fulfillment.DeliveryTask](), "listRiderOffers", "listRiderTasks")
	add(direct[fulfillment.DeliveryTask](), "acceptRiderOffer", "markRiderPickup", "completeRiderDelivery", "createDispatchTask", "offerDispatchTask", "reassignDispatchTask")
	add(direct[fulfillment.RiderLocation](), "updateRiderLocation", "getRiderLocation")
	add(list[fulfillment.OfflineResult](), "recoverRiderOfflineCommands")
	add(count("flagged"), "sweepStaleAssignments")
	add(direct[fulfillment.Conversation](), "getOrderChat", "blockOrderChat")
	add(direct[fulfillment.ChatMessage](), "sendOrderChatMessage", "recordOrderChatReceipt")
	add(list[fulfillment.LedgerEntry](), "listSettlementLedger")
	add(direct[fulfillment.LedgerEntry](), "seedSettlementEntry")
	add(list[fulfillment.Payout](), "listPayouts")
	add(direct[fulfillment.Payout](), "requestPayout", "approvePayout", "executePayout")
	add(direct[fulfillment.Reconciliation](), "reconcileSettlement")
	add(list[fulfillment.Territory](), "listTerritories")
	add(direct[fulfillment.FieldCheckIn](), "recordFieldCheckIn")
	add(list[fulfillment.AttendanceEntry](), "listAttendance")
	add(direct[fulfillment.RegionalDashboard](), "getRegionalDashboard")
	add(list[fulfillment.AuditEvent](), "listFulfillmentAudit")

	add(direct[governance.Dashboard](), "getGovernanceDashboard")
	generatedAt := typeOf[time.Time]()
	privacy := typeOf[string]()
	add(object(map[string]reflect.Type{"generated_at": generatedAt, "items": typeOf[[]governance.ReportCard](), "privacy_mode": privacy}), "listGovernanceReports")
	add(object(map[string]reflect.Type{"generated_at": generatedAt, "items": typeOf[[]governance.MapCell](), "privacy_mode": privacy}), "getGovernanceMaps")
	add(object(map[string]reflect.Type{"generated_at": generatedAt, "items": typeOf[[]governance.LeaderboardEntry](), "privacy_mode": privacy}), "getGovernanceLeaderboards")
	add(object(map[string]reflect.Type{"generated_at": generatedAt, "items": typeOf[[]governance.Insight](), "privacy_mode": privacy}), "getGovernanceIntelligence")
	add(object(map[string]reflect.Type{"generated_at": generatedAt, "items": typeOf[[]governance.CountryControl]()}), "listGovernanceCountries")

	add(list[localverticals.HomeListing](), "searchHomes")
	add(direct[localverticals.HomeListing](), "createHomeListing", "getHomeListing", "publishHomeListing", "upgradeHomeListing")
	add(direct[localverticals.HomeEstimate](), "getHomeEstimate")
	add(direct[localverticals.Inquiry](), "createHomeInquiry")
	add(direct[localverticals.Visit](), "scheduleHomeVisit")
	add(list[localverticals.ClassifiedListing](), "browseClassifieds")
	add(direct[localverticals.ClassifiedListing](), "createClassifiedListing", "getClassifiedListing", "revealClassifiedContact", "repostClassifiedListing", "reportClassifiedListing", "upgradeClassifiedListing")
	add(count("expired"), "expireClassifiedListings")

	add(direct[social.FeedPage](), "getSocialFeed")
	add(direct[social.Post](), "createSocialPost", "getSocialPost", "setSocialPostLike", "setSocialPostSave", "shareSocialPost")
	add(list[social.Comment](), "listSocialComments")
	add(direct[social.Comment](), "createSocialComment")
	add(direct[social.Report](), "reportSocialPost", "decideModerationReport")
	add(direct[social.Profile](), "getSocialProfile", "setSocialRelationship")
	add(direct[social.Follow](), "followSocialProfile", "acceptSocialFollow")
	add(direct[social.MediaJob](), "appealSocialMedia", "processSocialMedia", "decideSocialMediaAppeal")
	add(list[social.EphemeralContent](), "listSocialEphemeral")
	add(direct[social.EphemeralContent](), "createSocialEphemeral", "setSocialHighlight")
	add(direct[social.Collection](), "createSocialCollection", "setSocialCollectionPost")
	add(list[social.Conversation](), "listSocialConversations")
	add(direct[social.Conversation](), "openSocialConversation", "acceptSocialConversation")
	add(list[social.DirectMessage](), "listSocialMessages")
	add(direct[social.DirectMessage](), "sendSocialMessage")
	add(direct[social.Presence](), "setSocialPresence", "getSocialPresence")
	add(direct[social.CallSession](), "createSocialCall", "signalSocialCall")
	add(list[social.Report](), "listModerationReports")
	add(count("purged"), "purgeSocialRetention")

	add(direct[supply.Application](), "registerVendor", "getVendorApplication", "submitVendorDocuments", "scheduleVendorFieldVisit", "setVendorZones", "submitVendorBank", "transitionVendorApplication", "checkInVendorFieldVisit", "verifyVendorBank")
	add(direct[supply.Dashboard](), "getVendorDashboard")
	add(list[supply.CatalogItem](), "listVendorCatalog")
	add(direct[supply.CatalogItem](), "createVendorCatalogItem", "updateVendorCatalogItem", "setVendorInventory", "setVendorSchedule", "approveVendorCatalog")
	add(list[supply.WorkItem](), "listVendorWork")
	add(direct[supply.WorkItem](), "transitionVendorWork")
	add(direct[supply.Promotion](), "createVendorPromotion")

	add(list[checkout.DeliverySlot](), "transactionListDeliverySlots")
	add(direct[payment.Payment](), "transactionAcceptProviderWebhook")
	add(list[order.Order](), "transactionListOrders")
	add(direct[order.Order](), "transactionRequestCancellation", "transactionConfirmDelivery", "transactionRequestReturn", "transactionRateOrder")
	return specs
}

func rootsForContract(fileName string) []reflect.Type {
	switch fileName {
	case "booking.openapi.json":
		return []reflect.Type{typeOf[booking.Booking]()}
	case "emergency.openapi.json":
		return []reflect.Type{typeOf[emergency.Request](), typeOf[emergency.Message](), typeOf[emergency.SLAReport]()}
	case "food.openapi.json":
		return []reflect.Type{typeOf[food.Restaurant](), typeOf[food.MenuItem](), typeOf[food.Cart](), typeOf[food.Order]()}
	case "fulfillment.openapi.json":
		return []reflect.Type{
			typeOf[fulfillment.RiderProfile](), typeOf[fulfillment.DeliveryTask](), typeOf[fulfillment.Conversation](),
			typeOf[fulfillment.ChatMessage](), typeOf[fulfillment.LedgerEntry](), typeOf[fulfillment.Payout](),
		}
	case "governance.openapi.json":
		return []reflect.Type{typeOf[governance.Dashboard]()}
	case "local_verticals.openapi.json":
		return []reflect.Type{typeOf[localverticals.HomeListing](), typeOf[localverticals.HomeEstimate](), typeOf[localverticals.ClassifiedListing]()}
	case "social.openapi.json":
		return []reflect.Type{
			typeOf[social.FeedPage](), typeOf[social.Post](), typeOf[social.Profile](), typeOf[social.Comment](),
			typeOf[social.Report](), typeOf[social.MediaJob](), typeOf[social.EphemeralContent](),
			typeOf[social.Conversation](), typeOf[social.DirectMessage](),
		}
	case "supply.openapi.json":
		return []reflect.Type{typeOf[supply.Application](), typeOf[supply.Dashboard](), typeOf[supply.CatalogItem](), typeOf[supply.WorkItem](), typeOf[supply.Promotion]()}
	case "transaction.openapi.json":
		return []reflect.Type{typeOf[checkout.DeliverySlot](), typeOf[checkout.Quote](), typeOf[checkout.PlaceResult](), typeOf[payment.Payment](), typeOf[order.Order]()}
	default:
		return nil
	}
}

func main() {
	check := flag.Bool("check", false, "fail instead of writing when response contracts are stale")
	flag.Parse()
	specs := responseSpecs()
	files, err := filepath.Glob("api/openapi/*.openapi.json")
	if err != nil {
		panic(err)
	}
	updated := 0
	changedContracts := 0
	for _, contractPath := range files {
		body, readErr := os.ReadFile(contractPath)
		if readErr != nil {
			panic(readErr)
		}
		var document map[string]any
		if unmarshalErr := json.Unmarshal(body, &document); unmarshalErr != nil {
			panic(fmt.Errorf("%s: %w", contractPath, unmarshalErr))
		}
		beforeCanonical, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			panic(marshalErr)
		}
		components := document["components"].(map[string]any)["schemas"].(map[string]any)
		builder := &schemaBuilder{components: components, aliases: aliases(), building: map[string]bool{}}
		for _, rootType := range rootsForContract(filepath.Base(contractPath)) {
			builder.schema(rootType)
		}
		paths := document["paths"].(map[string]any)
		fileUpdated := 0
		for _, pathValue := range paths {
			pathItem := pathValue.(map[string]any)
			for method, operationValue := range pathItem {
				if !map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}[method] {
					continue
				}
				operation := operationValue.(map[string]any)
				operationID, _ := operation["operationId"].(string)
				spec, known := specs[operationID]
				responses, ok := operation["responses"].(map[string]any)
				if !ok {
					continue
				}
				for status, responseValue := range responses {
					if len(status) != 3 || status[0] != '2' || status == "204" {
						continue
					}
					response := responseValue.(map[string]any)
					content, _ := response["content"].(map[string]any)
					media, _ := content["application/json"].(map[string]any)
					if media != nil && media["schema"] != nil {
						continue
					}
					if !known {
						panic(fmt.Sprintf("%s: no response mapping for %s", contractPath, operationID))
					}
					if content == nil {
						content = map[string]any{}
						response["content"] = content
					}
					if media == nil {
						media = map[string]any{}
						content["application/json"] = media
					}
					media["schema"] = builder.response(spec)
					fileUpdated++
				}
			}
		}
		afterCanonical, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			panic(marshalErr)
		}
		if fileUpdated == 0 && bytes.Equal(beforeCanonical, afterCanonical) {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "%s has stale response contracts; run go run ./cmd/responsecontractgen\n", contractPath)
			os.Exit(1)
		}
		formatted, marshalErr := json.MarshalIndent(document, "", "  ")
		if marshalErr != nil {
			panic(marshalErr)
		}
		formatted = append(formatted, '\n')
		if writeErr := os.WriteFile(contractPath, formatted, 0o644); writeErr != nil {
			panic(writeErr)
		}
		fmt.Printf("%s: synchronized response contracts (%d newly attached)\n", contractPath, fileUpdated)
		updated += fileUpdated
		changedContracts++
	}
	if changedContracts == 0 {
		fmt.Println("all successful responses already have schemas")
	} else {
		fmt.Printf("synchronized %d contracts; attached %d response schemas\n", changedContracts, updated)
	}
}
