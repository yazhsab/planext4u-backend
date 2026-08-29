package localverticals

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type idem struct{ fingerprint, resourceID string }

type Service struct {
	clock       func() time.Time
	config      Configuration
	mu          sync.Mutex
	sequence    int64
	homes       map[string]*HomeListing
	classifieds map[string]*ClassifiedListing
	inquiries   map[string]Inquiry
	visits      map[string]Visit
	verified    map[string]bool
	idempotency map[string]idem
}

var (
	safeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
)

func NewService(config Configuration, clock func() time.Time) (*Service, error) {
	if clock == nil || !safeID(config.TenantID) || !countryPattern.MatchString(config.Country) || len(config.Currency) != 3 {
		return nil, ErrInvalidRequest
	}
	service := &Service{clock: clock, config: config, homes: map[string]*HomeListing{}, classifieds: map[string]*ClassifiedListing{}, inquiries: map[string]Inquiry{}, visits: map[string]Visit{}, verified: map[string]bool{}, idempotency: map[string]idem{}}
	for _, owner := range config.KYCVerifiedOwners {
		if !safeID(owner) {
			return nil, ErrInvalidRequest
		}
		service.verified[owner] = true
	}
	for index := range config.Homes {
		value := cloneHome(config.Homes[index])
		if !safeID(value.ID) || !validHome(value.Title, value.PropertyType, value.Purpose, value.Locality, value.AreaSqFt, value.Bedrooms, value.Price, value.Latitude, value.Longitude) {
			return nil, ErrInvalidRequest
		}
		value.tenantID, value.country = config.TenantID, config.Country
		if value.Revision == 0 {
			value.Revision = 1
		}
		if value.Status == "" {
			value.Status = "ACTIVE"
		}
		if value.Plan == "" {
			value.Plan = "STANDARD"
		}
		if value.Estimate.Version == "" {
			value.Estimate = service.estimate(value)
		}
		service.homes[value.ID] = &value
	}
	for index := range config.Classifieds {
		value := cloneClassified(config.Classifieds[index])
		if !safeID(value.ID) || strings.TrimSpace(value.Title) == "" {
			return nil, ErrInvalidRequest
		}
		value.tenantID, value.country = config.TenantID, config.Country
		if value.Revision == 0 {
			value.Revision = 1
		}
		if value.Status == "" {
			value.Status = "PUBLISHED"
		}
		if value.Plan == "" {
			value.Plan = "STANDARD"
		}
		value.contact = strings.TrimSpace(config.SeedContacts[value.ID])
		if value.contact != "" {
			value.ContactMasked = maskContact(value.contact)
		}
		service.classifieds[value.ID] = &value
	}
	return service, nil
}

func (service *Service) SearchHomes(actor Actor, filter HomeSearch) ([]HomeListing, error) {
	if !customer(actor) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	query, locality := strings.ToLower(strings.TrimSpace(filter.Query)), strings.ToLower(strings.TrimSpace(filter.Locality))
	values := []HomeListing{}
	for _, item := range service.homes {
		if item.tenantID != actor.TenantID || item.country != actor.Country || item.Status != "ACTIVE" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Locality+" "+strings.Join(item.Amenities, " ")), query) {
			continue
		}
		if locality != "" && !strings.Contains(strings.ToLower(item.Locality), locality) {
			continue
		}
		if filter.PropertyType != "" && !strings.EqualFold(item.PropertyType, filter.PropertyType) {
			continue
		}
		if filter.Purpose != "" && !strings.EqualFold(item.Purpose, filter.Purpose) {
			continue
		}
		if filter.MinPrice > 0 && item.Price.AmountMinor < filter.MinPrice {
			continue
		}
		if filter.MaxPrice > 0 && item.Price.AmountMinor > filter.MaxPrice {
			continue
		}
		values = append(values, presentHome(actor, *item))
	}
	sort.Slice(values, func(i, j int) bool {
		if (values[i].FeaturedUntil != nil) != (values[j].FeaturedUntil != nil) {
			return values[i].FeaturedUntil != nil
		}
		return values[i].UpdatedAt.After(values[j].UpdatedAt)
	})
	return values, nil
}

