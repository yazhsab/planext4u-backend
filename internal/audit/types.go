package audit

import "time"

type Outcome string

const (
	OutcomeSucceeded Outcome = "SUCCEEDED"
	OutcomeDenied    Outcome = "DENIED"
	OutcomeFailed    Outcome = "FAILED"
)

type Actor struct {
	SubjectID string `json:"subject_id"`
	ActorType string `json:"actor_type"`
	IPPrefix  string `json:"ip_prefix,omitempty"`
}

type Target struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Entry struct {
	ID            string         `json:"id"`
	TenantID      string         `json:"tenant_id"`
	Country       string         `json:"country"`
	Actor         Actor          `json:"actor"`
	Action        string         `json:"action"`
	Target        Target         `json:"target"`
	Outcome       Outcome        `json:"outcome"`
	ReasonCode    string         `json:"reason_code"`
	CorrelationID string         `json:"correlation_id"`
	OccurredAt    time.Time      `json:"occurred_at"`
	RecordedAt    time.Time      `json:"recorded_at"`
	Before        map[string]any `json:"before,omitempty"`
	After         map[string]any `json:"after,omitempty"`
	PreviousHash  string         `json:"previous_hash,omitempty"`
	Hash          string         `json:"hash"`
	Sequence      int64          `json:"sequence"`
}

type RecordRequest struct {
	Country       string         `json:"country"`
	Actor         Actor          `json:"actor"`
	Action        string         `json:"action"`
	Target        Target         `json:"target"`
	Outcome       Outcome        `json:"outcome"`
	ReasonCode    string         `json:"reason_code"`
	CorrelationID string         `json:"correlation_id"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Before        map[string]any `json:"before,omitempty"`
	After         map[string]any `json:"after,omitempty"`
}

type SearchFilter struct {
	TenantID string
	Country  string
	ActorID  string
	Action   string
	TargetID string
	From     time.Time
	To       time.Time
	After    int64
	Limit    int
}

type Page struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
}

type Export struct {
	GeneratedAt time.Time `json:"generated_at"`
	Reason      string    `json:"reason"`
	Entries     []Entry   `json:"entries"`
	ChainValid  bool      `json:"chain_valid"`
}

type Principal struct {
	TenantID        string
	SubjectID       string
	Capabilities    map[string]bool
	AuthenticatedAt time.Time
}
