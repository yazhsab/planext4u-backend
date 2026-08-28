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
	refunded    map[string]int64
}

type replay struct {
	fingerprint string
	entry       LedgerEntry
}

type Service struct {
	clock              func() time.Time
	rewardPolicy       RewardPolicy
	program            Program
	mu                 sync.Mutex
	accounts           map[string]*accountState
	requests           map[string]replay
	referrals          map[string]bool
	rewardUsage        map[string]int64
	rewardLast         map[string]time.Time
	referralCodes      map[string]Scope
	pendingReferrals   map[string]string
	activatedReferrals map[string]bool
	referralRequests   map[string]string
}

func NewService(clock func() time.Time, rewardPolicy RewardPolicy) (*Service, error) {
	return NewServiceWithProgram(clock, rewardPolicy, Program{})
}

func NewServiceWithProgram(clock func() time.Time, rewardPolicy RewardPolicy, program Program) (*Service, error) {
	if clock == nil || rewardPolicy.DailyDeviceCap < 0 || rewardPolicy.Cooldown < 0 || rewardPolicy.ReferralSenderPoints < 0 || rewardPolicy.ReferralRecipientPoints < 0 || rewardPolicy.ReferralExpiry < 0 || !validProgram(program) {
		return nil, ErrInvalidRequest
	}
	return &Service{
		clock: clock, rewardPolicy: rewardPolicy, program: cloneProgram(program), accounts: map[string]*accountState{}, requests: map[string]replay{},
		referrals: map[string]bool{}, rewardUsage: map[string]int64{}, rewardLast: map[string]time.Time{},
		referralCodes: map[string]Scope{}, pendingReferrals: map[string]string{}, activatedReferrals: map[string]bool{}, referralRequests: map[string]string{},
	}, nil
}

