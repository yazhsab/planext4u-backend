package messaging

import "time"

type OutboxState string

const (
	OutboxPending   OutboxState = "PENDING"
	OutboxLeased    OutboxState = "LEASED"
	OutboxPublished OutboxState = "PUBLISHED"
	OutboxDead      OutboxState = "DEAD_LETTER"
)

type Message struct {
	ID               string         `json:"event_id"`
	TenantID         string         `json:"tenant_id"`
	Country          string         `json:"country"`
	Producer         string         `json:"producer"`
	EventType        string         `json:"event_type"`
	SchemaVersion    int            `json:"schema_version"`
	AggregateType    string         `json:"aggregate_type"`
	AggregateID      string         `json:"aggregate_id"`
	AggregateVersion int64          `json:"aggregate_version"`
	CorrelationID    string         `json:"correlation_id"`
	CausationID      string         `json:"causation_id"`
	Traceparent      string         `json:"traceparent"`
	Classification   string         `json:"classification"`
	OccurredAt       time.Time      `json:"occurred_at"`
	Data             map[string]any `json:"data"`
}

type OutboxRecord struct {
	Message       Message     `json:"message"`
	State         OutboxState `json:"state"`
	Attempts      int         `json:"attempts"`
	NextAttemptAt time.Time   `json:"next_attempt_at"`
	LeaseOwner    string      `json:"-"`
	LeaseUntil    time.Time   `json:"-"`
	LastErrorCode string      `json:"last_error_code,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	PublishedAt   *time.Time  `json:"published_at,omitempty"`
	DeadAt        *time.Time  `json:"dead_at,omitempty"`
}

type ReplayPrincipal struct {
	TenantID        string
	Country         string
	SubjectID       string
	Capabilities    map[string]bool
	AuthenticatedAt time.Time
}

type ReplayAudit struct {
	TenantID      string
	Country       string
	ActorID       string
	MessageID     string
	Reason        string
	CorrelationID string
	OccurredAt    time.Time
}

type ConsumeOutcome string

const (
	ConsumeProcessed  ConsumeOutcome = "PROCESSED"
	ConsumeDuplicate  ConsumeOutcome = "DUPLICATE"
	ConsumeOutOfOrder ConsumeOutcome = "OUT_OF_ORDER"
)
