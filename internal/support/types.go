package support

import "time"

type Role string

const (
	RoleCustomer Role = "CUSTOMER"
	RoleVendor   Role = "VENDOR"
	RoleRider    Role = "RIDER"
)

type Category string

const (
	CategoryAccount          Category = "ACCOUNT"
	CategoryOrder            Category = "ORDER"
	CategoryPayment          Category = "PAYMENT"
	CategoryVendorOperations Category = "VENDOR_OPERATIONS"
	CategoryRiderOperations  Category = "RIDER_OPERATIONS"
	CategoryOther            Category = "OTHER"
)

type Priority string

const (
	PriorityLow    Priority = "LOW"
	PriorityNormal Priority = "NORMAL"
	PriorityHigh   Priority = "HIGH"
	PriorityUrgent Priority = "URGENT"
)

type Status string

const (
	StatusOpen                Status = "OPEN"
	StatusWaitingForSupport   Status = "WAITING_FOR_SUPPORT"
	StatusWaitingForRequester Status = "WAITING_FOR_REQUESTER"
	StatusResolved            Status = "RESOLVED"
	StatusClosed              Status = "CLOSED"
)

type AuthorType string

const (
	AuthorRequester AuthorType = "REQUESTER"
	AuthorSupport   AuthorType = "SUPPORT"
)

type Actor struct {
	TenantID string
	Country  string
	Subject  string
	Roles    []Role
}

type AdminPrincipal struct {
	TenantID     string
	Country      string
	Subject      string
	Capabilities map[string]bool
}

type Message struct {
	ID         string     `json:"id"`
	AuthorType AuthorType `json:"author_type"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Ticket struct {
	ID               string    `json:"id"`
	OwnerRole        Role      `json:"owner_role"`
	Category         Category  `json:"category"`
	Subject          string    `json:"subject"`
	RelatedReference string    `json:"related_reference,omitempty"`
	Priority         Priority  `json:"priority"`
	Status           Status    `json:"status"`
	Messages         []Message `json:"messages"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	TenantID      string `json:"-"`
	Country       string `json:"-"`
	OwnerSubject  string `json:"-"`
	RequestDigest string `json:"-"`
}

type TicketPage struct {
	Items      []Ticket `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type AdminTicket struct {
	ID               string    `json:"id"`
	OwnerRole        Role      `json:"owner_role"`
	OwnerReference   string    `json:"owner_reference"`
	Category         Category  `json:"category"`
	Subject          string    `json:"subject"`
	RelatedReference string    `json:"related_reference,omitempty"`
	Priority         Priority  `json:"priority"`
	Status           Status    `json:"status"`
	MessageCount     int       `json:"message_count"`
	LastMessageAt    time.Time `json:"last_message_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AdminTicketPage struct {
	Items      []AdminTicket `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type CreateTicketRequest struct {
	OwnerRole        Role     `json:"owner_role"`
	Category         Category `json:"category"`
	Subject          string   `json:"subject"`
	Description      string   `json:"description"`
	RelatedReference string   `json:"related_reference,omitempty"`
	Priority         Priority `json:"priority,omitempty"`
}

type AddMessageRequest struct {
	Body string `json:"body"`
}

type AdminListFilter struct {
	OwnerRole Role
	Status    Status
	Limit     int
	Cursor    string
}
