package wallet

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid wallet request")
	ErrInsufficientBalance = errors.New("insufficient wallet balance")
	ErrEntryNotFound       = errors.New("wallet entry not found")
	ErrAlreadyReversed     = errors.New("wallet entry already reversed")
	ErrIdempotencyConflict = errors.New("wallet idempotency conflict")
	ErrRewardNotEligible   = errors.New("wallet reward not eligible")
)

type Scope struct {
	TenantID   string
	Country    string
	CustomerID string
}

type EntryType string

const (
	EntryCredit   EntryType = "CREDIT"
	EntryDebit    EntryType = "DEBIT"
	EntryExpiry   EntryType = "EXPIRY"
	EntryReversal EntryType = "REVERSAL"
)

type LedgerEntry struct {
	ID               string     `json:"id"`
	Type             EntryType  `json:"type"`
	Category         string     `json:"category"`
	SourceReference  string     `json:"source_reference"`
	ReversesEntryID  string     `json:"reverses_entry_id,omitempty"`
	DeltaPoints      int64      `json:"delta_points"`
	BalanceAfter     int64      `json:"balance_after"`
	OriginalExpiryAt *time.Time `json:"original_expiry_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

type Account struct {
	Balance int64         `json:"balance"`
	Entries []LedgerEntry `json:"entries"`
}

type RewardPolicy struct {
	DailyDeviceCap int64
	Cooldown       time.Duration
}
