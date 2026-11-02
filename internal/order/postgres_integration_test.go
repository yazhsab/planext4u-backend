//go:build integration

package order

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresOrderLifecycleReplayNotificationsAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("ORDER_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("ORDER_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS ordering CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS ordering CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/ordering/000001_ordering.up.sql",
		"../../migrations/ordering/000002_durable_lifecycle.up.sql",
		"../../migrations/ordering/000003_vendor_work_intents.up.sql",
		"../../migrations/ordering/000004_vendor_work_cancellations.up.sql",
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
	notifier := &postgresOrderNotifier{failures: 1}
	service, err := NewPostgresServiceWithNotifier(pool, func() time.Time { return now }, notifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: postgresOrderTenantOne, Country: "IN", CustomerID: postgresOrderCustomerOne}
	created, replay, err := service.Create(scope, "order-create-postgres-001", orderSnapshot(), true)
	if err != nil || replay || created.Status != StatusPlaced || created.Revision != 1 {
		t.Fatalf("created=%#v replay=%t err=%v", created, replay, err)
	}
	restarted, _ := NewPostgresServiceWithNotifier(pool, func() time.Time { return now }, notifier)
	replayed, replay, err := restarted.Create(scope, "order-create-postgres-001", orderSnapshot(), true)
	if err != nil || !replay || replayed.ID != created.ID || len(replayed.Timeline) != 1 {
		t.Fatalf("create replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	if _, _, err := restarted.Create(scope, "order-create-postgres-001", orderSnapshot(), false); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("create conflict=%v", err)
	}

	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index, reason := range []string{"Vendor accepted one", "Vendor accepted two"} {
		wait.Add(1)
		go func(index int, reason string) {
			defer wait.Done()
			_, _, callErr := restarted.Transition(scope, "order-concurrent-00000"+string(rune('1'+index)), created.ID, 1, StatusAccepted, "VENDOR", reason)
			results <- callErr
		}(index, reason)
	}
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for callErr := range results {
		switch {
		case callErr == nil:
			succeeded++
		case errors.Is(callErr, ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent transition error: %v", callErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent transitions succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	loaded, err := restarted.Get(scope, created.ID)
	if err != nil || loaded.Status != StatusAccepted || loaded.Revision != 2 || len(loaded.Timeline) != 2 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	listed, err := restarted.List(scope)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	otherScope := Scope{TenantID: postgresOrderTenantTwo, Country: "IN", CustomerID: postgresOrderCustomerOne}
	if _, err := restarted.Get(otherScope, created.ID); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("cross tenant get=%v", err)
	}

	if delivered, err := restarted.ProcessNotifications(ctx, 10); err != nil || delivered != 1 {
		// The create intent fails and is backed off; the transition intent is
		// independent and succeeds in the same drain.
		t.Fatalf("first notification drain=%d err=%v", delivered, err)
	}
	pending, err := restarted.PendingNotifications(scope)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 1 || pending[0].LastError != "NOTIFICATION_PROVIDER_FAILED" {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if current, err := restarted.Get(scope, created.ID); err != nil || current.Status != StatusAccepted {
		t.Fatalf("notification failure changed order=%#v err=%v", current, err)
	}
	now = now.Add(3 * time.Second)
	if delivered, err := restarted.ProcessNotifications(ctx, 10); err != nil || delivered != 1 {
		t.Fatalf("retry notification drain=%d err=%v", delivered, err)
	}
	pending, _ = restarted.PendingNotifications(scope)
	if len(pending) != 0 || notifier.CallCount() != 3 {
		t.Fatalf("pending=%#v calls=%d", pending, notifier.CallCount())
	}
	var timelineCount, intentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.timeline_events WHERE order_id=$1`, created.ID).Scan(&timelineCount); err != nil || timelineCount != 2 {
		t.Fatalf("timeline rows=%d err=%v", timelineCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.notification_intents WHERE order_id=$1`, created.ID).Scan(&intentCount); err != nil || intentCount != 2 {
		t.Fatalf("notification rows=%d err=%v", intentCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_intents WHERE order_id=$1`, created.ID).Scan(&intentCount); err != nil || intentCount != 1 {
		t.Fatalf("placed-order vendor intents=%d err=%v", intentCount, err)
	}
	if _, err := restarted.ClaimVendorWorkIntents(ctx, 101); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unbounded vendor claim=%v", err)
	}
	claims, err := restarted.ClaimVendorWorkIntents(ctx, 1)
	if err != nil || len(claims) != 1 || claims[0].OrderID != created.ID || claims[0].OrderRevision != 1 || len(claims[0].Lines) != 1 || claims[0].Attempts != 1 {
		t.Fatalf("initial vendor claim=%#v err=%v", claims, err)
	}
	if again, err := restarted.ClaimVendorWorkIntents(ctx, 1); err != nil || len(again) != 0 {
		t.Fatalf("leased vendor claim repeated=%#v err=%v", again, err)
	}
	if err := restarted.RetryVendorWorkIntent(ctx, claims[0].ID, claims[0].ClaimToken, "SUPPLY_UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.AckVendorWorkIntent(ctx, claims[0].ID, claims[0].ClaimToken); !errors.Is(err, ErrVendorWorkClaimLost) {
		t.Fatalf("released claim acknowledged=%v", err)
	}
	if again, err := restarted.ClaimVendorWorkIntents(ctx, 1); err != nil || len(again) != 0 {
		t.Fatalf("vendor backoff skipped=%#v err=%v", again, err)
	}
	now = now.Add(3 * time.Second)
	reclaimed, err := restarted.ClaimVendorWorkIntents(ctx, 1)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != claims[0].ID || reclaimed[0].ClaimToken == claims[0].ClaimToken || reclaimed[0].Attempts != 2 {
		t.Fatalf("vendor retry claim=%#v err=%v", reclaimed, err)
	}
	if err := restarted.AckVendorWorkIntent(ctx, claims[0].ID, claims[0].ClaimToken); !errors.Is(err, ErrVendorWorkClaimLost) {
		t.Fatalf("stale vendor claim acknowledged=%v", err)
	}
	if err := restarted.AckVendorWorkIntent(ctx, reclaimed[0].ID, reclaimed[0].ClaimToken); err != nil {
		t.Fatal(err)
	}
	if again, err := restarted.ClaimVendorWorkIntents(ctx, 1); err != nil || len(again) != 0 {
		t.Fatalf("delivered vendor claim repeated=%#v err=%v", again, err)
	}

	pendingSnapshot := orderSnapshot()
	pendingSnapshot.PaymentID = "payment-pending-002"
	pendingOrder, replay, err := restarted.Create(scope, "order-payment-pending-002", pendingSnapshot, false)
	if err != nil || replay || pendingOrder.Status != StatusPendingPayment {
		t.Fatalf("pending order=%#v replay=%t err=%v", pendingOrder, replay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_intents WHERE order_id=$1`, pendingOrder.ID).Scan(&intentCount); err != nil || intentCount != 0 {
		t.Fatalf("pending order vendor intents=%d err=%v", intentCount, err)
	}
	placed, replay, err := restarted.Transition(scope, "order-payment-capture-002", pendingOrder.ID, 1, StatusPlaced, "PLATFORM", "Payment captured")
	if err != nil || replay || placed.Revision != 2 {
		t.Fatalf("captured order=%#v replay=%t err=%v", placed, replay, err)
	}
	if _, replay, err := restarted.Transition(scope, "order-payment-capture-002", pendingOrder.ID, 1, StatusPlaced, "PLATFORM", "Payment captured"); err != nil || !replay {
		t.Fatalf("capture replay=%t err=%v", replay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_intents WHERE order_id=$1 AND order_revision=2`, pendingOrder.ID).Scan(&intentCount); err != nil || intentCount != 1 {
		t.Fatalf("captured order vendor intents=%d err=%v", intentCount, err)
	}

	multi := orderSnapshot()
	first := multi.Lines[0]
	first.Quantity, first.LineTotal.AmountMinor, first.TaxMinor, first.DiscountMinor, first.CommissionMinor = 1, 10000, 400, 900, 1200
	second := first
	second.VendorID, second.ItemID, second.VariantID = "vendor-local-002", "item-rice", "variant-rice"
	multi.Lines = []LineSnapshot{first, second}
	multi.PaymentID = "payment-multi-vendor-003"
	multiOrder, replay, err := restarted.Create(scope, "order-multi-vendor-003", multi, true)
	if err != nil || replay {
		t.Fatalf("multi-vendor order=%#v replay=%t err=%v", multiOrder, replay, err)
	}
	rows, err := pool.Query(ctx, `SELECT vendor_id,lines FROM ordering.vendor_work_intents WHERE order_id=$1 ORDER BY vendor_id`, multiOrder.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	perVendor := map[string][]LineSnapshot{}
	for rows.Next() {
		var vendorID string
		var payload []byte
		if err := rows.Scan(&vendorID, &payload); err != nil {
			t.Fatal(err)
		}
		var lines []LineSnapshot
		if err := json.Unmarshal(payload, &lines); err != nil {
			t.Fatal(err)
		}
		perVendor[vendorID] = lines
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(perVendor) != 2 || len(perVendor[first.VendorID]) != 1 || len(perVendor[second.VendorID]) != 1 || perVendor[first.VendorID][0].VariantID != first.VariantID || perVendor[second.VendorID][0].VariantID != second.VariantID {
		t.Fatalf("vendor line split=%#v", perVendor)
	}
}

type postgresOrderNotifier struct {
	mu       sync.Mutex
	failures int
	calls    int
}

func (notifier *postgresOrderNotifier) Send(_ context.Context, _ Notification) error {
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	notifier.calls++
	if notifier.failures > 0 {
		notifier.failures--
		return errors.New("synthetic notification failure")
	}
	return nil
}

func (notifier *postgresOrderNotifier) CallCount() int {
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	return notifier.calls
}

const (
	postgresOrderTenantOne   = "559d37a4-2458-43f8-886d-89d33aa8e482"
	postgresOrderTenantTwo   = "009523a1-4c7d-4530-a2d0-ece121f75b37"
	postgresOrderCustomerOne = "78b2e2e4-bc18-45af-89f9-b139aceab1e4"
)
