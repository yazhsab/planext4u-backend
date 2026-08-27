package wallet

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBEWallet001EarnRedeemExpiryAndReversalReconcile(t *testing.T) {
	t.Parallel()
	clock := newWalletClock()
	service, _ := NewService(clock.Now, RewardPolicy{DailyDeviceCap: 100, Cooldown: time.Minute})
	scope := walletScope()
	firstExpiry := clock.Now().Add(24 * time.Hour)
	secondExpiry := clock.Now().Add(48 * time.Hour)
	first, _, err := service.Credit(scope, "idem-wallet-credit-0001", "ORDER_REWARD", "order-001", 100, firstExpiry)
	if err != nil || first.BalanceAfter != 100 {
		t.Fatalf("first credit = %#v err=%v", first, err)
	}
	_, _, _ = service.Credit(scope, "idem-wallet-credit-0002", "PROMOTION", "campaign-001", 80, secondExpiry)
	debit, _, err := service.Redeem(scope, "idem-wallet-debit-0001", "checkout-001", 120)
	if err != nil || debit.BalanceAfter != 60 {
		t.Fatalf("debit = %#v err=%v", debit, err)
	}
	reversal, _, err := service.ReverseDebit(scope, "idem-wallet-reverse-0001", debit.ID, "refund-001")
	if err != nil || reversal.BalanceAfter != 180 || reversal.OriginalExpiryAt == nil || !reversal.OriginalExpiryAt.Equal(firstExpiry) {
		t.Fatalf("reversal = %#v err=%v", reversal, err)
	}
	clock.Advance(24 * time.Hour)
	expired, _ := service.Expire(scope)
	if Sum(expired) != -100 {
		t.Fatalf("first FIFO expiry delta = %d", Sum(expired))
	}
	clock.Advance(24 * time.Hour)
	expired, _ = service.Expire(scope)
	if Sum(expired) != -80 {
		t.Fatalf("second FIFO expiry delta = %d", Sum(expired))
	}
	account, _ := service.Account(scope)
	if account.Balance != 0 || !Verify(account) || Sum(account.Entries) != 0 {
		t.Fatalf("account does not reconcile: %#v", account)
	}
}

func TestWalletIdempotencyReferralAndAntiAbuse(t *testing.T) {
	t.Parallel()
	clock := newWalletClock()
	service, _ := NewService(clock.Now, RewardPolicy{DailyDeviceCap: 20, Cooldown: time.Minute})
	scope := walletScope()
	expiry := clock.Now().Add(30 * 24 * time.Hour)
	first, _, _ := service.AwardReferral(scope, "idem-referral-award-0001", "referral-first-order-001", 10, expiry)
	replayReferral, replayed, err := service.AwardReferral(scope, "idem-referral-award-0001", "referral-first-order-001", 10, expiry)
	if err != nil || !replayed || replayReferral.ID != first.ID {
		t.Fatalf("referral replay = %#v replayed=%v err=%v", replayReferral, replayed, err)
	}
	if _, _, err := service.AwardReferral(scope, "idem-referral-award-0002", "referral-first-order-001", 10, expiry); !errors.Is(err, ErrRewardNotEligible) {
		t.Fatalf("duplicate referral = %v", err)
	}
	replay, replayed, err := service.Credit(scope, "idem-referral-award-0001", "FIRST_PURCHASE_REFERRAL", "referral-first-order-001", 10, expiry)
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatalf("credit replay = %#v replayed=%v err=%v", replay, replayed, err)
	}
	if _, _, err := service.RewardEngagement(scope, "idem-engagement-0001", "view-proof-001", "device-001", 15, expiry); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := service.RewardEngagement(scope, "idem-engagement-0001", "view-proof-001", "device-001", 15, expiry); err != nil || !replayed {
		t.Fatalf("engagement replay replayed=%v err=%v", replayed, err)
	}
	if _, _, err := service.RewardEngagement(scope, "idem-engagement-0002", "view-proof-002", "device-001", 5, expiry); !errors.Is(err, ErrRewardNotEligible) {
		t.Fatalf("cooldown abuse = %v", err)
	}
	clock.Advance(time.Minute)
	if _, _, err := service.RewardEngagement(scope, "idem-engagement-0003", "view-proof-003", "device-001", 10, expiry); !errors.Is(err, ErrRewardNotEligible) {
		t.Fatalf("daily cap abuse = %v", err)
	}
}

type walletClock struct {
	mu    sync.Mutex
	value time.Time
}

func newWalletClock() *walletClock {
	return &walletClock{value: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}
}
func (clock *walletClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.value
}
func (clock *walletClock) Advance(value time.Duration) {
	clock.mu.Lock()
	clock.value = clock.value.Add(value)
	clock.mu.Unlock()
}
func walletScope() Scope {
	return Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
}
