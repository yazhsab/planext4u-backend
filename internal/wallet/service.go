package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type lot struct {
	id        string
	remaining int64
	expiresAt time.Time
}

type allocation struct {
	lotID     string
	points    int64
	expiresAt time.Time
}

type accountState struct {
	balance     int64
	entries     []LedgerEntry
	lots        []lot
	allocations map[string][]allocation
	reversed    map[string]bool
}

type replay struct {
	fingerprint string
	entry       LedgerEntry
}

type Service struct {
	clock        func() time.Time
	rewardPolicy RewardPolicy
	mu           sync.Mutex
	accounts     map[string]*accountState
	requests     map[string]replay
	referrals    map[string]bool
	rewardUsage  map[string]int64
	rewardLast   map[string]time.Time
}

func NewService(clock func() time.Time, rewardPolicy RewardPolicy) (*Service, error) {
	if clock == nil || rewardPolicy.DailyDeviceCap < 0 || rewardPolicy.Cooldown < 0 {
		return nil, ErrInvalidRequest
	}
	return &Service{
		clock: clock, rewardPolicy: rewardPolicy, accounts: map[string]*accountState{}, requests: map[string]replay{},
		referrals: map[string]bool{}, rewardUsage: map[string]int64{}, rewardLast: map[string]time.Time{},
	}, nil
}

