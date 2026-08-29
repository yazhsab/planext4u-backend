package emergency

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

type idempotent struct{ fingerprint, resourceID string }

type Service struct {
	clock       func() time.Time
	config      Configuration
	mu          sync.Mutex
	sequence    int64
	requests    map[string]*Request
	messages    map[string][]Message
	idempotency map[string]idempotent
}

var safePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$`)

func NewService(config Configuration, clock func() time.Time) (*Service, error) {
	if clock == nil || !safeID(config.TenantID) || !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(config.Country) {
		return nil, ErrInvalidRequest
	}
	if config.AssignmentSLA <= 0 {
		config.AssignmentSLA = 5 * time.Minute
	}
	if config.LocationMaxAge <= 0 {
		config.LocationMaxAge = 2 * time.Minute
	}
	return &Service{clock: clock, config: config, requests: map[string]*Request{}, messages: map[string][]Message{}, idempotency: map[string]idempotent{}}, nil
}

func (service *Service) Create(actor Actor, key string, input CreateRequest) (Request, bool, error) {
	category, priority, description := strings.ToUpper(strings.TrimSpace(input.Category)), strings.ToUpper(strings.TrimSpace(input.Priority)), strings.TrimSpace(input.Description)
	if !customer(actor) || !validKey(key) || !input.LocationConsent || !map[string]bool{"MEDICAL": true, "SAFETY": true, "FIRE": true, "ACCIDENT": true, "OTHER": true}[category] || !map[string]bool{"HIGH": true, "CRITICAL": true}[priority] || len(description) < 5 || len(description) > 2000 || !validLocation(input.Location) {
		return Request{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	scope, fingerprint := idemScope(actor, "create", key), digest(input)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return Request{}, false, ErrIdempotencyConflict
		}
		return service.present(actor, *service.requests[old.resourceID]), true, nil
	}
	now := service.clock().UTC()
	location := input.Location
	location.CapturedAt = now
	value := &Request{ID: service.next(), Revision: 1, RequesterID: actor.Subject, Category: category, Description: description, Priority: priority, Status: "OPEN", LocationConsent: true, EscalationLevel: 0, SLADeadline: now.Add(service.config.AssignmentSLA), CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country, lastLocation: &location}
	service.requests[value.ID], service.idempotency[scope] = value, idempotent{fingerprint, value.ID}
	return service.present(actor, *value), false, nil
}

func (service *Service) Get(actor Actor, id string) (Request, error) {
	if !validActor(actor) || !safeID(id) {
		return Request{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.requests[id]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country || !service.canView(actor, value) {
		return Request{}, ErrNotFound
	}
	return service.present(actor, *value), nil
}

func (service *Service) List(actor Actor) ([]Request, error) {
	if !validActor(actor) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []Request{}
	for _, item := range service.requests {
		if item.tenantID == actor.TenantID && item.country == actor.Country && service.canView(actor, item) {
			values = append(values, service.present(actor, *item))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) Accept(actor Actor, key, id string) (Request, bool, error) {
	if !responder(actor) || !validKey(key) || !safeID(id) {
		if !actor.MFAVerified {
			return Request{}, false, ErrMFARequired
		}
		return Request{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.requests[id]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return Request{}, false, ErrNotFound
	}
	scope := idemScope(actor, "accept:"+id, key)
	if _, ok := service.idempotency[scope]; ok && value.AssignedResponder == actor.Subject {
		return service.present(actor, *value), true, nil
	}
	if value.Status != "OPEN" || value.AssignedResponder != "" {
		return Request{}, false, ErrConflict
	}
	now := service.clock().UTC()
	value.AssignedResponder, value.Status, value.AcceptedAt, value.UpdatedAt, value.Revision = actor.Subject, "ASSIGNED", &now, now, value.Revision+1
	service.idempotency[scope] = idempotent{digest(id), id}
	return service.present(actor, *value), false, nil
}

func (service *Service) UpdateLocation(actor Actor, id string, input LocationRequest) (Request, error) {
	if !validActor(actor) || !safeID(id) {
		return Request{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.requests[id]
	if value == nil || (actor.Subject != value.RequesterID && actor.Subject != value.AssignedResponder) {
		return Request{}, ErrForbidden
	}
	value.LocationConsent = input.Consent
	value.UpdatedAt = service.clock().UTC()
	value.Revision++
	if !input.Consent {
		value.lastLocation = nil
		return service.present(actor, *value), nil
	}
	if !validLocation(input.Location) {
		return Request{}, ErrInvalidRequest
	}
	location := input.Location
	location.CapturedAt = service.clock().UTC()
	value.lastLocation = &location
	return service.present(actor, *value), nil
}

func (service *Service) Transition(actor Actor, key, id string, revision int64, input TransitionRequest) (Request, bool, error) {
	status, note := strings.ToUpper(strings.TrimSpace(input.Status)), strings.TrimSpace(input.Note)
	if !responder(actor) || !validKey(key) || !safeID(id) || revision < 1 || !map[string]bool{"EN_ROUTE": true, "ON_SCENE": true, "RESOLVED": true}[status] || len(note) < 4 {
		if !actor.MFAVerified {
			return Request{}, false, ErrMFARequired
		}
		return Request{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.requests[id]
	if value == nil || value.AssignedResponder != actor.Subject {
		return Request{}, false, ErrForbidden
	}
	scope, fingerprint := idemScope(actor, "transition:"+id, key), digest(input)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return Request{}, false, ErrIdempotencyConflict
		}
		return service.present(actor, *value), true, nil
	}
	if value.Revision != revision || !validTransition(value.Status, status) {
		return Request{}, false, ErrConflict
	}
	now := service.clock().UTC()
	value.Status, value.UpdatedAt, value.Revision = status, now, value.Revision+1
	if status == "RESOLVED" {
		value.ResolvedAt = &now
		value.lastLocation = nil
	}
	service.idempotency[scope] = idempotent{fingerprint, id}
	return service.present(actor, *value), false, nil
}

func (service *Service) Messages(actor Actor, id string) ([]Message, error) {
	if !validActor(actor) || !safeID(id) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.requests[id]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country || !service.canView(actor, value) {
		return nil, ErrNotFound
	}
	return append([]Message(nil), service.messages[id]...), nil
}

func (service *Service) SendMessage(actor Actor, key, id string, input MessageRequest) (Message, bool, error) {
	body := strings.TrimSpace(input.Body)
	if !validActor(actor) || !validKey(key) || !safeID(id) || len([]rune(body)) < 1 || len([]rune(body)) > 2000 {
		return Message{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	request := service.requests[id]
	if request == nil || request.tenantID != actor.TenantID || request.country != actor.Country || request.Status == "RESOLVED" || (actor.Subject != request.RequesterID && actor.Subject != request.AssignedResponder) {
		return Message{}, false, ErrForbidden
	}
	scope, fingerprint := idemScope(actor, "message:"+id, key), digest(input)
	if old, ok := service.idempotency[scope]; ok {
		if old.fingerprint != fingerprint {
			return Message{}, false, ErrIdempotencyConflict
		}
		for _, message := range service.messages[id] {
			if message.ID == old.resourceID {
				return message, true, nil
			}
		}
	}
	value := Message{ID: "emergency-message-" + fmt.Sprintf("%012d", service.nextSequence()), RequestID: id, SenderID: actor.Subject, Body: body, Status: "DELIVERED", CreatedAt: service.clock().UTC()}
	service.messages[id] = append(service.messages[id], value)
	service.idempotency[scope] = idempotent{fingerprint: fingerprint, resourceID: value.ID}
	return value, false, nil
}

func (service *Service) RunEscalations(actor Actor) (int, error) {
	if !administrator(actor) {
		if !actor.MFAVerified {
			return 0, ErrMFARequired
		}
		return 0, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now, count := service.clock().UTC(), 0
	for _, value := range service.requests {
		if value.Status == "OPEN" && !now.Before(value.SLADeadline) {
			value.EscalationLevel++
			value.SLADeadline = now.Add(service.config.AssignmentSLA)
			value.UpdatedAt = now
			value.Revision++
			count++
		}
	}
	return count, nil
}

func (service *Service) SLA(actor Actor) (SLAReport, error) {
	if !administrator(actor) {
		if !actor.MFAVerified {
			return SLAReport{}, ErrMFARequired
		}
		return SLAReport{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	report := SLAReport{GeneratedAt: service.clock().UTC(), ByCategory: map[string]int{}, LocationPrecision: "aggregate_only"}
	var total int64
	for _, value := range service.requests {
		if value.tenantID != actor.TenantID || value.country != actor.Country {
			continue
		}
		report.ByCategory[value.Category]++
		switch value.Status {
		case "OPEN":
			report.Open++
		case "RESOLVED":
			report.Resolved++
		default:
			report.Assigned++
		}
		if value.EscalationLevel > 0 {
			report.Breached++
		}
		if value.AcceptedAt != nil {
			total += int64(value.AcceptedAt.Sub(value.CreatedAt).Seconds())
		}
	}
	accepted := report.Assigned + report.Resolved
	if accepted > 0 {
		report.AverageAcceptSecs = total / int64(accepted)
	}
	return report, nil
}

func (service *Service) present(actor Actor, value Request) Request {
	value.AllowedActions = []string{"VIEW"}
	value.CurrentLocation = nil
	if value.LocationConsent && value.lastLocation != nil && service.clock().UTC().Sub(value.lastLocation.CapturedAt) <= service.config.LocationMaxAge && service.canSeeLocation(actor, &value) {
		location := *value.lastLocation
		value.CurrentLocation = &location
	}
	value.lastLocation = nil
	if actor.Subject == value.RequesterID && value.Status != "RESOLVED" {
		value.AllowedActions = []string{"UPDATE_LOCATION", "REVOKE_LOCATION"}
	}
	if actor.Subject == value.AssignedResponder {
		value.AllowedActions = []string{"UPDATE_LOCATION", "EN_ROUTE", "ON_SCENE", "RESOLVE"}
	}
	if responder(actor) && value.Status == "OPEN" {
		value.AllowedActions = []string{"ACCEPT"}
	}
	return value
}
func (service *Service) canView(actor Actor, value *Request) bool {
	return actor.Subject == value.RequesterID || actor.Subject == value.AssignedResponder || responder(actor) || administrator(actor)
}
func (service *Service) canSeeLocation(actor Actor, value *Request) bool {
	return actor.Subject == value.RequesterID || actor.Subject == value.AssignedResponder || administrator(actor)
}
func validTransition(from, to string) bool {
	return map[string]map[string]bool{"ASSIGNED": {"EN_ROUTE": true}, "EN_ROUTE": {"ON_SCENE": true}, "ON_SCENE": {"RESOLVED": true}}[from][to]
}
func validLocation(value Location) bool {
	return value.Latitude >= -90 && value.Latitude <= 90 && value.Longitude >= -180 && value.Longitude <= 180 && value.AccuracyM > 0 && value.AccuracyM <= 1000
}
func validActor(actor Actor) bool {
	return safeID(actor.TenantID) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(actor.Country) && safeID(actor.Subject) && len(actor.Roles) > 0
}
func customer(actor Actor) bool { return validActor(actor) && hasRole(actor, "CUSTOMER") }
func responder(actor Actor) bool {
	return validActor(actor) && actor.MFAVerified && (hasRole(actor, "EMERGENCY_RESPONDER") || hasRole(actor, "SUPER_ADMIN"))
}
func administrator(actor Actor) bool {
	return validActor(actor) && actor.MFAVerified && (hasRole(actor, "EMERGENCY_ADMIN") || hasRole(actor, "SUPER_ADMIN"))
}
func hasRole(actor Actor, expected string) bool {
	for _, role := range actor.Roles {
		if strings.EqualFold(strings.TrimSpace(role), expected) {
			return true
		}
	}
	return false
}
func safeID(value string) bool   { return safePattern.MatchString(value) }
func validKey(value string) bool { return len(value) >= 8 && len(value) <= 128 && safeID(value) }
func idemScope(actor Actor, op, key string) string {
	return actor.TenantID + "\x00" + actor.Country + "\x00" + actor.Subject + "\x00" + op + "\x00" + key
}
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func (service *Service) next() string {
	return fmt.Sprintf("emergency-request-%012d", service.nextSequence())
}
func (service *Service) nextSequence() int64 {
	service.sequence++
	return service.sequence
}
