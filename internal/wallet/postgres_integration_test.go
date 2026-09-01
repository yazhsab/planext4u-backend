//go:build integration

package wallet

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresWalletFIFORefundReferralAndRewardControls(t *testing.T) {
	databaseURL := os.Getenv("WALLET_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("WALLET_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS wallet CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS wallet CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/wallet/000001_wallet.up.sql",
		"../../migrations/wallet/000002_durable_allocations_referrals.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	policy := RewardPolicy{DailyDeviceCap: 20, Cooldown: time.Minute, ReferralSenderPoints: 50, ReferralRecipientPoints: 25, ReferralExpiry: 365 * 24 * time.Hour}
	program := Program{ReferralBaseURL: "https://planext4u.example/referral", RefillOffers: []RefillOffer{{ID: "refill-100", Country: "IN", Points: 100, Price: Money{AmountMinor: 10000, Currency: "INR"}, PaymentMethods: []string{"RAZORPAY"}, ExpiresAfter: 365 * 24 * time.Hour}}}
	service, err := NewPostgresService(pool, func() time.Time { return now }, policy, program)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: postgresWalletTenantOne, Country: "IN", CustomerID: postgresWalletCustomerOne}
	first, replay, err := service.Credit(scope, "wallet-credit-postgres-001", "PROMOTION", "campaign-postgres-001", 100, now.Add(2*time.Hour))
	if err != nil || replay || first.BalanceAfter != 100 {
		t.Fatalf("first credit=%#v replay=%t err=%v", first, replay, err)
	}
	second, replay, err := service.Credit(scope, "wallet-credit-postgres-002", "PROMOTION", "campaign-postgres-002", 100, now.Add(3*time.Hour))
	if err != nil || replay || second.BalanceAfter != 200 {
		t.Fatalf("second credit=%#v replay=%t err=%v", second, replay, err)
	}
	restarted, _ := NewPostgresService(pool, func() time.Time { return now }, policy, program)
	replayed, replay, err := restarted.Credit(scope, "wallet-credit-postgres-001", "PROMOTION", "campaign-postgres-001", 100, now.Add(2*time.Hour))
	if err != nil || !replay || replayed.ID != first.ID {
		t.Fatalf("credit replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	if _, _, err := restarted.Credit(scope, "wallet-credit-postgres-001", "PROMOTION", "campaign-postgres-001", 101, now.Add(2*time.Hour)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("credit conflict=%v", err)
	}
	debit, replay, err := restarted.Redeem(scope, "wallet-redeem-postgres-001", "checkout-postgres-001", 120)
	if err != nil || replay || debit.DeltaPoints != -120 || debit.BalanceAfter != 80 {
		t.Fatalf("debit=%#v replay=%t err=%v", debit, replay, err)
	}
	partial, replay, err := restarted.RefundDebit(scope, "wallet-refund-postgres-001", debit.ID, "return-postgres-001", 50)
	if err != nil || replay || partial.DeltaPoints != 50 || partial.BalanceAfter != 130 {
		t.Fatalf("partial refund=%#v replay=%t err=%v", partial, replay, err)
	}
	reversal, replay, err := restarted.ReverseDebit(scope, "wallet-reverse-postgres-001", debit.ID, "order-postgres-001")
	if err != nil || replay || reversal.DeltaPoints != 70 || reversal.BalanceAfter != 200 {
		t.Fatalf("reversal=%#v replay=%t err=%v", reversal, replay, err)
	}
	if _, _, err := restarted.ReverseDebit(scope, "wallet-reverse-postgres-002", debit.ID, "order-postgres-001"); !errors.Is(err, ErrAlreadyReversed) {
		t.Fatalf("duplicate full reversal=%v", err)
	}
	account, err := restarted.Account(scope)
	if err != nil || account.Balance != 200 || !Verify(account) {
		t.Fatalf("account=%#v err=%v", account, err)
	}

	short, _, err := restarted.Credit(scope, "wallet-expiry-postgres-001", "PROMOTION", "short-campaign-001", 30, now.Add(time.Minute))
	if err != nil || short.BalanceAfter != 230 {
		t.Fatalf("short credit=%#v err=%v", short, err)
	}
	now = now.Add(2 * time.Minute)
	expired, err := restarted.Expire(scope)
	if err != nil || len(expired) != 1 || expired[0].DeltaPoints != -30 {
		t.Fatalf("expired=%#v err=%v", expired, err)
	}

	owner := Scope{TenantID: postgresWalletTenantOne, Country: "IN", CustomerID: postgresWalletCustomerTwo}
	referred := Scope{TenantID: postgresWalletTenantOne, Country: "IN", CustomerID: postgresWalletCustomerThree}
	ownerExperience, err := restarted.Experience(owner)
	if err != nil || ownerExperience.Referral.Code == "" {
		t.Fatalf("owner experience=%#v err=%v", ownerExperience, err)
	}
	if _, err := restarted.Experience(referred); err != nil {
		t.Fatal(err)
	}
	profile, replay, err := restarted.ApplyReferral(referred, "wallet-referral-postgres-001", ownerExperience.Referral.Code)
	if err != nil || replay || profile.PendingCode != ownerExperience.Referral.Code {
		t.Fatalf("referral profile=%#v replay=%t err=%v", profile, replay, err)
	}
	profile, replay, err = restarted.ApplyReferral(referred, "wallet-referral-postgres-001", ownerExperience.Referral.Code)
	if err != nil || !replay || profile.PendingCode != ownerExperience.Referral.Code {
		t.Fatalf("referral replay=%#v replay=%t err=%v", profile, replay, err)
	}
	awards, err := restarted.ActivateReferral(referred, "purchase-postgres-001")
	if err != nil || len(awards) != 2 || Sum(awards) != 75 {
		t.Fatalf("referral awards=%#v err=%v", awards, err)
	}
	if _, err := restarted.ActivateReferral(referred, "purchase-postgres-001"); !errors.Is(err, ErrRewardNotEligible) {
		t.Fatalf("duplicate referral activation=%v", err)
	}
	ownerAccount, _ := restarted.Account(owner)
	referredAccount, _ := restarted.Account(referred)
	if ownerAccount.Balance != 50 || referredAccount.Balance != 25 {
		t.Fatalf("referral balances owner=%d referred=%d", ownerAccount.Balance, referredAccount.Balance)
	}

	reward, replay, err := restarted.RewardEngagement(scope, "wallet-engagement-postgres-01", "social-event-postgres-001", "device-postgres-001", 10, now.Add(24*time.Hour))
	if err != nil || replay || reward.DeltaPoints != 10 {
		t.Fatalf("engagement reward=%#v replay=%t err=%v", reward, replay, err)
	}
	if _, _, err := restarted.RewardEngagement(scope, "wallet-engagement-postgres-02", "social-event-postgres-002", "device-postgres-001", 10, now.Add(24*time.Hour)); !errors.Is(err, ErrRewardNotEligible) {
		t.Fatalf("engagement cooldown=%v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, err := restarted.RewardEngagement(scope, "wallet-engagement-postgres-03", "social-event-postgres-003", "device-postgres-001", 11, now.Add(24*time.Hour)); !errors.Is(err, ErrRewardNotEligible) {
		t.Fatalf("engagement daily cap=%v", err)
	}
	other := Scope{TenantID: postgresWalletTenantTwo, Country: "IN", CustomerID: postgresWalletCustomerOne}
	otherAccount, err := restarted.Account(other)
	if err != nil || otherAccount.Balance != 0 || len(otherAccount.Entries) != 0 {
		t.Fatalf("cross tenant account=%#v err=%v", otherAccount, err)
	}
}

const (
	postgresWalletTenantOne     = "ca6c19aa-b397-46aa-a532-742ba72968e0"
	postgresWalletTenantTwo     = "ce6b1801-64d5-42e9-8a46-b6df97a2b3b3"
	postgresWalletCustomerOne   = "74429fee-5aa2-4a27-bf61-d6d62b10984d"
	postgresWalletCustomerTwo   = "4f0139c4-972c-433c-8a93-1939b4f1cb43"
	postgresWalletCustomerThree = "59dcba2a-a47f-46a2-afb0-b3c05d6d2b89"
)