func (service *Service) Account(scope Scope) (Account, error) {
	if !validScope(scope) {
		return Account{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked(scope, service.clock().UTC())
	state := service.accountLocked(scope)
	return Account{Balance: state.balance, Entries: append([]LedgerEntry(nil), state.entries...)}, nil
}

func (service *Service) Credit(scope Scope, idempotencyKey, category, sourceReference string, points int64, expiresAt time.Time) (LedgerEntry, bool, error) {
	now := service.clock().UTC()
	if !validScope(scope) || !validCommand(idempotencyKey, category, sourceReference) || points < 1 || !expiresAt.After(now) || expiresAt.After(now.AddDate(3, 0, 0)) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("credit\x00%s\x00%s\x00%d\x00%s", category, sourceReference, points, expiresAt.UTC().Format(time.RFC3339Nano))
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.creditLocked(scope, idempotencyKey, category, sourceReference, points, expiresAt, fingerprint)
}

func (service *Service) creditLocked(scope Scope, idempotencyKey, category, sourceReference string, points int64, expiresAt time.Time, fingerprint string) (LedgerEntry, bool, error) {
	if entry, replayed, err := service.replayLocked(scope, idempotencyKey, fingerprint); replayed || err != nil {
		return entry, replayed, err
	}
	state := service.accountLocked(scope)
	entry := service.appendLocked(scope, state, idempotencyKey, EntryCredit, category, sourceReference, "", points, &expiresAt)
	state.lots = append(state.lots, lot{id: entry.ID, remaining: points, expiresAt: expiresAt.UTC()})
	service.rememberLocked(scope, idempotencyKey, fingerprint, entry)
	return entry, false, nil
}

func (service *Service) Redeem(scope Scope, idempotencyKey, sourceReference string, points int64) (LedgerEntry, bool, error) {
	if !validScope(scope) || !validCommand(idempotencyKey, "CHECKOUT_REDEMPTION", sourceReference) || points < 1 {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("redeem\x00%s\x00%d", sourceReference, points)
	service.mu.Lock()
	defer service.mu.Unlock()
	if entry, replayed, err := service.replayLocked(scope, idempotencyKey, fingerprint); replayed || err != nil {
		return entry, replayed, err
	}
	now := service.clock().UTC()
	service.expireLocked(scope, now)
	state := service.accountLocked(scope)
	if state.balance < points {
		return LedgerEntry{}, false, ErrInsufficientBalance
	}
	sort.SliceStable(state.lots, func(i, j int) bool { return state.lots[i].expiresAt.Before(state.lots[j].expiresAt) })
	remaining := points
	allocations := []allocation{}
	for index := range state.lots {
		if remaining == 0 {
			break
		}
		available := state.lots[index].remaining
		if available == 0 {
			continue
		}
		used := min64(available, remaining)
		state.lots[index].remaining -= used
		remaining -= used
		allocations = append(allocations, allocation{lotID: state.lots[index].id, points: used, expiresAt: state.lots[index].expiresAt})
	}
	entry := service.appendLocked(scope, state, idempotencyKey, EntryDebit, "CHECKOUT_REDEMPTION", sourceReference, "", -points, nil)
	state.allocations[entry.ID] = allocations
	service.rememberLocked(scope, idempotencyKey, fingerprint, entry)
	return entry, false, nil
}

func (service *Service) ReverseDebit(scope Scope, idempotencyKey, debitEntryID, sourceReference string) (LedgerEntry, bool, error) {
	if !validScope(scope) || !validCommand(idempotencyKey, "ORDER_REFUND", sourceReference) || !safeID(debitEntryID) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := "reverse\x00" + debitEntryID + "\x00" + sourceReference
	service.mu.Lock()
	defer service.mu.Unlock()
	if entry, replayed, err := service.replayLocked(scope, idempotencyKey, fingerprint); replayed || err != nil {
		return entry, replayed, err
	}
	state := service.accountLocked(scope)
	allocations, exists := state.allocations[debitEntryID]
	if !exists {
		return LedgerEntry{}, false, ErrEntryNotFound
	}
	if state.reversed[debitEntryID] {
		return LedgerEntry{}, false, ErrAlreadyReversed
	}
	var points int64
	var earliest *time.Time
	for _, item := range allocations {
		points += item.points
		expiry := item.expiresAt
		if earliest == nil || expiry.Before(*earliest) {
			earliest = &expiry
		}
		state.lots = append(state.lots, lot{id: debitEntryID + ":reversal:" + item.lotID, remaining: item.points, expiresAt: expiry})
	}
	entry := service.appendLocked(scope, state, idempotencyKey, EntryReversal, "ORDER_REFUND", sourceReference, debitEntryID, points, earliest)
	state.reversed[debitEntryID] = true
	service.rememberLocked(scope, idempotencyKey, fingerprint, entry)
	service.expireLocked(scope, service.clock().UTC())
	return entry, false, nil
}

func (service *Service) AwardReferral(scope Scope, idempotencyKey, referralReference string, points int64, expiresAt time.Time) (LedgerEntry, bool, error) {
	now := service.clock().UTC()
	if !validScope(scope) || !validCommand(idempotencyKey, "FIRST_PURCHASE_REFERRAL", referralReference) || points < 1 || !expiresAt.After(now) || expiresAt.After(now.AddDate(3, 0, 0)) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("credit\x00%s\x00%s\x00%d\x00%s", "FIRST_PURCHASE_REFERRAL", referralReference, points, expiresAt.UTC().Format(time.RFC3339Nano))
	service.mu.Lock()
	defer service.mu.Unlock()
	if entry, replayed, err := service.replayLocked(scope, idempotencyKey, fingerprint); replayed || err != nil {
		return entry, replayed, err
	}
	key := scopeKey(scope) + "\x00" + referralReference
	if service.referrals[key] {
		return LedgerEntry{}, false, ErrRewardNotEligible
	}
	service.referrals[key] = true
	return service.creditLocked(scope, idempotencyKey, "FIRST_PURCHASE_REFERRAL", referralReference, points, expiresAt, fingerprint)
}

func (service *Service) RewardEngagement(scope Scope, idempotencyKey, eventReference, deviceReference string, points int64, expiresAt time.Time) (LedgerEntry, bool, error) {
	now := service.clock().UTC()
	if !validScope(scope) || !validCommand(idempotencyKey, "ENGAGEMENT_REWARD", eventReference) || !safeID(deviceReference) || points < 1 || !expiresAt.After(now) || expiresAt.After(now.AddDate(3, 0, 0)) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("credit\x00%s\x00%s\x00%d\x00%s", "ENGAGEMENT_REWARD", eventReference, points, expiresAt.UTC().Format(time.RFC3339Nano))
	day := now.Format("2006-01-02")
	usageKey := scope.TenantID + "\x00" + scope.Country + "\x00" + deviceReference + "\x00" + day
	cooldownKey := scopeKey(scope) + "\x00" + deviceReference
	service.mu.Lock()
	defer service.mu.Unlock()
	if entry, replayed, err := service.replayLocked(scope, idempotencyKey, fingerprint); replayed || err != nil {
		return entry, replayed, err
	}
	if last, exists := service.rewardLast[cooldownKey]; exists && now.Sub(last) < service.rewardPolicy.Cooldown {
		return LedgerEntry{}, false, ErrRewardNotEligible
	}
	if service.rewardPolicy.DailyDeviceCap > 0 && service.rewardUsage[usageKey]+points > service.rewardPolicy.DailyDeviceCap {
		return LedgerEntry{}, false, ErrRewardNotEligible
	}
	service.rewardUsage[usageKey] += points
	service.rewardLast[cooldownKey] = now
	return service.creditLocked(scope, idempotencyKey, "ENGAGEMENT_REWARD", eventReference, points, expiresAt, fingerprint)
}

func (service *Service) Expire(scope Scope) ([]LedgerEntry, error) {
	if !validScope(scope) {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.expireLocked(scope, service.clock().UTC()), nil
}

func (service *Service) expireLocked(scope Scope, now time.Time) []LedgerEntry {
	state := service.accountLocked(scope)
	entries := []LedgerEntry{}
	for index := range state.lots {
		value := &state.lots[index]
		if value.remaining == 0 || now.Before(value.expiresAt) {
			continue
		}
		points := value.remaining
		value.remaining = 0
		expiry := value.expiresAt
		key := "expiry-" + value.id
		entries = append(entries, service.appendLocked(scope, state, key, EntryExpiry, "FIFO_EXPIRY", value.id, "", -points, &expiry))
	}
	return entries
}

func (service *Service) appendLocked(scope Scope, state *accountState, key string, kind EntryType, category, source, reverses string, delta int64, expiry *time.Time) LedgerEntry {
	digest := sha256.Sum256([]byte(scopeKey(scope) + "\x00" + key + "\x00" + string(kind)))
	state.balance += delta
	entry := LedgerEntry{
		ID: "wallet-" + hex.EncodeToString(digest[:8]), Type: kind, Category: category, SourceReference: source,
		ReversesEntryID: reverses, DeltaPoints: delta, BalanceAfter: state.balance, OriginalExpiryAt: expiry, CreatedAt: service.clock().UTC(),
	}
	state.entries = append(state.entries, entry)
	return entry
}

func (service *Service) replayLocked(scope Scope, key, fingerprint string) (LedgerEntry, bool, error) {
	value, exists := service.requests[scopeKey(scope)+"\x00"+key]
	if !exists {
		return LedgerEntry{}, false, nil
	}
	if value.fingerprint != fingerprint {
		return LedgerEntry{}, false, ErrIdempotencyConflict
	}
	return value.entry, true, nil
}

func (service *Service) rememberLocked(scope Scope, key, fingerprint string, entry LedgerEntry) {
	service.requests[scopeKey(scope)+"\x00"+key] = replay{fingerprint: fingerprint, entry: entry}
}

func (service *Service) accountLocked(scope Scope) *accountState {
	key := scopeKey(scope)
	state := service.accounts[key]
	if state == nil {
		state = &accountState{allocations: map[string][]allocation{}, reversed: map[string]bool{}}
		service.accounts[key] = state
	}
	return state
}

func validCommand(key, category, source string) bool {
	return safeID(key) && len(key) >= 16 && safeID(category) && safeID(source)
}

func validScope(scope Scope) bool {
	return safeID(scope.TenantID) && safeID(scope.CustomerID) && len(scope.Country) == 2 && strings.ToUpper(scope.Country) == scope.Country
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}

func scopeKey(scope Scope) string {
	return scope.TenantID + "\x00" + scope.Country + "\x00" + scope.CustomerID
}
func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func Sum(entries []LedgerEntry) int64 {
	var result int64
	for _, entry := range entries {
		result += entry.DeltaPoints
	}
	return result
}

func Verify(account Account) bool {
	var balance int64
	for _, entry := range account.Entries {
		balance += entry.DeltaPoints
		if balance != entry.BalanceAfter || balance < 0 {
			return false
		}
	}
	return balance == account.Balance
}
