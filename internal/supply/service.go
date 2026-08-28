package supply

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type idempotentResult struct {
	fingerprint string
	resourceID  string
}

type Service struct {
	clock        func() time.Time
	mu           sync.Mutex
	sequence     int64
	applications map[string]*Application
	catalog      map[string]*CatalogItem
	work         map[string]*WorkItem
	promotions   map[string]*Promotion
	idempotency  map[string]idempotentResult
}

func NewService(clock func() time.Time) (*Service, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{clock: clock, applications: map[string]*Application{}, catalog: map[string]*CatalogItem{}, work: map[string]*WorkItem{}, promotions: map[string]*Promotion{}, idempotency: map[string]idempotentResult{}}, nil
}

func (service *Service) Register(actor Actor, key string, request RegisterRequest) (Application, bool, error) {
	if !validActor(actor) || !hasRole(actor, "VENDOR") || !validRegister(request) || !validKey(key) {
		return Application{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	fingerprint := digest(request)
	if previous, ok := service.idempotency[idempotencyScope(actor, "register", key)]; ok {
		if previous.fingerprint != fingerprint {
			return Application{}, false, ErrIdempotencyConflict
		}
		return cloneApplication(*service.applications[actor.Subject]), true, nil
	}
	if existing := service.applications[actor.Subject]; existing != nil {
		return Application{}, false, ErrConflict
	}
	now := service.clock().UTC()
	service.sequence++
	value := &Application{ID: "vendor-application-" + sequence(service.sequence), VendorID: actor.Subject, Revision: 1, Status: StatusRegistered, BusinessName: strings.TrimSpace(request.BusinessName), BusinessType: strings.TrimSpace(request.BusinessType), ContactName: strings.TrimSpace(request.ContactName), AllowedActions: []string{"SUBMIT_DOCUMENTS"}, Timeline: []TimelineEvent{{Status: StatusRegistered, Actor: actor.Subject, CreatedAt: now}}, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.applications[actor.Subject] = value
	service.idempotency[idempotencyScope(actor, "register", key)] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneApplication(*value), false, nil
}

func (service *Service) Application(actor Actor) (Application, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.applicationFor(actor)
	if value == nil {
		return Application{}, ErrNotFound
	}
	return roleApplication(actor, *value), nil
}

func (service *Service) SubmitDocuments(actor Actor, key string, revision int64, request DocumentsRequest) (Application, bool, error) {
	if !validKey(key) || len(request.Documents) < 2 || len(request.Documents) > 10 {
		return Application{}, false, ErrInvalidRequest
	}
	for _, document := range request.Documents {
		if !safeID(document.AssetID) || !regexp.MustCompile(`^[A-Z_]{2,40}$`).MatchString(document.Kind) {
			return Application{}, false, ErrInvalidRequest
		}
	}
	return service.mutateApplication(actor, key, revision, "documents", request, func(value *Application) error {
		if !hasRole(actor, "VENDOR") || value.VendorID != actor.Subject || value.Status != StatusRegistered {
			return ErrInvalidTransition
		}
		value.Documents = cloneDocuments(request.Documents)
		for index := range value.Documents {
			value.Documents[index].OCRStatus = "PENDING"
			value.Documents[index].ExtractedFields = nil
		}
		value.Status = StatusDocumentsSubmitted
		return nil
	})
}

func (service *Service) Transition(actor Actor, key string, revision int64, request TransitionRequest) (Application, bool, error) {
	return service.TransitionForVendor(actor, key, actor.Subject, revision, request)
}

func (service *Service) TransitionForVendor(actor Actor, key, vendorID string, revision int64, request TransitionRequest) (Application, bool, error) {
	if !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "FIELD_OFFICER", "FINANCE") || len(strings.TrimSpace(request.Reason)) < 8 {
		return Application{}, false, ErrForbidden
	}
	return service.mutateApplicationByVendor(actor, vendorID, key, revision, "transition", request, func(value *Application) error {
		if !allowedApplicationTransition(value.Status, request.Status, actor) {
			return ErrInvalidTransition
		}
		if request.Status == StatusOCRReview {
			for index := range value.Documents {
				value.Documents[index].OCRStatus = "REVIEW_REQUIRED"
				value.Documents[index].ExtractedFields = map[string]string{"business_name": value.BusinessName}
			}
		}
		if request.Status == StatusKYCReview {
			for index := range value.Documents {
				value.Documents[index].OCRStatus = "VERIFIED"
			}
		}
		if request.Status == StatusApproved {
			if value.Bank == nil || value.Bank.Status != "VERIFIED" || len(value.Zones) == 0 || value.Visit == nil || value.Visit.CheckedInAt == nil {
				return ErrConflict
			}
			value.Verified = true
		}
		if request.Status == StatusRejected {
			value.Verified = false
		}
		value.Status = request.Status
		return nil
	})
}

func (service *Service) ScheduleVisit(actor Actor, key string, revision int64, request VisitRequest) (Application, bool, error) {
	if !hasAnyRole(actor, "VENDOR", "OPS_ADMIN", "SUPER_ADMIN") || request.ScheduledAt.Before(service.clock().Add(time.Hour)) || !validPoint(request.Latitude, request.Longitude) || request.AllowedRadiusM < 20 || request.AllowedRadiusM > 2000 {
		return Application{}, false, ErrInvalidRequest
	}
	return service.mutateApplication(actor, key, revision, "visit", request, func(value *Application) error {
		if value.Status != StatusFieldVisitRequired && value.Status != StatusFieldVisitScheduled {
			return ErrInvalidTransition
		}
		service.sequence++
		value.Visit = &FieldVisit{ID: "field-visit-" + sequence(service.sequence), ScheduledAt: request.ScheduledAt.UTC(), Latitude: request.Latitude, Longitude: request.Longitude, AllowedRadiusM: request.AllowedRadiusM}
		value.Status = StatusFieldVisitScheduled
		return nil
	})
}

func (service *Service) FieldCheckIn(actor Actor, key string, vendorID string, revision int64, request CheckInRequest) (Application, bool, error) {
	if !hasRole(actor, "FIELD_OFFICER") || !validPoint(request.Latitude, request.Longitude) {
		return Application{}, false, ErrForbidden
	}
	return service.mutateApplicationByVendor(actor, vendorID, key, revision, "checkin", request, func(value *Application) error {
		if value.Status != StatusFieldVisitScheduled || value.Visit == nil {
			return ErrInvalidTransition
		}
		distance := haversineMeters(request.Latitude, request.Longitude, value.Visit.Latitude, value.Visit.Longitude)
		if distance > value.Visit.AllowedRadiusM {
			return ErrForbidden
		}
		now := service.clock().UTC()
		value.Visit.OfficerID = actor.Subject
		value.Visit.CheckedInAt = &now
		value.Visit.CheckInDistance = math.Round(distance)
		value.Status = StatusFieldVisitPassed
		return nil
	})
}

func (service *Service) SetZones(actor Actor, key string, revision int64, zones []ServiceZone) (Application, bool, error) {
	if len(zones) == 0 || len(zones) > 20 {
		return Application{}, false, ErrInvalidRequest
	}
	for _, zone := range zones {
		if !safeID(zone.ID) || !validPoint(zone.Latitude, zone.Longitude) || zone.RadiusKM <= 0 || zone.RadiusKM > 100 || len(zone.PostalCodes) == 0 || !safeID(zone.PolicyVersion) {
			return Application{}, false, ErrInvalidRequest
		}
	}
	return service.mutateApplication(actor, key, revision, "zones", zones, func(value *Application) error {
		if !hasRole(actor, "VENDOR") || value.VendorID != actor.Subject || (value.Status != StatusFieldVisitPassed && value.Status != StatusBankReview) {
			return ErrInvalidTransition
		}
		value.Zones = cloneZones(zones)
		return nil
	})
}

func (service *Service) SubmitBank(actor Actor, key string, revision int64, bank BankAccount) (Application, bool, error) {
	if !hasRole(actor, "VENDOR") || !regexp.MustCompile(`^bankref_[A-Za-z0-9_-]{8,100}$`).MatchString(bank.Reference) || !regexp.MustCompile(`^[0-9]{4}$`).MatchString(bank.Last4) || !regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`).MatchString(bank.IFSC) || len(strings.TrimSpace(bank.HolderName)) < 3 {
		return Application{}, false, ErrInvalidRequest
	}
	return service.mutateApplication(actor, key, revision, "bank", bank, func(value *Application) error {
		if value.Status != StatusFieldVisitPassed && value.Status != StatusBankReview {
			return ErrInvalidTransition
		}
		bank.Status = "PENDING_VERIFICATION"
		value.Bank = &bank
		value.Status = StatusBankReview
		return nil
	})
}

func (service *Service) VerifyBank(actor Actor, key, vendorID string, revision int64, reason string) (Application, bool, error) {
	if !hasAnyRole(actor, "FINANCE", "SUPER_ADMIN") || len(strings.TrimSpace(reason)) < 8 {
		return Application{}, false, ErrForbidden
	}
	return service.mutateApplicationByVendor(actor, vendorID, key, revision, "bank-verify", reason, func(value *Application) error {
		if value.Status != StatusBankReview || value.Bank == nil {
			return ErrInvalidTransition
		}
		value.Bank.Status = "VERIFIED"
		return nil
	})
}

func (service *Service) UpsertCatalog(actor Actor, key string, currentID string, revision int64, request CatalogRequest) (CatalogItem, bool, error) {
	if !hasRole(actor, "VENDOR") || !validCatalog(request) || !validKey(key) {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	application := service.applicationFor(actor)
	if application == nil || !application.Verified {
		return CatalogItem{}, false, ErrForbidden
	}
	fingerprint := digest(struct {
		ID      string
		Request CatalogRequest
	}{currentID, request})
	scope := idempotencyScope(actor, "catalog", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return CatalogItem{}, false, ErrIdempotencyConflict
		}
		return cloneCatalog(*service.catalog[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	var value *CatalogItem
	if currentID == "" {
		service.sequence++
		id := "vendor-catalog-" + sequence(service.sequence)
		value = &CatalogItem{ID: id, VendorID: actor.Subject, Revision: 1, Stock: 0, tenantID: actor.TenantID, country: actor.Country}
		service.catalog[id] = value
	} else {
		value = service.catalog[currentID]
		if value == nil {
			return CatalogItem{}, false, ErrNotFound
		}
		if value.VendorID != actor.Subject || value.tenantID != actor.TenantID || value.country != actor.Country {
			return CatalogItem{}, false, ErrForbidden
		}
		if value.Revision != revision {
			return CatalogItem{}, false, ErrConflict
		}
		value.Revision++
	}
	value.Kind, value.Name, value.Description, value.SKU, value.Price = request.Kind, strings.TrimSpace(request.Name), strings.TrimSpace(request.Description), strings.TrimSpace(request.SKU), request.Price
	value.ApprovalStatus, value.Active, value.UpdatedAt = "PENDING_APPROVAL", false, now
	value.AllowedActions = []string{"EDIT", "SET_INVENTORY", "SET_SCHEDULE"}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneCatalog(*value), false, nil
}

func (service *Service) Catalog(actor Actor) ([]CatalogItem, error) {
	if !validActor(actor) || !hasAnyRole(actor, "VENDOR", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []CatalogItem{}
	for _, item := range service.catalog {
		if item.tenantID == actor.TenantID && item.country == actor.Country && (item.VendorID == actor.Subject || hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN")) {
			values = append(values, cloneCatalog(*item))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].UpdatedAt.After(values[j].UpdatedAt) })
	return values, nil
}

func (service *Service) ApproveCatalog(actor Actor, key, itemID string, revision int64, approved bool, reason string) (CatalogItem, bool, error) {
	if !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !validKey(key) || len(strings.TrimSpace(reason)) < 8 {
		return CatalogItem{}, false, ErrForbidden
	}
	return service.mutateCatalog(actor, key, itemID, revision, "catalog-approval", struct {
		Approved bool
		Reason   string
	}{approved, reason}, func(value *CatalogItem) error {
		if approved {
			value.ApprovalStatus, value.Active = "APPROVED", true
		} else {
			value.ApprovalStatus, value.Active = "REJECTED", false
		}
		return nil
	})
}

func (service *Service) SetInventory(actor Actor, key, itemID string, revision int64, stock int) (CatalogItem, bool, error) {
	if stock < 0 || stock > 1000000 {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	return service.mutateCatalog(actor, key, itemID, revision, "inventory", stock, func(value *CatalogItem) error {
		if !hasRole(actor, "VENDOR") || value.VendorID != actor.Subject {
			return ErrForbidden
		}
		value.Stock = stock
		return nil
	})
}

func (service *Service) SetSchedule(actor Actor, key, itemID string, revision int64, windows []ScheduleWindow) (CatalogItem, bool, error) {
	if !validSchedule(windows) {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	return service.mutateCatalog(actor, key, itemID, revision, "schedule", windows, func(value *CatalogItem) error {
		if !hasRole(actor, "VENDOR") || value.VendorID != actor.Subject {
			return ErrForbidden
		}
		value.Schedules = append([]ScheduleWindow(nil), windows...)
		return nil
	})
}

func (service *Service) Work(actor Actor) ([]WorkItem, error) {
	if !hasRole(actor, "VENDOR") {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []WorkItem{}
	for _, item := range service.work {
		if item.VendorID == actor.Subject && item.tenantID == actor.TenantID && item.country == actor.Country {
			values = append(values, *item)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].UpdatedAt.After(values[j].UpdatedAt) })
	return values, nil
}

func (service *Service) SeedWork(actor Actor, item WorkItem) error {
	if !validActor(actor) || item.ID == "" || item.VendorID == "" {
		return ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	item.tenantID, item.country = actor.TenantID, actor.Country
	service.work[item.ID] = &item
	return nil
}

func (service *Service) TransitionWork(actor Actor, key, workID, status string) (WorkItem, bool, error) {
	if !hasRole(actor, "VENDOR") || !validKey(key) {
		return WorkItem{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.work[workID]
	if value == nil {
		return WorkItem{}, false, ErrNotFound
	}
	if value.VendorID != actor.Subject || value.tenantID != actor.TenantID || value.country != actor.Country {
		return WorkItem{}, false, ErrForbidden
	}
	if !allowedWorkTransition(value.Status, status) {
		return WorkItem{}, false, ErrInvalidTransition
	}
	fingerprint := digest(status)
	scope := idempotencyScope(actor, "work:"+workID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return WorkItem{}, false, ErrIdempotencyConflict
		}
		return *value, true, nil
	}
	value.Status, value.UpdatedAt = status, service.clock().UTC()
	value.AllowedActions = workActions(status)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: workID}
	return *value, false, nil
}

func (service *Service) UpsertPromotion(actor Actor, key string, request Promotion) (Promotion, bool, error) {
	if !hasRole(actor, "VENDOR") || !validKey(key) || len(strings.TrimSpace(request.Title)) < 3 || !request.EndsAt.After(request.StartsAt) || request.Budget.AmountMinor < 0 {
		return Promotion{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "promotion", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Promotion{}, false, ErrIdempotencyConflict
		}
		return *service.promotions[previous.resourceID], true, nil
	}
	service.sequence++
	request.ID, request.VendorID, request.Status = "vendor-promotion-"+sequence(service.sequence), actor.Subject, "DRAFT"
	request.tenantID, request.country = actor.TenantID, actor.Country
	service.promotions[request.ID] = &request
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: request.ID}
	return request, false, nil
}

func (service *Service) Dashboard(actor Actor) (Dashboard, error) {
	application, err := service.Application(actor)
	if err != nil {
		return Dashboard{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := Dashboard{Application: application, Sales: Money{Currency: "INR"}, Recommendations: []string{}, OfflineSnapshotAt: service.clock().UTC()}
	for _, item := range service.catalog {
		if item.VendorID == actor.Subject {
			result.CatalogItems++
			if item.Stock < 5 {
				result.LowStockItems++
			}
		}
	}
	for _, item := range service.work {
		if item.VendorID == actor.Subject && item.Status != "COMPLETED" && item.Status != "CANCELLED" {
			result.OpenWorkItems++
		}
		if item.VendorID == actor.Subject && item.Status == "COMPLETED" {
			result.Sales.AmountMinor += item.Total.AmountMinor
		}
	}
	if result.LowStockItems > 0 {
		result.Recommendations = append(result.Recommendations, "Restock low inventory before the next demand window.")
	}
	if result.CatalogItems == 0 {
		result.Recommendations = append(result.Recommendations, "Add the first approved catalog item.")
	}
	return result, nil
}

func (service *Service) mutateApplication(actor Actor, key string, revision int64, operation string, input any, mutation func(*Application) error) (Application, bool, error) {
	return service.mutateApplicationByVendor(actor, actor.Subject, key, revision, operation, input, mutation)
}

func (service *Service) mutateApplicationByVendor(actor Actor, vendorID, key string, revision int64, operation string, input any, mutation func(*Application) error) (Application, bool, error) {
	if !validActor(actor) || !validKey(key) {
		return Application{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.applications[vendorID]
	if value == nil {
		return Application{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country || (!hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "FIELD_OFFICER", "FINANCE") && value.VendorID != actor.Subject) {
		return Application{}, false, ErrForbidden
	}
	fingerprint := digest(input)
	scope := idempotencyScope(actor, operation+":"+vendorID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Application{}, false, ErrIdempotencyConflict
		}
		return roleApplication(actor, *value), true, nil
	}
	if value.Revision != revision {
		return Application{}, false, ErrConflict
	}
	previousStatus := value.Status
	if err := mutation(value); err != nil {
		return Application{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	value.AllowedActions = applicationActions(*value)
	if value.Status != previousStatus {
		reason := ""
		if transition, ok := input.(TransitionRequest); ok {
			reason = strings.TrimSpace(transition.Reason)
		}
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: actor.Subject, Reason: reason, CreatedAt: value.UpdatedAt})
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return roleApplication(actor, *value), false, nil
}

func (service *Service) mutateCatalog(actor Actor, key, itemID string, revision int64, operation string, input any, mutation func(*CatalogItem) error) (CatalogItem, bool, error) {
	if !validActor(actor) || !validKey(key) {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.catalog[itemID]
	if value == nil {
		return CatalogItem{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return CatalogItem{}, false, ErrForbidden
	}
	fingerprint := digest(input)
	scope := idempotencyScope(actor, operation+":"+itemID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return CatalogItem{}, false, ErrIdempotencyConflict
		}
		return cloneCatalog(*value), true, nil
	}
	if value.Revision != revision {
		return CatalogItem{}, false, ErrConflict
	}
	if err := mutation(value); err != nil {
		return CatalogItem{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	value.AllowedActions = catalogActions(actor, *value)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneCatalog(*value), false, nil
}

func (service *Service) applicationFor(actor Actor) *Application {
	if hasRole(actor, "VENDOR") {
		value := service.applications[actor.Subject]
		if value != nil && value.tenantID == actor.TenantID && value.country == actor.Country {
			return value
		}
	}
	return nil
}

func allowedApplicationTransition(from, to ApplicationStatus, actor Actor) bool {
	allowed := map[ApplicationStatus][]ApplicationStatus{
		StatusDocumentsSubmitted: {StatusOCRReview}, StatusOCRReview: {StatusKYCReview, StatusRejected}, StatusKYCReview: {StatusFieldVisitRequired, StatusRejected}, StatusBankReview: {StatusApproved, StatusRejected},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			if to == StatusApproved && !hasAnyRole(actor, "SUPER_ADMIN", "OPS_ADMIN") {
				return false
			}
			return true
		}
	}
	return false
}

func applicationActions(value Application) []string {
	switch value.Status {
	case StatusRegistered:
		return []string{"SUBMIT_DOCUMENTS"}
	case StatusFieldVisitRequired, StatusFieldVisitScheduled:
		return []string{"SCHEDULE_FIELD_VISIT"}
	case StatusFieldVisitPassed:
		return []string{"SET_ZONES", "SUBMIT_BANK"}
	case StatusBankReview:
		return []string{"SET_ZONES", "SUBMIT_BANK"}
	case StatusApproved:
		return []string{"MANAGE_CATALOG", "VIEW_SETTLEMENTS", "CREATE_PROMOTION"}
	default:
		return []string{}
	}
}

func roleApplication(actor Actor, value Application) Application {
	result := cloneApplication(value)
	if !hasAnyRole(actor, "VENDOR", "FIELD_OFFICER", "OPS_ADMIN", "SUPER_ADMIN", "FINANCE") {
		result.Documents, result.Bank = nil, nil
	}
	return result
}

func allowedWorkTransition(from, to string) bool {
	allowed := map[string][]string{"NEW": {"ACCEPTED", "REJECTED"}, "ACCEPTED": {"PREPARING", "EN_ROUTE"}, "PREPARING": {"READY"}, "READY": {"COMPLETED"}, "EN_ROUTE": {"COMPLETED"}}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

func workActions(status string) []string {
	switch status {
	case "NEW":
		return []string{"ACCEPT", "REJECT"}
	case "ACCEPTED":
		return []string{"PREPARING", "EN_ROUTE"}
	case "PREPARING":
		return []string{"READY"}
	case "READY", "EN_ROUTE":
		return []string{"COMPLETE"}
	default:
		return []string{}
	}
}

func validSchedule(values []ScheduleWindow) bool {
	if len(values) == 0 || len(values) > 28 {
		return false
	}
	for index, value := range values {
		if value.Weekday < 1 || value.Weekday > 7 || value.StartsMinute < 0 || value.EndsMinute > 1440 || value.EndsMinute <= value.StartsMinute || value.Capacity < 1 || value.BufferMinute < 0 || strings.TrimSpace(value.TimeZone) == "" {
			return false
		}
		for other := index + 1; other < len(values); other++ {
			candidate := values[other]
			if value.Weekday == candidate.Weekday && value.StartsMinute < candidate.EndsMinute && candidate.StartsMinute < value.EndsMinute {
				return false
			}
		}
	}
	return true
}

func validRegister(value RegisterRequest) bool {
	return len(strings.TrimSpace(value.BusinessName)) >= 3 && len(strings.TrimSpace(value.BusinessType)) >= 3 && len(strings.TrimSpace(value.ContactName)) >= 3
}
func validCatalog(value CatalogRequest) bool {
	return (value.Kind == CatalogProduct || value.Kind == CatalogService || value.Kind == CatalogFood) && len(strings.TrimSpace(value.Name)) >= 3 && len(strings.TrimSpace(value.Description)) >= 3 && safeID(value.SKU) && value.Price.AmountMinor >= 0 && regexp.MustCompile(`^[A-Z]{3}$`).MatchString(value.Price.Currency)
}
func validActor(value Actor) bool {
	return safeID(value.TenantID) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(value.Country) && safeID(value.Subject) && len(value.Roles) > 0
}
func validKey(value string) bool { return len(value) >= 16 && len(value) <= 128 && safeID(value) }
func safeID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(value)
}
func validPoint(latitude, longitude float64) bool {
	return latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180
}
func hasRole(actor Actor, role string) bool {
	for _, candidate := range actor.Roles {
		if strings.TrimSpace(candidate) == role {
			return true
		}
	}
	return false
}
func hasAnyRole(actor Actor, roles ...string) bool {
	for _, role := range roles {
		if hasRole(actor, role) {
			return true
		}
	}
	return false
}
func sequence(value int64) string {
	return time.Unix(value, 0).UTC().Format("150405") + strings.Repeat("0", int(value%3))
}
func idempotencyScope(actor Actor, operation, key string) string {
	return actor.TenantID + ":" + actor.Country + ":" + actor.Subject + ":" + operation + ":" + key
}
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func cloneApplication(value Application) Application {
	value.Documents = cloneDocuments(value.Documents)
	value.Zones = cloneZones(value.Zones)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	value.Timeline = append([]TimelineEvent(nil), value.Timeline...)
	if value.Visit != nil {
		copy := *value.Visit
		value.Visit = &copy
	}
	if value.Bank != nil {
		copy := *value.Bank
		value.Bank = &copy
	}
	return value
}
func cloneDocuments(values []Document) []Document {
	result := append([]Document(nil), values...)
	for index := range result {
		if result[index].ExtractedFields != nil {
			result[index].ExtractedFields = map[string]string{}
			for key, value := range values[index].ExtractedFields {
				result[index].ExtractedFields[key] = value
			}
		}
	}
	return result
}
func cloneZones(values []ServiceZone) []ServiceZone {
	result := append([]ServiceZone(nil), values...)
	for index := range result {
		result[index].PostalCodes = append([]string(nil), values[index].PostalCodes...)
	}
	return result
}
func cloneCatalog(value CatalogItem) CatalogItem {
	value.Schedules = append([]ScheduleWindow(nil), value.Schedules...)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}
func catalogActions(actor Actor, value CatalogItem) []string {
	if hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
		return []string{"APPROVE", "REJECT"}
	}
	actions := []string{"EDIT", "SET_INVENTORY", "SET_SCHEDULE"}
	if value.ApprovalStatus == "APPROVED" {
		actions = append(actions, "PAUSE")
	}
	return actions
}
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const radius = 6371000.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dlat, dlon := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return radius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
