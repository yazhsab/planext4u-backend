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
	DailyDeviceCap          int64
	Cooldown                time.Duration
	ReferralSenderPoints    int64
	ReferralRecipientPoints int64
	ReferralExpiry          time.Duration
}

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type RefillOffer struct {
	ID             string        `json:"id"`
	Country        string        `json:"country"`
	Points         int64         `json:"points"`
	BonusPoints    int64         `json:"bonus_points"`
	Price          Money         `json:"price"`
	PaymentMethods []string      `json:"payment_methods"`
	ExpiresAfter   time.Duration `json:"-"`
}

type RewardCampaign struct {
	ID          string    `json:"id"`
	Country     string    `json:"country"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Points      int64     `json:"points"`
	EndsAt      time.Time `json:"ends_at"`
}

type Program struct {
	ReferralBaseURL string
	RefillOffers    []RefillOffer
	Campaigns       []RewardCampaign
}

type ReferralProfile struct {
	Code            string `json:"code"`
	ShareURL        string `json:"share_url"`
	SenderPoints    int64  `json:"sender_points"`
	RecipientPoints int64  `json:"recipient_points"`
	PendingCode     string `json:"pending_code,omitempty"`
	Rewarded        bool   `json:"rewarded"`
}

type Experience struct {
	Account   Account          `json:"account"`
	Referral  ReferralProfile  `json:"referral"`
	Refills   []RefillOffer    `json:"refills"`
	Campaigns []RewardCampaign `json:"campaigns"`
}

// WalletService is the checkout-facing points ledger boundary.
type WalletService interface {
	Experience(Scope) (Experience, error)
	RefillOffer(Scope, string) (RefillOffer, error)
	ApplyReferral(Scope, string, string) (ReferralProfile, bool, error)
	ActivateReferral(Scope, string) ([]LedgerEntry, error)
	Account(Scope) (Account, error)
	Credit(Scope, string, string, string, int64, time.Time) (LedgerEntry, bool, error)
	Redeem(Scope, string, string, int64) (LedgerEntry, bool, error)
	ReverseDebit(Scope, string, string, string) (LedgerEntry, bool, error)
	RefundDebit(Scope, string, string, string, int64) (LedgerEntry, bool, error)
}
