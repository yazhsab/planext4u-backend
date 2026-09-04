package support

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	uuidPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	safeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	keyPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
)

type cursor struct {
	CreatedAt time.Time
	ID        string
	Present   bool
}

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository, clock func() time.Time) (*Service, error) {
	if repository == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{repository: repository, clock: clock}, nil
}

func (service *Service) Create(ctx context.Context, actor Actor, request CreateTicketRequest, idempotencyKey string) (Ticket, bool, error) {
	request.Subject, request.Description, request.RelatedReference, idempotencyKey = strings.TrimSpace(request.Subject), strings.TrimSpace(request.Description), strings.TrimSpace(request.RelatedReference), strings.TrimSpace(idempotencyKey)
	if request.Priority == "" {
		request.Priority = PriorityNormal
	}
	if !validActor(actor) || !actorHasRole(actor, request.OwnerRole) || !validCategoryForRole(request.Category, request.OwnerRole) || !validText(request.Subject, 4, 160) || !validText(request.Description, 2, 8_000) || !validOptionalReference(request.RelatedReference) || !validPriority(request.Priority) || !keyPattern.MatchString(idempotencyKey) {
		return Ticket{}, false, ErrInvalidRequest
	}
	now := service.clock().UTC()
	ticket := Ticket{ID: uuid.NewString(), TenantID: actor.TenantID, Country: actor.Country, OwnerSubject: actor.Subject, OwnerRole: request.OwnerRole, Category: request.Category, Subject: request.Subject, RelatedReference: request.RelatedReference, Priority: request.Priority, Status: StatusOpen, CreatedAt: now, UpdatedAt: now}
	message := Message{ID: uuid.NewString(), AuthorType: AuthorRequester, Body: request.Description, CreatedAt: now}
	digest := requestDigest(string(request.OwnerRole), string(request.Category), request.Subject, request.Description, request.RelatedReference, string(request.Priority))
	return service.repository.Create(ctx, ticket, message, idempotencyKey, digest)
}

func (service *Service) List(ctx context.Context, actor Actor, role Role, limit int, encodedCursor string) (TicketPage, error) {
	if limit == 0 {
		limit = 20
	}
	after, err := decodeCursor(encodedCursor)
	if !validActor(actor) || !actorHasRole(actor, role) {
		return TicketPage{}, ErrForbidden
	}
	if err != nil || limit < 1 || limit > 50 {
		return TicketPage{}, ErrInvalidRequest
	}
	return service.repository.List(ctx, actor, role, limit, after)
}

func (service *Service) Get(ctx context.Context, actor Actor, ticketID string) (Ticket, error) {
	if !validActor(actor) {
		return Ticket{}, ErrForbidden
	}
	if !uuidPattern.MatchString(ticketID) {
		return Ticket{}, ErrInvalidRequest
	}
	return service.repository.Get(ctx, actor, ticketID)
}

func (service *Service) AddMessage(ctx context.Context, actor Actor, ticketID string, request AddMessageRequest, idempotencyKey string) (Ticket, bool, error) {
	request.Body, idempotencyKey = strings.TrimSpace(request.Body), strings.TrimSpace(idempotencyKey)
	if !validActor(actor) {
		return Ticket{}, false, ErrForbidden
	}
	if !uuidPattern.MatchString(ticketID) || !validText(request.Body, 1, 8_000) || !keyPattern.MatchString(idempotencyKey) {
		return Ticket{}, false, ErrInvalidRequest
	}
	message := Message{ID: uuid.NewString(), AuthorType: AuthorRequester, Body: request.Body, CreatedAt: service.clock().UTC()}
	return service.repository.AddMessage(ctx, actor, ticketID, message, idempotencyKey, requestDigest(request.Body))
}

func (service *Service) AdminList(ctx context.Context, principal AdminPrincipal, filter AdminListFilter) (AdminTicketPage, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	after, err := decodeCursor(filter.Cursor)
	if !validAdminPrincipal(principal) || !principal.Capabilities[CapabilitySupportManage] {
		return AdminTicketPage{}, ErrForbidden
	}
	if err != nil || filter.Limit < 1 || filter.Limit > 100 || (filter.OwnerRole != "" && !validRole(filter.OwnerRole)) || (filter.Status != "" && !validStatus(filter.Status)) {
		return AdminTicketPage{}, ErrInvalidRequest
	}
	return service.repository.AdminList(ctx, principal, filter, after)
}

func validActor(actor Actor) bool {
	return safeIDPattern.MatchString(actor.TenantID) && countryPattern.MatchString(actor.Country) && safeIDPattern.MatchString(actor.Subject) && len(actor.Roles) > 0
}

func validAdminPrincipal(principal AdminPrincipal) bool {
	return safeIDPattern.MatchString(principal.TenantID) && countryPattern.MatchString(principal.Country) && safeIDPattern.MatchString(principal.Subject)
}

func actorHasRole(actor Actor, role Role) bool {
	if !validRole(role) {
		return false
	}
	for _, candidate := range actor.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func validRole(value Role) bool {
	return value == RoleCustomer || value == RoleVendor || value == RoleRider
}
func validPriority(value Priority) bool {
	return value == PriorityLow || value == PriorityNormal || value == PriorityHigh || value == PriorityUrgent
}
func validStatus(value Status) bool {
	return value == StatusOpen || value == StatusWaitingForSupport || value == StatusWaitingForRequester || value == StatusResolved || value == StatusClosed
}

func validCategoryForRole(category Category, role Role) bool {
	if category == CategoryAccount || category == CategoryPayment || category == CategoryOther {
		return true
	}
	switch category {
	case CategoryOrder:
		return role == RoleCustomer
	case CategoryVendorOperations:
		return role == RoleVendor
	case CategoryRiderOperations:
		return role == RoleRider
	default:
		return false
	}
}

func validText(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
}

func validOptionalReference(value string) bool {
	return value == "" || safeIDPattern.MatchString(value)
}

func requestDigest(values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:])
}

func encodeCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAt.UTC().UnixNano(), 10) + "|" + id))
}

func decodeCursor(value string) (cursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return cursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursor{}, ErrInvalidRequest
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 || !uuidPattern.MatchString(parts[1]) {
		return cursor{}, ErrInvalidRequest
	}
	nanoseconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return cursor{}, ErrInvalidRequest
	}
	return cursor{CreatedAt: time.Unix(0, nanoseconds).UTC(), ID: parts[1], Present: true}, nil
}

func beforeCursor(createdAt time.Time, id string, after cursor) bool {
	return !after.Present || createdAt.Before(after.CreatedAt) || (createdAt.Equal(after.CreatedAt) && id < after.ID)
}

func isServiceError(err error) bool {
	return errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrIdempotencyConflict)
}