func (service *Service) Home(actor Actor, id string) (HomeListing, error) {
	if !customer(actor) || !safeID(id) {
		return HomeListing{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.homes[id]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country || (value.Status != "ACTIVE" && value.OwnerID != actor.Subject) {
		return HomeListing{}, ErrNotFound
	}
	return presentHome(actor, *value), nil
}

func (service *Service) CreateHome(actor Actor, key string, request HomeListingRequest) (HomeListing, bool, error) {
	if !customer(actor) || !validKey(key) || !validHome(request.Title, request.PropertyType, request.Purpose, request.Locality, request.AreaSqFt, request.Bedrooms, request.Price, request.Latitude, request.Longitude) || len(request.MediaAssetIDs) > 20 {
		return HomeListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	scope, fingerprint := idemScope(actor, "home-create", key), digest(request)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return HomeListing{}, false, ErrIdempotencyConflict
		}
		return presentHome(actor, *service.homes[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	value := &HomeListing{ID: service.next("home-listing"), Revision: 1, OwnerID: actor.Subject, Title: strings.TrimSpace(request.Title), PropertyType: strings.ToUpper(request.PropertyType), Purpose: strings.ToUpper(request.Purpose), Locality: strings.TrimSpace(request.Locality), Latitude: request.Latitude, Longitude: request.Longitude, AreaSqFt: request.AreaSqFt, Bedrooms: request.Bedrooms, Price: request.Price, Amenities: cleanList(request.Amenities), MediaAssetIDs: cleanList(request.MediaAssetIDs), Status: "DRAFT", KYCVerified: service.verified[actor.Subject], Plan: "STANDARD", CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	value.Estimate = service.estimate(*value)
	service.homes[value.ID], service.idempotency[scope] = value, idem{fingerprint, value.ID}
	return presentHome(actor, *value), false, nil
}

func (service *Service) PublishHome(actor Actor, key, id string, revision int64) (HomeListing, bool, error) {
	if !customer(actor) || !validKey(key) || !safeID(id) || revision < 1 {
		return HomeListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.homes[id]
	if value == nil || value.OwnerID != actor.Subject {
		return HomeListing{}, false, ErrNotFound
	}
	scope := idemScope(actor, "home-publish:"+id, key)
	if _, ok := service.idempotency[scope]; ok {
		return presentHome(actor, *value), true, nil
	}
	if !value.KYCVerified {
		return HomeListing{}, false, ErrForbidden
	}
	if value.Revision != revision || value.Status != "DRAFT" {
		return HomeListing{}, false, ErrConflict
	}
	value.Status, value.Revision, value.UpdatedAt = "ACTIVE", value.Revision+1, service.clock().UTC()
	service.idempotency[scope] = idem{digest(revision), id}
	return presentHome(actor, *value), false, nil
}

func (service *Service) EstimateHome(actor Actor, id string) (HomeEstimate, error) {
	value, err := service.Home(actor, id)
	if err != nil {
		return HomeEstimate{}, err
	}
	return value.Estimate, nil
}

func (service *Service) Inquire(actor Actor, key, id, message string) (Inquiry, bool, error) {
	message = strings.TrimSpace(message)
	if !customer(actor) || !validKey(key) || !safeID(id) || len([]rune(message)) < 4 || len([]rune(message)) > 1000 {
		return Inquiry{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	listing := service.homes[id]
	if listing == nil || listing.Status != "ACTIVE" || listing.OwnerID == actor.Subject {
		return Inquiry{}, false, ErrForbidden
	}
	scope, fingerprint := idemScope(actor, "home-inquiry:"+id, key), digest(message)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Inquiry{}, false, ErrIdempotencyConflict
		}
		return service.inquiries[previous.resourceID], true, nil
	}
	value := Inquiry{ID: service.next("home-inquiry"), ListingID: id, BuyerID: actor.Subject, Message: message, Status: "OPEN", CreatedAt: service.clock().UTC()}
	service.inquiries[value.ID], service.idempotency[scope] = value, idem{fingerprint, value.ID}
	return value, false, nil
}

func (service *Service) ScheduleVisit(actor Actor, key, id string, scheduledAt time.Time) (Visit, bool, error) {
	if !customer(actor) || !validKey(key) || !safeID(id) || !scheduledAt.After(service.clock().UTC().Add(time.Hour)) {
		return Visit{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	listing := service.homes[id]
	if listing == nil || listing.Status != "ACTIVE" || listing.OwnerID == actor.Subject {
		return Visit{}, false, ErrForbidden
	}
	scope, fingerprint := idemScope(actor, "home-visit:"+id, key), digest(scheduledAt)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Visit{}, false, ErrIdempotencyConflict
		}
		return service.visits[previous.resourceID], true, nil
	}
	value := Visit{ID: service.next("home-visit"), ListingID: id, VisitorID: actor.Subject, ScheduledAt: scheduledAt.UTC(), Status: "REQUESTED", CreatedAt: service.clock().UTC()}
	service.visits[value.ID], service.idempotency[scope] = value, idem{fingerprint, value.ID}
	return value, false, nil
}

func (service *Service) UpgradeHome(actor Actor, key, id, plan string) (HomeListing, bool, error) {
	plan = strings.ToUpper(strings.TrimSpace(plan))
	if !customer(actor) || !validKey(key) || !safeID(id) || !map[string]bool{"STANDARD": true, "FEATURED": true, "PREMIUM": true}[plan] {
		return HomeListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.homes[id]
	if value == nil || value.OwnerID != actor.Subject {
		return HomeListing{}, false, ErrNotFound
	}
	scope, fingerprint := idemScope(actor, "home-upgrade:"+id, key), digest(plan)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return HomeListing{}, false, ErrIdempotencyConflict
		}
		return presentHome(actor, *value), true, nil
	}
	value.Plan, value.Revision, value.UpdatedAt = plan, value.Revision+1, service.clock().UTC()
	if plan != "STANDARD" {
		until := value.UpdatedAt.Add(7 * 24 * time.Hour)
		value.FeaturedUntil = &until
	} else {
		value.FeaturedUntil = nil
	}
	service.idempotency[scope] = idem{fingerprint, id}
	return presentHome(actor, *value), false, nil
}

func (service *Service) BrowseClassifieds(actor Actor, query, category, locality string) ([]ClassifiedListing, error) {
	if !customer(actor) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now, values := service.clock().UTC(), []ClassifiedListing{}
	for _, item := range service.classifieds {
		if item.Status == "PUBLISHED" && !now.Before(item.ExpiresAt) {
			item.Status = "EXPIRED"
		}
		if item.tenantID != actor.TenantID || item.country != actor.Country || item.Status != "PUBLISHED" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Description), strings.ToLower(query)) {
			continue
		}
		if category != "" && !strings.EqualFold(item.Category, category) {
			continue
		}
		if locality != "" && !strings.Contains(strings.ToLower(item.Locality), strings.ToLower(locality)) {
			continue
		}
		values = append(values, presentClassified(actor, *item))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].UpdatedAt.After(values[j].UpdatedAt) })
	return values, nil
}

func (service *Service) Classified(actor Actor, id string) (ClassifiedListing, error) {
	if !customer(actor) || !safeID(id) {
		return ClassifiedListing{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.classifieds[id]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country || (value.Status != "PUBLISHED" && value.OwnerID != actor.Subject) {
		return ClassifiedListing{}, ErrNotFound
	}
	return presentClassified(actor, *value), nil
}

func (service *Service) CreateClassified(actor Actor, key string, request ClassifiedRequest) (ClassifiedListing, bool, error) {
	if !customer(actor) || !validKey(key) || !validClassified(request) {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	scope, fingerprint := idemScope(actor, "classified-create", key), digest(request)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return ClassifiedListing{}, false, ErrIdempotencyConflict
		}
		return presentClassified(actor, *service.classifieds[old.resourceID]), true, nil
	}
	now, status := service.clock().UTC(), "PUBLISHED"
	for _, term := range service.config.ReviewTerms {
		if strings.Contains(strings.ToLower(request.Title+" "+request.Description), strings.ToLower(term)) {
			status = "PENDING_REVIEW"
		}
	}
	value := &ClassifiedListing{ID: service.next("classified-listing"), Revision: 1, OwnerID: actor.Subject, Category: strings.ToUpper(request.Category), Title: strings.TrimSpace(request.Title), Description: strings.TrimSpace(request.Description), Price: request.Price, Locality: strings.TrimSpace(request.Locality), MediaAssetIDs: cleanList(request.MediaAssetIDs), Status: status, Plan: "STANDARD", ContactMasked: maskContact(request.Contact), WhatsAppEnabled: request.WhatsAppEnabled, ExpiresAt: now.Add(30 * 24 * time.Hour), CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country, contact: strings.TrimSpace(request.Contact)}
	service.classifieds[value.ID], service.idempotency[scope] = value, idem{fingerprint, value.ID}
	return presentClassified(actor, *value), false, nil
}

func (service *Service) RevealContact(actor Actor, id string, request ContactRequest) (ClassifiedListing, error) {
	channel := strings.ToUpper(strings.TrimSpace(request.Channel))
	if !customer(actor) || !safeID(id) || !request.Consent || !map[string]bool{"PHONE": true, "WHATSAPP": true}[channel] {
		return ClassifiedListing{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.classifieds[id]
	if value == nil || value.Status != "PUBLISHED" || value.OwnerID == actor.Subject || (channel == "WHATSAPP" && !value.WhatsAppEnabled) {
		return ClassifiedListing{}, ErrForbidden
	}
	result := presentClassified(actor, *value)
	result.ContactRevealed = value.contact
	return result, nil
}

func (service *Service) RepostClassified(actor Actor, key, id string) (ClassifiedListing, bool, error) {
	if !customer(actor) || !validKey(key) || !safeID(id) {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.classifieds[id]
	if value == nil || value.OwnerID != actor.Subject {
		return ClassifiedListing{}, false, ErrNotFound
	}
	scope := idemScope(actor, "classified-repost:"+id, key)
	if _, ok := service.idempotency[scope]; ok {
		return presentClassified(actor, *value), true, nil
	}
	if value.Status != "EXPIRED" && service.clock().UTC().Before(value.ExpiresAt) {
		return ClassifiedListing{}, false, ErrConflict
	}
	value.Status, value.ExpiresAt, value.UpdatedAt, value.Revision = "PUBLISHED", service.clock().UTC().Add(30*24*time.Hour), service.clock().UTC(), value.Revision+1
	service.idempotency[scope] = idem{digest(id), id}
	return presentClassified(actor, *value), false, nil
}

func (service *Service) ReportClassified(actor Actor, key, id string, request ReportRequest) (ClassifiedListing, bool, error) {
	if !customer(actor) || !validKey(key) || !safeID(id) || len(strings.TrimSpace(request.Reason)) < 3 || len(request.Details) > 1000 {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.classifieds[id]
	if value == nil || value.OwnerID == actor.Subject || value.Status != "PUBLISHED" {
		return ClassifiedListing{}, false, ErrForbidden
	}
	scope, fingerprint := idemScope(actor, "classified-report:"+id, key), digest(request)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return ClassifiedListing{}, false, ErrIdempotencyConflict
		}
		return presentClassified(actor, *value), true, nil
	}
	value.ReportCount++
	if value.ReportCount >= 3 {
		value.Status = "PENDING_REVIEW"
	}
	service.idempotency[scope] = idem{fingerprint, id}
	return presentClassified(actor, *value), false, nil
}

func (service *Service) UpgradeClassified(actor Actor, key, id, plan string) (ClassifiedListing, bool, error) {
	plan = strings.ToUpper(strings.TrimSpace(plan))
	if !customer(actor) || !validKey(key) || !safeID(id) || !map[string]bool{"STANDARD": true, "FEATURED": true}[plan] {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.classifieds[id]
	if value == nil || value.OwnerID != actor.Subject {
		return ClassifiedListing{}, false, ErrNotFound
	}
	scope, fingerprint := idemScope(actor, "classified-upgrade:"+id, key), digest(plan)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return ClassifiedListing{}, false, ErrIdempotencyConflict
		}
		return presentClassified(actor, *value), true, nil
	}
	value.Plan, value.Revision, value.UpdatedAt = plan, value.Revision+1, service.clock().UTC()
	if plan == "FEATURED" {
		until := value.UpdatedAt.Add(7 * 24 * time.Hour)
		value.FeaturedUntil = &until
	} else {
		value.FeaturedUntil = nil
	}
	service.idempotency[scope] = idem{fingerprint, id}
	return presentClassified(actor, *value), false, nil
}

func (service *Service) ExpireClassifieds(actor Actor) (int, error) {
	if !admin(actor) {
		if !actor.MFAVerified {
			return 0, ErrMFARequired
		}
		return 0, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	count, now := 0, service.clock().UTC()
	for _, item := range service.classifieds {
		if item.Status == "PUBLISHED" && !now.Before(item.ExpiresAt) {
			item.Status, count = "EXPIRED", count+1
		}
	}
	return count, nil
}

func (service *Service) estimate(value HomeListing) HomeEstimate {
	adjustment := int64(100)
	if strings.EqualFold(value.PropertyType, "VILLA") {
		adjustment = 112
	}
	amount := value.Price.AmountMinor * adjustment / 100
	return HomeEstimate{Version: "homes-avm-2026.08", Amount: Money{AmountMinor: amount, Currency: value.Price.Currency}, PricePerArea: Money{AmountMinor: amount / int64(value.AreaSqFt), Currency: value.Price.Currency}, Factors: map[string]string{"locality": "weighted", "property_type": strings.ToLower(value.PropertyType), "confidence": "indicative"}, GeneratedAt: service.clock().UTC()}
}

func presentHome(actor Actor, value HomeListing) HomeListing {
	value.Amenities, value.MediaAssetIDs = append([]string(nil), value.Amenities...), append([]string(nil), value.MediaAssetIDs...)
	value.Estimate.Factors = cloneMap(value.Estimate.Factors)
	value.AllowedActions = []string{"INQUIRE", "SCHEDULE_VISIT", "VIEW_ESTIMATE"}
	if value.OwnerID == actor.Subject {
		value.AllowedActions = []string{"EDIT", "UPGRADE"}
		if value.Status == "DRAFT" && value.KYCVerified {
			value.AllowedActions = append(value.AllowedActions, "PUBLISH")
		}
	}
	return value
}

func presentClassified(actor Actor, value ClassifiedListing) ClassifiedListing {
	value.MediaAssetIDs = append([]string(nil), value.MediaAssetIDs...)
	value.contact = ""
	value.ContactRevealed = ""
	value.AllowedActions = []string{"CONTACT", "REPORT"}
	if value.OwnerID == actor.Subject {
		value.AllowedActions = []string{"EDIT", "UPGRADE"}
		if value.Status == "EXPIRED" {
			value.AllowedActions = append(value.AllowedActions, "REPOST")
		}
	}
	return value
}

func validHome(title, propertyType, purpose, locality string, area, bedrooms int, price Money, latitude, longitude float64) bool {
	return len(strings.TrimSpace(title)) >= 4 && map[string]bool{"APARTMENT": true, "HOUSE": true, "VILLA": true, "LAND": true, "COMMERCIAL": true}[strings.ToUpper(propertyType)] && map[string]bool{"SALE": true, "RENT": true}[strings.ToUpper(purpose)] && len(strings.TrimSpace(locality)) >= 2 && area >= 100 && area <= 1000000 && bedrooms >= 0 && bedrooms <= 50 && price.AmountMinor > 0 && len(price.Currency) == 3 && latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180
}

func validClassified(value ClassifiedRequest) bool {
	return len(strings.TrimSpace(value.Category)) >= 2 && len(strings.TrimSpace(value.Title)) >= 4 && len([]rune(strings.TrimSpace(value.Description))) >= 8 && len([]rune(value.Description)) <= 5000 && value.Price.AmountMinor >= 0 && len(value.Price.Currency) == 3 && len(strings.TrimSpace(value.Locality)) >= 2 && len(value.MediaAssetIDs) <= 10 && len(strings.TrimSpace(value.Contact)) >= 8
}

func customer(actor Actor) bool { return validActor(actor) && hasRole(actor, "CUSTOMER") }
func admin(actor Actor) bool {
	return validActor(actor) && actor.MFAVerified && (hasRole(actor, "CONTENT_ADMIN") || hasRole(actor, "SUPER_ADMIN") || hasRole(actor, "MODERATOR"))
}
func validActor(actor Actor) bool {
	return safeID(actor.TenantID) && countryPattern.MatchString(actor.Country) && safeID(actor.Subject) && len(actor.Roles) > 0
}
func hasRole(actor Actor, role string) bool {
	for _, current := range actor.Roles {
		if strings.EqualFold(strings.TrimSpace(current), role) {
			return true
		}
	}
	return false
}
func safeID(value string) bool   { return safeIDPattern.MatchString(value) }
func validKey(value string) bool { return len(value) >= 8 && len(value) <= 128 && safeID(value) }
func idemScope(actor Actor, op, key string) string {
	return actor.TenantID + "\x00" + actor.Country + "\x00" + actor.Subject + "\x00" + op + "\x00" + key
}
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func (service *Service) next(prefix string) string {
	service.sequence++
	return fmt.Sprintf("%s-%012d", prefix, service.sequence)
}
func cleanList(values []string) []string {
	result, seen := []string{}, map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
func cloneHome(value HomeListing) HomeListing {
	value.Amenities = append([]string(nil), value.Amenities...)
	value.MediaAssetIDs = append([]string(nil), value.MediaAssetIDs...)
	value.Estimate.Factors = cloneMap(value.Estimate.Factors)
	return value
}
func cloneClassified(value ClassifiedListing) ClassifiedListing {
	value.MediaAssetIDs = append([]string(nil), value.MediaAssetIDs...)
	return value
}
func cloneMap(value map[string]string) map[string]string {
	result := map[string]string{}
	for key, item := range value {
		result[key] = item
	}
	return result
}
func maskContact(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(value)-4) + value[len(value)-4:]
}
