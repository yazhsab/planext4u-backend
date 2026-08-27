package commerce

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testProvider struct {
	mu       sync.RWMutex
	variants map[string]VariantSnapshot
}

func (provider *testProvider) Resolve(_ context.Context, _ Scope, id string) (VariantSnapshot, error) {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	value, exists := provider.variants[id]
	if !exists {
		return VariantSnapshot{}, ErrVariantNotFound
	}
	return value, nil
}

func (provider *testProvider) setPrice(id string, amount int64) {
	provider.mu.Lock()
	value := provider.variants[id]
	value.UnitPrice.AmountMinor = amount
	provider.variants[id] = value
	provider.mu.Unlock()
}

func TestCartUsesServerPriceAndRepricesExistingLines(t *testing.T) {
	t.Parallel()
	provider := syntheticProvider()
	service, _ := NewService(provider, fixedClock)
	scope := syntheticScope()

	first, replay, err := service.Change(context.Background(), scope, "idem-cart-command-0001", 0, "variant-oil-1l", 2)
	if err != nil || replay || first.Total.AmountMinor != 90000 || first.Items[0].UnitPrice.AmountMinor != 45000 || first.Revision != 1 {
		t.Fatalf("first change = %#v replay=%v err=%v", first, replay, err)
	}
	provider.setPrice("variant-oil-1l", 47500)
	second, _, err := service.Change(context.Background(), scope, "idem-cart-command-0002", 1, "variant-rice-5kg", 1)
	if err != nil || second.Total.AmountMinor != 155000 || second.PricingStatus != "REPRICED" || !second.Items[0].PriceChanged {
		t.Fatalf("repriced cart = %#v err=%v", second, err)
	}
	if !contains(second.AllowedActions, "CHECKOUT") {
		t.Fatalf("allowed actions = %v", second.AllowedActions)
	}
}

func TestCartIdempotencyReplayConflictAndRevision(t *testing.T) {
	t.Parallel()
	service, _ := NewService(syntheticProvider(), fixedClock)
	scope := syntheticScope()
	first, _, _ := service.Change(context.Background(), scope, "idem-cart-command-0003", 0, "variant-oil-1l", 1)
	replayed, replay, err := service.Change(context.Background(), scope, "idem-cart-command-0003", 0, "variant-oil-1l", 1)
	if err != nil || !replay || replayed.Revision != first.Revision {
		t.Fatalf("replay = %#v %v %v", replayed, replay, err)
	}
	if _, _, err := service.Change(context.Background(), scope, "idem-cart-command-0003", 1, "variant-oil-1l", 2); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	if _, _, err := service.Change(context.Background(), scope, "idem-cart-command-0004", 0, "variant-oil-1l", 2); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision conflict = %v", err)
	}
}

func TestCartRejectsUnavailableAndExcessQuantity(t *testing.T) {
	t.Parallel()
	service, _ := NewService(syntheticProvider(), fixedClock)
	scope := syntheticScope()
	if _, _, err := service.Change(context.Background(), scope, "idem-cart-command-0005", 0, "variant-unavailable", 1); !errors.Is(err, ErrVariantUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	if _, _, err := service.Change(context.Background(), scope, "idem-cart-command-0006", 0, "variant-oil-1l", 6); !errors.Is(err, ErrQuantityUnavailable) {
		t.Fatalf("quantity error = %v", err)
	}
}

func syntheticProvider() *testProvider {
	return &testProvider{variants: map[string]VariantSnapshot{
		"variant-oil-1l":      {VariantID: "variant-oil-1l", ItemID: "item-sesame-oil", ItemName: "Cold-pressed sesame oil", VariantName: "1L", UnitPrice: Money{AmountMinor: 45000, Currency: "INR"}, Available: true, Stock: 12, MaxPerOrder: 5},
		"variant-rice-5kg":    {VariantID: "variant-rice-5kg", ItemID: "item-rice", ItemName: "Local ponni rice", VariantName: "5kg", UnitPrice: Money{AmountMinor: 60000, Currency: "INR"}, Available: true, Stock: 20, MaxPerOrder: 4},
		"variant-unavailable": {VariantID: "variant-unavailable", ItemID: "item-old", ItemName: "Unavailable item", VariantName: "Standard", UnitPrice: Money{AmountMinor: 1000, Currency: "INR"}, Available: false, Stock: 0, MaxPerOrder: 1},
	}}
}

func syntheticScope() Scope {
	return Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
}
func fixedClock() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }
func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