func (service *Service) Experience(scope Scope) (Experience, error) {
	account, err := service.Account(scope)
	if err != nil {
		return Experience{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	code := referralCode(scope)
	service.referralCodes[code] = scope
	profile := ReferralProfile{Code: code, SenderPoints: service.rewardPolicy.ReferralSenderPoints, RecipientPoints: service.rewardPolicy.ReferralRecipientPoints, PendingCode: service.pendingReferrals[scopeKey(scope)], Rewarded: service.activatedReferrals[scopeKey(scope)]}
	if service.program.ReferralBaseURL != "" {
		profile.ShareURL = strings.TrimRight(service.program.ReferralBaseURL, "/") + "/" + code
	}
	refills := []RefillOffer{}
	for _, value := range service.program.RefillOffers {
		if value.Country == scope.Country {
			refills = append(refills, cloneRefillOffer(value))
		}
	}
	campaigns := []RewardCampaign{}
	now := service.clock().UTC()
	for _, value := range service.program.Campaigns {
		if value.Country == scope.Country && now.Before(value.EndsAt) {
			campaigns = append(campaigns, value)
		}
	}
	return Experience{Account: account, Referral: profile, Refills: refills, Campaigns: campaigns}, nil
}

func (service *Service) RefillOffer(scope Scope, offerID string) (RefillOffer, error) {
	if !validScope(scope) || !safeID(offerID) {
		return RefillOffer{}, ErrInvalidRequest
	}
	for _, value := range service.program.RefillOffers {
		if value.ID == offerID && value.Country == scope.Country {
			return cloneRefillOffer(value), nil
		}
	}
	return RefillOffer{}, ErrEntryNotFound
}

// ApplyReferral records a referral for award only after the referred
// customer's first captured purchase. It does not mint points client-side.
func (service *Service) ApplyReferral(scope Scope, idempotencyKey, code string) (ReferralProfile, bool, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(code) {
		return ReferralProfile{}, false, ErrInvalidRequest
	}
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	if existing, ok := service.referralRequests[requestKey]; ok {
		if existing != code {
			service.mu.Unlock()
			return ReferralProfile{}, false, ErrIdempotencyConflict
		}
		service.mu.Unlock()
		experience, err := service.Experience(scope)
		return experience.Referral, true, err
	}
	owner, exists := service.referralCodes[code]
	key := scopeKey(scope)
	if !exists || owner == scope || service.pendingReferrals[key] != "" || service.activatedReferrals[key] {
		service.mu.Unlock()
		return ReferralProfile{}, false, ErrRewardNotEligible
	}
	service.pendingReferrals[key] = code
	service.referralRequests[requestKey] = code
	service.mu.Unlock()
	experience, err := service.Experience(scope)
	return experience.Referral, false, err
}

// ActivateReferral is called only by the authoritative captured-payment path.
func (service *Service) ActivateReferral(referred Scope, purchaseReference string) ([]LedgerEntry, error) {
	if !validScope(referred) || !safeID(purchaseReference) {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	key := scopeKey(referred)
	code := service.pendingReferrals[key]
	owner, exists := service.referralCodes[code]
	already := service.activatedReferrals[key]
	service.mu.Unlock()
	if already || !exists || service.rewardPolicy.ReferralSenderPoints < 1 || service.rewardPolicy.ReferralRecipientPoints < 1 || service.rewardPolicy.ReferralExpiry <= 0 {
		return nil, ErrRewardNotEligible
	}
	expiresAt := service.clock().UTC().Add(service.rewardPolicy.ReferralExpiry)
	digest := sha256.Sum256([]byte(key + "\x00" + purchaseReference))
	seed := hex.EncodeToString(digest[:8])
	sender, _, err := service.AwardReferral(owner, "referral-sender-"+seed, "purchase-"+seed, service.rewardPolicy.ReferralSenderPoints, expiresAt)
	if err != nil {
		return nil, err
	}
	recipient, _, err := service.AwardReferral(referred, "referral-recipient-"+seed, "purchase-"+seed, service.rewardPolicy.ReferralRecipientPoints, expiresAt)
	if err != nil {
		return nil, err
	}
	service.mu.Lock()
	service.activatedReferrals[key] = true
	delete(service.pendingReferrals, key)
	service.mu.Unlock()
	return []LedgerEntry{sender, recipient}, nil
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
	var total int64
	for _, item := range allocations {
		total += item.points
	}
	already := state.refunded[debitEntryID]
	var points int64
	var earliest *time.Time
	skip := already
	for _, item := range allocations {
		available := item.points
		if skip >= available {
			skip -= available
			continue
		}
		available -= skip
		skip = 0
		points += available
		expiry := item.expiresAt
		if earliest == nil || expiry.Before(*earliest) {
			earliest = &expiry
		}
		state.lots = append(state.lots, lot{id: debitEntryID + ":reversal:" + item.lotID, remaining: available, expiresAt: expiry})
	}
	if points == 0 {
		return LedgerEntry{}, false, ErrAlreadyReversed
	}
	entry := service.appendLocked(scope, state, idempotencyKey, EntryReversal, "ORDER_REFUND", sourceReference, debitEntryID, points, earliest)
	state.reversed[debitEntryID] = true
	state.refunded[debitEntryID] = total
	service.rememberLocked(scope, idempotencyKey, fingerprint, entry)
	service.expireLocked(scope, service.clock().UTC())
	return entry, false, nil
}

// RefundDebit restores part of a checkout redemption while retaining the
// original FIFO expiry dates. Multiple partial refunds may not exceed the
// original debit and each command is idempotent.
func (service *Service) RefundDebit(scope Scope, idempotencyKey, debitEntryID, sourceReference string, points int64) (LedgerEntry, bool, error) {
	if !validScope(scope) || !validCommand(idempotencyKey, "ORDER_REFUND", sourceReference) || !safeID(debitEntryID) || points < 1 {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := fmt.Sprintf("partial-refund\x00%s\x00%s\x00%d", debitEntryID, sourceReference, points)
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
	var total int64
	for _, value := range allocations {
		total += value.points
	}
	already := state.refunded[debitEntryID]
	if points > total-already {
		return LedgerEntry{}, false, ErrAlreadyReversed
	}
	remaining, skip := points, already
	var earliest *time.Time
	for _, value := range allocations {
		available := value.points
		if skip >= available {
			skip -= available
			continue
		}
		available -= skip
		skip = 0
		restored := min64(available, remaining)
		expiry := value.expiresAt
		state.lots = append(state.lots, lot{id: fmt.Sprintf("%s:refund:%d:%s", debitEntryID, already+points-remaining, value.lotID), remaining: restored, expiresAt: expiry})
		if earliest == nil || expiry.Before(*earliest) {
			earliest = &expiry
		}
		remaining -= restored
		if remaining == 0 {
			break
		}
	}
	entry := service.appendLocked(scope, state, idempotencyKey, EntryReversal, "ORDER_REFUND", sourceReference, debitEntryID, points, earliest)
	state.refunded[debitEntryID] += points
	state.reversed[debitEntryID] = state.refunded[debitEntryID] == total
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
		state = &accountState{allocations: map[string][]allocation{}, reversed: map[string]bool{}, refunded: map[string]int64{}}
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

func validProgram(value Program) bool {
	if value.ReferralBaseURL != "" && (!strings.HasPrefix(value.ReferralBaseURL, "https://") || len(value.ReferralBaseURL) > 500) {
		return false
	}
	seen := map[string]bool{}
	for _, offer := range value.RefillOffers {
		if !safeID(offer.ID) || len(offer.Country) != 2 || offer.Country != strings.ToUpper(offer.Country) || offer.Points < 1 || offer.BonusPoints < 0 || offer.Price.AmountMinor < 1 || len(offer.Price.Currency) != 3 || offer.Price.Currency != strings.ToUpper(offer.Price.Currency) || offer.ExpiresAfter <= 0 || offer.ExpiresAfter > 3*365*24*time.Hour || len(offer.PaymentMethods) == 0 || seen[offer.Country+"\x00"+offer.ID] {
			return false
		}
		for _, method := range offer.PaymentMethods {
			if method != "RAZORPAY" && method != "PAYSTACK" {
				return false
			}
		}
		seen[offer.Country+"\x00"+offer.ID] = true
	}
	for _, campaign := range value.Campaigns {
		if !safeID(campaign.ID) || len(campaign.Country) != 2 || strings.TrimSpace(campaign.Title) == "" || strings.TrimSpace(campaign.Description) == "" || campaign.Points < 1 || campaign.EndsAt.IsZero() {
			return false
		}
	}
	return true
}

func referralCode(scope Scope) string {
	digest := sha256.Sum256([]byte(scopeKey(scope)))
	return "P4U" + strings.ToUpper(hex.EncodeToString(digest[:4]))
}

func cloneRefillOffer(value RefillOffer) RefillOffer {
	value.PaymentMethods = append([]string(nil), value.PaymentMethods...)
	return value
}

func cloneProgram(value Program) Program {
	value.RefillOffers = append([]RefillOffer(nil), value.RefillOffers...)
	for index := range value.RefillOffers {
		value.RefillOffers[index] = cloneRefillOffer(value.RefillOffers[index])
	}
	value.Campaigns = append([]RewardCampaign(nil), value.Campaigns...)
	return value
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
