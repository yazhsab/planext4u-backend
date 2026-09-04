package support

import (
	"context"
	"errors"
	"sort"
	"sync"
)

var (
	ErrInvalidRequest      = errors.New("invalid support request")
	ErrForbidden           = errors.New("support access forbidden")
	ErrNotFound            = errors.New("support ticket not found")
	ErrIdempotencyConflict = errors.New("support idempotency key conflict")
)

const CapabilitySupportManage = "admin.support.manage"

type Repository interface {
	Create(context.Context, Ticket, Message, string, string) (Ticket, bool, error)
	List(context.Context, Actor, Role, int, cursor) (TicketPage, error)
	Get(context.Context, Actor, string) (Ticket, error)
	AddMessage(context.Context, Actor, string, Message, string, string) (Ticket, bool, error)
	AdminList(context.Context, AdminPrincipal, AdminListFilter, cursor) (AdminTicketPage, error)
}

type idempotencyRecord struct {
	TicketID  string
	Digest    string
	MessageID string
}

type MemoryRepository struct {
	mu              sync.Mutex
	tickets         map[string]Ticket
	createRequests  map[string]idempotencyRecord
	messageRequests map[string]idempotencyRecord
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		tickets: map[string]Ticket{}, createRequests: map[string]idempotencyRecord{}, messageRequests: map[string]idempotencyRecord{},
	}
}

func (repository *MemoryRepository) Create(_ context.Context, ticket Ticket, initial Message, key, digest string) (Ticket, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	idempotencyKey := ticket.TenantID + "\x00" + ticket.Country + "\x00" + ticket.OwnerSubject + "\x00" + string(ticket.OwnerRole) + "\x00" + key
	if record, exists := repository.createRequests[idempotencyKey]; exists {
		if record.Digest != digest {
			return Ticket{}, false, ErrIdempotencyConflict
		}
		return cloneTicket(repository.tickets[record.TicketID]), false, nil
	}
	ticket.Messages = []Message{initial}
	repository.tickets[ticket.ID] = cloneTicket(ticket)
	repository.createRequests[idempotencyKey] = idempotencyRecord{TicketID: ticket.ID, Digest: digest, MessageID: initial.ID}
	return cloneTicket(ticket), true, nil
}

func (repository *MemoryRepository) List(_ context.Context, actor Actor, role Role, limit int, after cursor) (TicketPage, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	values := make([]Ticket, 0)
	for _, ticket := range repository.tickets {
		if ticket.TenantID == actor.TenantID && ticket.Country == actor.Country && ticket.OwnerSubject == actor.Subject && ticket.OwnerRole == role && beforeCursor(ticket.CreatedAt, ticket.ID, after) {
			values = append(values, cloneTicket(ticket))
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].ID > values[right].ID
		}
		return values[left].CreatedAt.After(values[right].CreatedAt)
	})
	return ticketPage(values, limit), nil
}

func (repository *MemoryRepository) Get(_ context.Context, actor Actor, id string) (Ticket, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	ticket, exists := repository.tickets[id]
	if !exists || !actorOwns(actor, ticket) {
		return Ticket{}, ErrNotFound
	}
	return cloneTicket(ticket), nil
}

func (repository *MemoryRepository) AddMessage(_ context.Context, actor Actor, ticketID string, message Message, key, digest string) (Ticket, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	ticket, exists := repository.tickets[ticketID]
	if !exists || !actorOwns(actor, ticket) {
		return Ticket{}, false, ErrNotFound
	}
	idempotencyKey := ticketID + "\x00" + key
	if record, replay := repository.messageRequests[idempotencyKey]; replay {
		if record.Digest != digest {
			return Ticket{}, false, ErrIdempotencyConflict
		}
		return cloneTicket(ticket), false, nil
	}
	ticket.Messages = append(ticket.Messages, message)
	ticket.UpdatedAt = message.CreatedAt
	if ticket.Status == StatusWaitingForRequester {
		ticket.Status = StatusWaitingForSupport
	}
	repository.tickets[ticketID] = cloneTicket(ticket)
	repository.messageRequests[idempotencyKey] = idempotencyRecord{TicketID: ticketID, Digest: digest, MessageID: message.ID}
	return cloneTicket(ticket), true, nil
}

func (repository *MemoryRepository) AdminList(_ context.Context, principal AdminPrincipal, filter AdminListFilter, after cursor) (AdminTicketPage, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	values := make([]AdminTicket, 0)
	for _, ticket := range repository.tickets {
		if ticket.TenantID != principal.TenantID || ticket.Country != principal.Country || (filter.OwnerRole != "" && ticket.OwnerRole != filter.OwnerRole) || (filter.Status != "" && ticket.Status != filter.Status) || !beforeCursor(ticket.CreatedAt, ticket.ID, after) {
			continue
		}
		lastMessageAt := ticket.CreatedAt
		if len(ticket.Messages) > 0 {
			lastMessageAt = ticket.Messages[len(ticket.Messages)-1].CreatedAt
		}
		values = append(values, AdminTicket{ID: ticket.ID, OwnerRole: ticket.OwnerRole, OwnerReference: ticket.OwnerSubject, Category: ticket.Category, Subject: ticket.Subject, RelatedReference: ticket.RelatedReference, Priority: ticket.Priority, Status: ticket.Status, MessageCount: len(ticket.Messages), LastMessageAt: lastMessageAt, CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt})
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].ID > values[right].ID
		}
		return values[left].CreatedAt.After(values[right].CreatedAt)
	})
	return adminTicketPage(values, filter.Limit), nil
}

func actorOwns(actor Actor, ticket Ticket) bool {
	if ticket.TenantID != actor.TenantID || ticket.Country != actor.Country || ticket.OwnerSubject != actor.Subject {
		return false
	}
	return actorHasRole(actor, ticket.OwnerRole)
}

func cloneTicket(value Ticket) Ticket {
	value.Messages = append([]Message(nil), value.Messages...)
	return value
}

func ticketPage(values []Ticket, limit int) TicketPage {
	page := TicketPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page
}

func adminTicketPage(values []AdminTicket, limit int) AdminTicketPage {
	page := AdminTicketPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page
}
