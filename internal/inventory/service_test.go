package inventory

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReservationIsAtomicUnderStockRace(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	service, _ := NewService([]SeedStock{{Scope: testScope(), VariantID: "variant-last-one", Quantity: 1}}, clock.Now)
	var successes atomic.Int32
	var insufficient atomic.Int32
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, _, err := service.Reserve(testScope(), "idem-stock-race-000"+string(rune('1'+index)), "order-race-00"+string(rune('1'+index)), []Line{{VariantID: "variant-last-one", Quantity: 1}}, clock.Now().Add(10*time.Minute))
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, ErrInsufficientStock) {
				insufficient.Add(1)
			}
		}(index)
	}
	close(start)
	group.Wait()
	if successes.Load() != 1 || insufficient.Load() != 1 {
		t.Fatalf("successes=%d insufficient=%d", successes.Load(), insufficient.Load())
	}
}

func TestReservationExpiryAndReleaseRestoreStockOnce(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	service, _ := NewService([]SeedStock{{Scope: testScope(), VariantID: "variant-oil", Quantity: 5}}, clock.Now)
	reservation, replay, err := service.Reserve(testScope(), "idem-expiry-command-0001", "order-expiry-001", []Line{{VariantID: "variant-oil", Quantity: 3}}, clock.Now().Add(5*time.Minute))
	if err != nil || replay {
		t.Fatalf("reserve err=%v replay=%v", err, replay)
	}
	if available, _ := service.Available(testScope(), "variant-oil"); available != 2 {
		t.Fatalf("available after reserve = %d", available)
	}
	clock.Advance(5 * time.Minute)
	if service.Expire() != 1 || service.Expire() != 0 {
		t.Fatal("reservation did not expire exactly once")
	}
	if available, _ := service.Available(testScope(), "variant-oil"); available != 5 {
		t.Fatalf("available after expiry = %d", available)
	}
	if _, err := service.Commit(testScope(), reservation.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("commit expired reservation = %v", err)
	}
}

func TestReservationReplayAndCompensation(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	service, _ := NewService([]SeedStock{{Scope: testScope(), VariantID: "variant-rice", Quantity: 4}}, clock.Now)
	deadline := clock.Now().Add(10 * time.Minute)
	first, _, _ := service.Reserve(testScope(), "idem-replay-command-0001", "order-replay-001", []Line{{VariantID: "variant-rice", Quantity: 2}}, deadline)
	replay, wasReplay, err := service.Reserve(testScope(), "idem-replay-command-0001", "order-replay-001", []Line{{VariantID: "variant-rice", Quantity: 2}}, deadline)
	if err != nil || !wasReplay || replay.ID != first.ID {
		t.Fatalf("replay=%#v wasReplay=%v err=%v", replay, wasReplay, err)
	}
	if _, _, err := service.Reserve(testScope(), "idem-replay-command-0001", "order-replay-001", []Line{{VariantID: "variant-rice", Quantity: 3}}, deadline); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	if _, err := service.Release(testScope(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(testScope(), first.ID); err != nil {
		t.Fatalf("idempotent release = %v", err)
	}
	if available, _ := service.Available(testScope(), "variant-rice"); available != 4 {
		t.Fatalf("available after compensation = %d", available)
	}
}

func TestCommittedInventoryRestockIsBoundedAndIdempotent(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	service, _ := NewService([]SeedStock{{Scope: testScope(), VariantID: "variant-return", Quantity: 5}}, clock.Now)
	reservation, _, _ := service.Reserve(testScope(), "idem-restock-reserve-0001", "order-restock-001", []Line{{VariantID: "variant-return", Quantity: 3}}, clock.Now().Add(10*time.Minute))
	if _, err := service.Commit(testScope(), reservation.ID); err != nil {
		t.Fatal(err)
	}
	restocked, replayed, err := service.Restock(testScope(), "idem-restock-command-0001", reservation.ID, []Line{{VariantID: "variant-return", Quantity: 2}})
	if err != nil || replayed || len(restocked.RestockedLines) != 1 || restocked.RestockedLines[0].Quantity != 2 {
		t.Fatalf("restocked=%#v replayed=%v err=%v", restocked, replayed, err)
	}
	if available, _ := service.Available(testScope(), "variant-return"); available != 4 {
		t.Fatalf("available after restock=%d", available)
	}
	if _, replayed, err = service.Restock(testScope(), "idem-restock-command-0001", reservation.ID, []Line{{VariantID: "variant-return", Quantity: 2}}); err != nil || !replayed {
		t.Fatalf("replay replayed=%v err=%v", replayed, err)
	}
	if _, _, err = service.Restock(testScope(), "idem-restock-command-0002", reservation.ID, []Line{{VariantID: "variant-return", Quantity: 2}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("over-restock=%v", err)
	}
	if available, _ := service.Available(testScope(), "variant-return"); available != 4 {
		t.Fatalf("stock changed on duplicate=%d", available)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock          { return &testClock{now: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)} }
func (clock *testClock) Now() time.Time { clock.mu.Lock(); defer clock.mu.Unlock(); return clock.now }
func (clock *testClock) Advance(value time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(value)
	clock.mu.Unlock()
}
func testScope() Scope { return Scope{TenantID: "tenant-synthetic-001", Country: "IN"} }
