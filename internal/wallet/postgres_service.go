package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const walletOperationTimeout = 10 * time.Second

// PostgresService is an append-only, FIFO-expiring points ledger. Account rows
// serialize balance changes while debit allocations retain the original lot
// expiries required for exact partial refunds and reversals.
type PostgresService struct {
	pool         *pgxpool.Pool
	clock        func() time.Time
	rewardPolicy RewardPolicy
	program      Program
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time, rewardPolicy RewardPolicy, program Program) (*PostgresService, error) {
	if pool == nil || clock == nil || rewardPolicy.DailyDeviceCap < 0 || rewardPolicy.Cooldown < 0 || rewardPolicy.ReferralSenderPoints < 0 || rewardPolicy.ReferralRecipientPoints < 0 || rewardPolicy.ReferralExpiry < 0 || !validProgram(program) {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock, rewardPolicy: rewardPolicy, program: cloneProgram(program)}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('wallet.accounts') IS NOT NULL
		   AND to_regclass('wallet.ledger_entries') IS NOT NULL
		   AND to_regclass('wallet.debit_allocations') IS NOT NULL
		   AND to_regclass('wallet.referral_applications') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check wallet schema readiness: %w", err)
	}
	if !ready {
		return errors.New("wallet schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Experience(scope Scope) (Experience, error) {
	account, err := service.Account(scope)
	if err != nil {
		return Experience{}, err
	}
	profile, err := service.referralProfile(scope, true)
	if err != nil {
		return Experience{}, err
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

func (service *PostgresService) RefillOffer(scope Scope, offerID string) (RefillOffer, error) {
	if !postgresWalletScope(scope) || !safeID(offerID) {
		return RefillOffer{}, ErrInvalidRequest
	}
	for _, value := range service.program.RefillOffers {
		if value.ID == offerID && value.Country == scope.Country {
			return cloneRefillOffer(value), nil
		}
	}
	return RefillOffer{}, ErrEntryNotFound
}

func (service *PostgresService) Account(scope Scope) (Account, error) {
	if !postgresWalletScope(scope) {
		return Account{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, balance, err := service.beginAccount(ctx, scope)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, balance, err = service.expireLocked(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return Account{}, err
	}
	if err := updateWalletAccount(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, fmt.Errorf("commit wallet expiry: %w", err)
	}
	rows, err := service.pool.Query(ctx, `
		SELECT id::text,entry_type,category,source_reference,COALESCE(reverses_entry_id::text,''),
		       delta_points,balance_after,original_expiry_at,created_at
		FROM wallet.ledger_entries
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3
		ORDER BY sequence_id`, scope.TenantID, scope.Country, scope.CustomerID)
	if err != nil {
		return Account{}, fmt.Errorf("load wallet ledger: %w", err)
	}
	defer rows.Close()
	entries := []LedgerEntry{}
	for rows.Next() {
		value, err := scanWalletEntry(rows)
		if err != nil {
			return Account{}, err
		}
		entries = append(entries, value)
	}
	if err := rows.Err(); err != nil {
		return Account{}, fmt.Errorf("iterate wallet ledger: %w", err)
	}
	return Account{Balance: balance, Entries: entries}, nil
}

func (service *PostgresService) Credit(scope Scope, idempotencyKey, category, sourceReference string, points int64, expiresAt time.Time) (LedgerEntry, bool, error) {
	now := service.clock().UTC()
	if !postgresWalletScope(scope) || !validCommand(idempotencyKey, category, sourceReference) || points < 1 || !expiresAt.After(now) || expiresAt.After(now.AddDate(3, 0, 0)) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	raw := fmt.Sprintf("credit\x00%s\x00%s\x00%d\x00%s", category, sourceReference, points, expiresAt.UTC().Format(time.RFC3339Nano))
	return service.credit(scope, idempotencyKey, category, sourceReference, points, expiresAt.UTC(), walletFingerprint(raw), "")
}

func (service *PostgresService) credit(scope Scope, key, category, source string, points int64, expiresAt time.Time, fingerprint, claimType string) (LedgerEntry, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, balance, err := service.beginAccount(ctx, scope)
	if err != nil {
		return LedgerEntry{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replay, found, err := loadWalletReplay(ctx, tx, scope, key, fingerprint); err != nil {
		return LedgerEntry{}, false, err
	} else if found {
		return replay, true, nil
	}
	if claimType != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet.reward_claims WHERE tenant_id=$1 AND country=$2 AND claim_type=$3 AND claim_reference=$4)`, scope.TenantID, scope.Country, claimType, source).Scan(&exists); err != nil {
			return LedgerEntry{}, false, fmt.Errorf("check wallet reward claim: %w", err)
		}
		if exists {
			return LedgerEntry{}, false, ErrRewardNotEligible
		}
	}
	if _, balance, err = service.expireLocked(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return LedgerEntry{}, false, err
	}
	entry, balance, err := appendWalletEntry(ctx, tx, scope, key, fingerprint, EntryCredit, category, source, "", points, balance, &expiresAt, service.clock().UTC())
	if err != nil {
		return LedgerEntry{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet.credit_lots (ledger_entry_id,remaining_points,expires_at) VALUES ($1,$2,$3)`, entry.ID, points, expiresAt); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("insert wallet credit lot: %w", err)
	}
	if claimType != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO wallet.reward_claims
				(tenant_id,country,claim_type,claim_reference,customer_identity_id,awarded_points,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, scope.TenantID, scope.Country, claimType, source, scope.CustomerID, points, entry.CreatedAt); err != nil {
			return LedgerEntry{}, false, fmt.Errorf("insert wallet reward claim: %w", err)
		}
	}
	if err := updateWalletAccount(ctx, tx, scope, balance, entry.CreatedAt); err != nil {
		return LedgerEntry{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("commit wallet credit: %w", err)
	}
	return entry, false, nil
}

func (service *PostgresService) Redeem(scope Scope, idempotencyKey, sourceReference string, points int64) (LedgerEntry, bool, error) {
	if !postgresWalletScope(scope) || !validCommand(idempotencyKey, "CHECKOUT_REDEMPTION", sourceReference) || points < 1 {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := walletFingerprint(fmt.Sprintf("redeem\x00%s\x00%d", sourceReference, points))
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, balance, err := service.beginAccount(ctx, scope)
	if err != nil {
		return LedgerEntry{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replay, found, err := loadWalletReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return LedgerEntry{}, false, err
	} else if found {
		return replay, true, nil
	}
	if _, balance, err = service.expireLocked(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return LedgerEntry{}, false, err
	}
	if balance < points {
		return LedgerEntry{}, false, ErrInsufficientBalance
	}
	rows, err := tx.Query(ctx, `
		SELECT lot.ledger_entry_id::text,lot.remaining_points,lot.expires_at
		FROM wallet.credit_lots lot
		JOIN wallet.ledger_entries entry ON entry.id=lot.ledger_entry_id
		WHERE entry.tenant_id=$1 AND entry.country=$2 AND entry.customer_identity_id=$3
		  AND lot.remaining_points > 0
		ORDER BY lot.expires_at,lot.ledger_entry_id FOR UPDATE OF lot`, scope.TenantID, scope.Country, scope.CustomerID)
	if err != nil {
		return LedgerEntry{}, false, fmt.Errorf("lock wallet credit lots: %w", err)
	}
	type allocationValue struct {
		id        string
		remaining int64
		expiresAt time.Time
		used      int64
	}
	allocations := []allocationValue{}
	remaining := points
	for rows.Next() {
		var value allocationValue
		if err := rows.Scan(&value.id, &value.remaining, &value.expiresAt); err != nil {
			rows.Close()
			return LedgerEntry{}, false, fmt.Errorf("scan wallet credit lot: %w", err)
		}
		value.used = min64(value.remaining, remaining)
		remaining -= value.used
		allocations = append(allocations, value)
		if remaining == 0 {
			break
		}
	}
	rows.Close()
	if remaining != 0 {
		return LedgerEntry{}, false, ErrInsufficientBalance
	}
	entry, balance, err := appendWalletEntry(ctx, tx, scope, idempotencyKey, fingerprint, EntryDebit, "CHECKOUT_REDEMPTION", sourceReference, "", -points, balance, nil, service.clock().UTC())
	if err != nil {
		return LedgerEntry{}, false, err
	}
	for _, allocation := range allocations {
		if _, err := tx.Exec(ctx, `UPDATE wallet.credit_lots SET remaining_points=remaining_points-$2 WHERE ledger_entry_id=$1`, allocation.id, allocation.used); err != nil {
			return LedgerEntry{}, false, fmt.Errorf("consume wallet credit lot: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallet.debit_allocations (debit_entry_id,credit_entry_id,allocated_points) VALUES ($1,$2,$3)`, entry.ID, allocation.id, allocation.used); err != nil {
			return LedgerEntry{}, false, fmt.Errorf("record wallet debit allocation: %w", err)
		}
	}
	if err := updateWalletAccount(ctx, tx, scope, balance, entry.CreatedAt); err != nil {
		return LedgerEntry{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("commit wallet redemption: %w", err)
	}
	return entry, false, nil
}

func (service *PostgresService) ReverseDebit(scope Scope, idempotencyKey, debitEntryID, sourceReference string) (LedgerEntry, bool, error) {
	return service.restoreDebit(scope, idempotencyKey, debitEntryID, sourceReference, 0, true)
}

func (service *PostgresService) RefundDebit(scope Scope, idempotencyKey, debitEntryID, sourceReference string, points int64) (LedgerEntry, bool, error) {
	if points < 1 {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	return service.restoreDebit(scope, idempotencyKey, debitEntryID, sourceReference, points, false)
}

func (service *PostgresService) restoreDebit(scope Scope, key, debitID, source string, requested int64, full bool) (LedgerEntry, bool, error) {
	if !postgresWalletScope(scope) || !validCommand(key, "ORDER_REFUND", source) || !walletUUID(debitID) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	raw := fmt.Sprintf("partial-refund\x00%s\x00%s\x00%d", debitID, source, requested)
	if full {
		raw = "reverse\x00" + debitID + "\x00" + source
	}
	fingerprint := walletFingerprint(raw)
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, balance, err := service.beginAccount(ctx, scope)
	if err != nil {
		return LedgerEntry{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replay, found, err := loadWalletReplay(ctx, tx, scope, key, fingerprint); err != nil {
		return LedgerEntry{}, false, err
	} else if found {
		return replay, true, nil
	}
	var entryType EntryType
	if err := tx.QueryRow(ctx, `SELECT entry_type FROM wallet.ledger_entries WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4 FOR UPDATE`, debitID, scope.TenantID, scope.Country, scope.CustomerID).Scan(&entryType); errors.Is(err, pgx.ErrNoRows) {
		return LedgerEntry{}, false, ErrEntryNotFound
	} else if err != nil {
		return LedgerEntry{}, false, fmt.Errorf("load wallet debit: %w", err)
	}
	if entryType != EntryDebit {
		return LedgerEntry{}, false, ErrEntryNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT allocation.credit_entry_id::text,allocation.allocated_points,allocation.refunded_points,lot.expires_at
		FROM wallet.debit_allocations allocation
		JOIN wallet.credit_lots lot ON lot.ledger_entry_id=allocation.credit_entry_id
		WHERE allocation.debit_entry_id=$1 ORDER BY lot.expires_at,allocation.credit_entry_id
		FOR UPDATE OF allocation,lot`, debitID)
	if err != nil {
		return LedgerEntry{}, false, fmt.Errorf("lock wallet debit allocations: %w", err)
	}
	type refundableAllocation struct {
		creditID            string
		allocated, refunded int64
		expiresAt           time.Time
		restore             int64
	}
	allocations := []refundableAllocation{}
	var available int64
	for rows.Next() {
		var value refundableAllocation
		if err := rows.Scan(&value.creditID, &value.allocated, &value.refunded, &value.expiresAt); err != nil {
			rows.Close()
			return LedgerEntry{}, false, fmt.Errorf("scan wallet debit allocation: %w", err)
		}
		available += value.allocated - value.refunded
		allocations = append(allocations, value)
	}
	rows.Close()
	if available == 0 || len(allocations) == 0 || (!full && requested > available) {
		return LedgerEntry{}, false, ErrAlreadyReversed
	}
	points := requested
	if full {
		points = available
	}
	remaining := points
	var earliest *time.Time
	for index := range allocations {
		availableInLot := allocations[index].allocated - allocations[index].refunded
		allocations[index].restore = min64(availableInLot, remaining)
		if allocations[index].restore > 0 {
			expiry := allocations[index].expiresAt
			if earliest == nil || expiry.Before(*earliest) {
				earliest = &expiry
			}
		}
		remaining -= allocations[index].restore
		if remaining == 0 {
			break
		}
	}
	entry, balance, err := appendWalletEntry(ctx, tx, scope, key, fingerprint, EntryReversal, "ORDER_REFUND", source, debitID, points, balance, earliest, service.clock().UTC())
	if err != nil {
		return LedgerEntry{}, false, err
	}
	for _, allocation := range allocations {
		if allocation.restore == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE wallet.credit_lots SET remaining_points=remaining_points+$2 WHERE ledger_entry_id=$1`, allocation.creditID, allocation.restore); err != nil {
			return LedgerEntry{}, false, fmt.Errorf("restore wallet credit lot: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE wallet.debit_allocations SET refunded_points=refunded_points+$3 WHERE debit_entry_id=$1 AND credit_entry_id=$2`, debitID, allocation.creditID, allocation.restore); err != nil {
			return LedgerEntry{}, false, fmt.Errorf("record wallet debit refund: %w", err)
		}
	}
	if _, balance, err = service.expireLocked(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return LedgerEntry{}, false, err
	}
	if err := updateWalletAccount(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return LedgerEntry{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("commit wallet debit restoration: %w", err)
	}
	return entry, false, nil
}

func (service *PostgresService) Expire(scope Scope) ([]LedgerEntry, error) {
	if !postgresWalletScope(scope) {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, balance, err := service.beginAccount(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	entries, balance, err := service.expireLocked(ctx, tx, scope, balance, service.clock().UTC())
	if err != nil {
		return nil, err
	}
	if err := updateWalletAccount(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit wallet expiry: %w", err)
	}
	return entries, nil
}

func (service *PostgresService) expireLocked(ctx context.Context, tx pgx.Tx, scope Scope, balance int64, now time.Time) ([]LedgerEntry, int64, error) {
	rows, err := tx.Query(ctx, `
		SELECT lot.ledger_entry_id::text,lot.remaining_points,lot.expires_at
		FROM wallet.credit_lots lot JOIN wallet.ledger_entries entry ON entry.id=lot.ledger_entry_id
		WHERE entry.tenant_id=$1 AND entry.country=$2 AND entry.customer_identity_id=$3
		  AND lot.remaining_points>0 AND lot.expires_at <= $4
		ORDER BY lot.expires_at,lot.ledger_entry_id FOR UPDATE OF lot`, scope.TenantID, scope.Country, scope.CustomerID, now)
	if err != nil {
		return nil, balance, fmt.Errorf("lock expired wallet lots: %w", err)
	}
	type expiredLot struct {
		id        string
		points    int64
		expiresAt time.Time
	}
	lots := []expiredLot{}
	for rows.Next() {
		var value expiredLot
		if err := rows.Scan(&value.id, &value.points, &value.expiresAt); err != nil {
			rows.Close()
			return nil, balance, fmt.Errorf("scan expired wallet lot: %w", err)
		}
		lots = append(lots, value)
	}
	rows.Close()
	entries := []LedgerEntry{}
	for _, lot := range lots {
		key := "expiry-" + lot.id
		fingerprint := walletFingerprint(key)
		entry, nextBalance, err := appendWalletEntry(ctx, tx, scope, key, fingerprint, EntryExpiry, "FIFO_EXPIRY", lot.id, "", -lot.points, balance, &lot.expiresAt, now)
		if err != nil {
			return nil, balance, err
		}
		balance = nextBalance
		if _, err := tx.Exec(ctx, `UPDATE wallet.credit_lots SET remaining_points=0 WHERE ledger_entry_id=$1`, lot.id); err != nil {
			return nil, balance, fmt.Errorf("expire wallet credit lot: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, balance, nil
}

func (service *PostgresService) ApplyReferral(scope Scope, idempotencyKey, code string) (ReferralProfile, bool, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !postgresWalletScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(code) {
		return ReferralProfile{}, false, ErrInvalidRequest
	}
	fingerprint := walletFingerprint(code)
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReferralProfile{}, false, fmt.Errorf("begin referral application: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID + ":" + idempotencyKey
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return ReferralProfile{}, false, fmt.Errorf("lock referral application: %w", err)
	}
	if profile, found, err := loadReferralReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return ReferralProfile{}, false, err
	} else if found {
		return profile, true, nil
	}
	var owner Scope
	if err := tx.QueryRow(ctx, `SELECT tenant_id::text,country,customer_identity_id::text FROM wallet.referral_codes WHERE code=$1 FOR UPDATE`, code).Scan(&owner.TenantID, &owner.Country, &owner.CustomerID); errors.Is(err, pgx.ErrNoRows) {
		return ReferralProfile{}, false, ErrRewardNotEligible
	} else if err != nil {
		return ReferralProfile{}, false, fmt.Errorf("load referral code: %w", err)
	}
	if owner == scope {
		return ReferralProfile{}, false, ErrRewardNotEligible
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet.referral_applications WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3)`, scope.TenantID, scope.Country, scope.CustomerID).Scan(&exists); err != nil {
		return ReferralProfile{}, false, fmt.Errorf("check referral application: %w", err)
	}
	if exists {
		return ReferralProfile{}, false, ErrRewardNotEligible
	}
	now := service.clock().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO wallet.referral_applications (tenant_id,country,customer_identity_id,referral_code,applied_at) VALUES ($1,$2,$3,$4,$5)`, scope.TenantID, scope.Country, scope.CustomerID, code, now); err != nil {
		return ReferralProfile{}, false, fmt.Errorf("insert referral application: %w", err)
	}
	profile := service.profile(scope, referralCode(scope), code, false)
	payload, _ := json.Marshal(profile)
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet.referral_requests
			(tenant_id,country,customer_identity_id,idempotency_key,request_fingerprint,response_payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`, scope.TenantID, scope.Country, scope.CustomerID, idempotencyKey, fingerprint, payload, now); err != nil {
		return ReferralProfile{}, false, fmt.Errorf("store referral replay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReferralProfile{}, false, fmt.Errorf("commit referral application: %w", err)
	}
	return profile, false, nil
}

func (service *PostgresService) ActivateReferral(referred Scope, purchaseReference string) ([]LedgerEntry, error) {
	if !postgresWalletScope(referred) || !safeID(purchaseReference) || service.rewardPolicy.ReferralSenderPoints < 1 || service.rewardPolicy.ReferralRecipientPoints < 1 || service.rewardPolicy.ReferralExpiry <= 0 {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin referral activation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var code string
	var activated bool
	if err := tx.QueryRow(ctx, `SELECT referral_code,activated FROM wallet.referral_applications WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 FOR UPDATE`, referred.TenantID, referred.Country, referred.CustomerID).Scan(&code, &activated); errors.Is(err, pgx.ErrNoRows) || activated {
		return nil, ErrRewardNotEligible
	} else if err != nil {
		return nil, fmt.Errorf("load referral activation: %w", err)
	}
	var owner Scope
	if err := tx.QueryRow(ctx, `SELECT tenant_id::text,country,customer_identity_id::text FROM wallet.referral_codes WHERE code=$1`, code).Scan(&owner.TenantID, &owner.Country, &owner.CustomerID); err != nil {
		return nil, fmt.Errorf("load referral owner: %w", err)
	}
	scopes := []Scope{owner, referred}
	sort.Slice(scopes, func(i, j int) bool { return scopeKey(scopes[i]) < scopeKey(scopes[j]) })
	balances := map[string]int64{}
	for _, scope := range scopes {
		balance, err := lockWalletAccount(ctx, tx, scope, service.clock().UTC())
		if err != nil {
			return nil, err
		}
		if _, balance, err = service.expireLocked(ctx, tx, scope, balance, service.clock().UTC()); err != nil {
			return nil, err
		}
		balances[scopeKey(scope)] = balance
	}
	digest := sha256.Sum256([]byte(scopeKey(referred) + "\x00" + purchaseReference))
	seed := hex.EncodeToString(digest[:8])
	expiresAt := service.clock().UTC().Add(service.rewardPolicy.ReferralExpiry)
	entries := make([]LedgerEntry, 0, 2)
	for _, award := range []struct {
		scope  Scope
		key    string
		points int64
	}{{owner, "referral-sender-" + seed, service.rewardPolicy.ReferralSenderPoints}, {referred, "referral-recipient-" + seed, service.rewardPolicy.ReferralRecipientPoints}} {
		fingerprint := walletFingerprint(award.key + ":" + purchaseReference)
		entry, balance, err := appendWalletEntry(ctx, tx, award.scope, award.key, fingerprint, EntryCredit, "FIRST_PURCHASE_REFERRAL", "purchase-"+seed, "", award.points, balances[scopeKey(award.scope)], &expiresAt, service.clock().UTC())
		if err != nil {
			return nil, err
		}
		balances[scopeKey(award.scope)] = balance
		if _, err := tx.Exec(ctx, `INSERT INTO wallet.credit_lots (ledger_entry_id,remaining_points,expires_at) VALUES ($1,$2,$3)`, entry.ID, award.points, expiresAt); err != nil {
			return nil, fmt.Errorf("insert referral credit lot: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallet.reward_claims (tenant_id,country,claim_type,claim_reference,customer_identity_id,awarded_points,created_at) VALUES ($1,$2,'REFERRAL',$3,$4,$5,$6)`, award.scope.TenantID, award.scope.Country, purchaseReference+":"+award.scope.CustomerID, award.scope.CustomerID, award.points, entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("insert referral reward claim: %w", err)
		}
		entries = append(entries, entry)
	}
	for _, scope := range scopes {
		if err := updateWalletAccount(ctx, tx, scope, balances[scopeKey(scope)], service.clock().UTC()); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE wallet.referral_applications SET activated=true,purchase_reference=$4,activated_at=$5 WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3`, referred.TenantID, referred.Country, referred.CustomerID, purchaseReference, service.clock().UTC()); err != nil {
		return nil, fmt.Errorf("activate referral application: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit referral activation: %w", err)
	}
	return entries, nil
}

func (service *PostgresService) AwardReferral(scope Scope, idempotencyKey, referralReference string, points int64, expiresAt time.Time) (LedgerEntry, bool, error) {
	now := service.clock().UTC()
	if !postgresWalletScope(scope) || !validCommand(idempotencyKey, "FIRST_PURCHASE_REFERRAL", referralReference) || points < 1 || !expiresAt.After(now) || expiresAt.After(now.AddDate(3, 0, 0)) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := walletFingerprint(fmt.Sprintf("credit\x00FIRST_PURCHASE_REFERRAL\x00%s\x00%d\x00%s", referralReference, points, expiresAt.UTC().Format(time.RFC3339Nano)))
	return service.credit(scope, idempotencyKey, "FIRST_PURCHASE_REFERRAL", referralReference, points, expiresAt.UTC(), fingerprint, "REFERRAL")
}

func (service *PostgresService) RewardEngagement(scope Scope, idempotencyKey, eventReference, deviceReference string, points int64, expiresAt time.Time) (LedgerEntry, bool, error) {
	now := service.clock().UTC()
	if !postgresWalletScope(scope) || !validCommand(idempotencyKey, "ENGAGEMENT_REWARD", eventReference) || !safeID(deviceReference) || points < 1 || !expiresAt.After(now) || expiresAt.After(now.AddDate(3, 0, 0)) {
		return LedgerEntry{}, false, ErrInvalidRequest
	}
	fingerprint := walletFingerprint(fmt.Sprintf("credit\x00ENGAGEMENT_REWARD\x00%s\x00%d\x00%s", eventReference, points, expiresAt.UTC().Format(time.RFC3339Nano)))
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	tx, balance, err := service.beginAccount(ctx, scope)
	if err != nil {
		return LedgerEntry{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replay, found, err := loadWalletReplay(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return LedgerEntry{}, false, err
	} else if found {
		return replay, true, nil
	}
	deviceDigest := sha256.Sum256([]byte(deviceReference))
	deviceHash := hex.EncodeToString(deviceDigest[:])
	dayStart := now.Truncate(24 * time.Hour)
	var used int64
	var last *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(awarded_points) FILTER (WHERE created_at >= $4),0),max(created_at)
		FROM wallet.reward_claims
		WHERE tenant_id=$1 AND country=$2 AND device_reference_hash=$3`, scope.TenantID, scope.Country, deviceHash, dayStart).Scan(&used, &last); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("load engagement reward limits: %w", err)
	}
	if (last != nil && now.Sub(*last) < service.rewardPolicy.Cooldown) || (service.rewardPolicy.DailyDeviceCap > 0 && used+points > service.rewardPolicy.DailyDeviceCap) {
		return LedgerEntry{}, false, ErrRewardNotEligible
	}
	if _, balance, err = service.expireLocked(ctx, tx, scope, balance, now); err != nil {
		return LedgerEntry{}, false, err
	}
	entry, balance, err := appendWalletEntry(ctx, tx, scope, idempotencyKey, fingerprint, EntryCredit, "ENGAGEMENT_REWARD", eventReference, "", points, balance, &expiresAt, now)
	if err != nil {
		return LedgerEntry{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet.credit_lots (ledger_entry_id,remaining_points,expires_at) VALUES ($1,$2,$3)`, entry.ID, points, expiresAt); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("insert engagement credit lot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet.reward_claims
			(tenant_id,country,claim_type,claim_reference,customer_identity_id,device_reference_hash,awarded_points,created_at)
		VALUES ($1,$2,'ENGAGEMENT',$3,$4,$5,$6,$7)`, scope.TenantID, scope.Country, eventReference, scope.CustomerID, deviceHash, points, now); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("insert engagement reward claim: %w", err)
	}
	if err := updateWalletAccount(ctx, tx, scope, balance, now); err != nil {
		return LedgerEntry{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LedgerEntry{}, false, fmt.Errorf("commit engagement reward: %w", err)
	}
	return entry, false, nil
}

func (service *PostgresService) referralProfile(scope Scope, ensure bool) (ReferralProfile, error) {
	if !postgresWalletScope(scope) {
		return ReferralProfile{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), walletOperationTimeout)
	defer cancel()
	code := referralCode(scope)
	if ensure {
		if _, err := service.pool.Exec(ctx, `INSERT INTO wallet.referral_codes (code,tenant_id,country,customer_identity_id,created_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, code, scope.TenantID, scope.Country, scope.CustomerID, service.clock().UTC()); err != nil {
			return ReferralProfile{}, fmt.Errorf("ensure referral code: %w", err)
		}
	}
	var applicationCode string
	var activated bool
	err := service.pool.QueryRow(ctx, `SELECT referral_code,activated FROM wallet.referral_applications WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3`, scope.TenantID, scope.Country, scope.CustomerID).Scan(&applicationCode, &activated)
	if errors.Is(err, pgx.ErrNoRows) {
		applicationCode, activated = "", false
	} else if err != nil {
		return ReferralProfile{}, fmt.Errorf("load referral profile: %w", err)
	}
	pendingCode := ""
	if applicationCode != "" && !activated {
		pendingCode = applicationCode
	}
	return service.profile(scope, code, pendingCode, activated), nil
}

func (service *PostgresService) profile(_ Scope, code, pending string, rewarded bool) ReferralProfile {
	profile := ReferralProfile{Code: code, SenderPoints: service.rewardPolicy.ReferralSenderPoints, RecipientPoints: service.rewardPolicy.ReferralRecipientPoints, PendingCode: pending, Rewarded: rewarded}
	if service.program.ReferralBaseURL != "" {
		profile.ShareURL = strings.TrimRight(service.program.ReferralBaseURL, "/") + "/" + code
	}
	return profile
}

func (service *PostgresService) beginAccount(ctx context.Context, scope Scope) (pgx.Tx, int64, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, 0, fmt.Errorf("begin wallet transaction: %w", err)
	}
	balance, err := lockWalletAccount(ctx, tx, scope, service.clock().UTC())
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, 0, err
	}
	return tx, balance, nil
}

func lockWalletAccount(ctx context.Context, tx pgx.Tx, scope Scope, now time.Time) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO wallet.accounts (tenant_id,country,customer_identity_id,balance_points,revision,updated_at) VALUES ($1,$2,$3,0,0,$4) ON CONFLICT DO NOTHING`, scope.TenantID, scope.Country, scope.CustomerID, now); err != nil {
		return 0, fmt.Errorf("ensure wallet account: %w", err)
	}
	var balance int64
	if err := tx.QueryRow(ctx, `SELECT balance_points FROM wallet.accounts WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 FOR UPDATE`, scope.TenantID, scope.Country, scope.CustomerID).Scan(&balance); err != nil {
		return 0, fmt.Errorf("lock wallet account: %w", err)
	}
	return balance, nil
}

func updateWalletAccount(ctx context.Context, tx pgx.Tx, scope Scope, balance int64, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE wallet.accounts SET balance_points=$4,revision=revision+1,updated_at=$5 WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3`, scope.TenantID, scope.Country, scope.CustomerID, balance, now); err != nil {
		return fmt.Errorf("update wallet account: %w", err)
	}
	return nil
}

func appendWalletEntry(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string, kind EntryType, category, source, reverses string, delta, balance int64, expiry *time.Time, now time.Time) (LedgerEntry, int64, error) {
	next := balance + delta
	if delta == 0 || next < 0 {
		return LedgerEntry{}, balance, ErrInsufficientBalance
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(scopeKey(scope)+":"+key+":"+string(kind))).String()
	value := LedgerEntry{ID: id, Type: kind, Category: category, SourceReference: source, ReversesEntryID: reverses, DeltaPoints: delta, BalanceAfter: next, OriginalExpiryAt: expiry, CreatedAt: now}
	var reversesValue any
	if reverses != "" {
		reversesValue = reverses
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet.ledger_entries
			(id,tenant_id,country,customer_identity_id,entry_type,category,source_reference,
			 reverses_entry_id,delta_points,balance_after,original_expiry_at,idempotency_key,request_fingerprint,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		value.ID, scope.TenantID, scope.Country, scope.CustomerID, value.Type, value.Category,
		value.SourceReference, reversesValue, value.DeltaPoints, value.BalanceAfter,
		value.OriginalExpiryAt, key, fingerprint, value.CreatedAt); err != nil {
		return LedgerEntry{}, balance, fmt.Errorf("append wallet ledger: %w", err)
	}
	return value, next, nil
}

func loadWalletReplay(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string) (LedgerEntry, bool, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text,entry_type,category,source_reference,COALESCE(reverses_entry_id::text,''),
		       delta_points,balance_after,original_expiry_at,created_at,request_fingerprint
		FROM wallet.ledger_entries
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND idempotency_key=$4`, scope.TenantID, scope.Country, scope.CustomerID, key)
	value, storedFingerprint, err := scanWalletReplay(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return LedgerEntry{}, false, nil
	}
	if err != nil {
		return LedgerEntry{}, false, err
	}
	if storedFingerprint != fingerprint {
		return LedgerEntry{}, false, ErrIdempotencyConflict
	}
	return value, true, nil
}

type walletScanner interface{ Scan(...any) error }

func scanWalletEntry(scanner walletScanner) (LedgerEntry, error) {
	var value LedgerEntry
	if err := scanner.Scan(&value.ID, &value.Type, &value.Category, &value.SourceReference, &value.ReversesEntryID, &value.DeltaPoints, &value.BalanceAfter, &value.OriginalExpiryAt, &value.CreatedAt); err != nil {
		return LedgerEntry{}, fmt.Errorf("scan wallet ledger entry: %w", err)
	}
	return value, nil
}

func scanWalletReplay(scanner walletScanner) (LedgerEntry, string, error) {
	var value LedgerEntry
	var fingerprint string
	if err := scanner.Scan(&value.ID, &value.Type, &value.Category, &value.SourceReference, &value.ReversesEntryID, &value.DeltaPoints, &value.BalanceAfter, &value.OriginalExpiryAt, &value.CreatedAt, &fingerprint); err != nil {
		return LedgerEntry{}, "", err
	}
	return value, fingerprint, nil
}

func loadReferralReplay(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string) (ReferralProfile, bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM wallet.referral_requests WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND idempotency_key=$4`, scope.TenantID, scope.Country, scope.CustomerID, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReferralProfile{}, false, nil
	}
	if err != nil {
		return ReferralProfile{}, false, fmt.Errorf("load referral replay: %w", err)
	}
	if storedFingerprint != fingerprint {
		return ReferralProfile{}, false, ErrIdempotencyConflict
	}
	var value ReferralProfile
	if json.Unmarshal(payload, &value) != nil {
		return ReferralProfile{}, false, fmt.Errorf("decode referral replay: %w", ErrInvalidRequest)
	}
	return value, true, nil
}

func walletFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func postgresWalletScope(scope Scope) bool {
	return walletUUID(scope.TenantID) && walletUUID(scope.CustomerID) && len(scope.Country) == 2 && scope.Country == strings.ToUpper(scope.Country)
}

func walletUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
