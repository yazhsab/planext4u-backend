package commerce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type idempotencyRecord struct {
	fingerprint string
	result      Cart
}

type Service struct {
	provider SnapshotProvider
	clock    func() time.Time
	mu       sync.RWMutex
	carts    map[string]Cart
	requests map[string]idempotencyRecord
}

func NewService(provider SnapshotProvider, clock func() time.Time) (*Service, error) {
	if provider == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{provider: provider, clock: clock, carts: map[string]Cart{}, requests: map[string]idempotencyRecord{}}, nil
}

func (service *Service) Get(scope Scope) (Cart, error) {
	if !validScope(scope) {
		return Cart{}, ErrInvalidRequest
	}
	key := scopeKey(scope)
	service.mu.RLock()
	value, exists := service.carts[key]
	service.mu.RUnlock()
	if !exists {
		return emptyCart(scope, service.clock().UTC()), nil
	}
	return cloneCart(value), nil
}

func (service *Service) Change(ctx context.Context, scope Scope, idempotencyKey string, expectedRevision int64, variantID string, quantity int) (Cart, bool, error) {
	if !validScope(scope) || !safeID(variantID) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || expectedRevision < 0 || quantity < 0 || quantity > 999 {
		return Cart{}, false, ErrInvalidRequest
	}
	key := scopeKey(scope)
	requestKey := key + "\x00" + idempotencyKey
	fingerprint := fmt.Sprintf("%d\x00%s\x00%d", expectedRevision, variantID, quantity)

	service.mu.RLock()
	if replay, exists := service.requests[requestKey]; exists {
		service.mu.RUnlock()
		if replay.fingerprint != fingerprint {
			return Cart{}, false, ErrIdempotencyConflict
		}
		return cloneCart(replay.result), true, nil
	}
	current, exists := service.carts[key]
	service.mu.RUnlock()
	if !exists {
		current = emptyCart(scope, service.clock().UTC())
	}
	if current.Revision != expectedRevision {
		return Cart{}, false, ErrRevisionConflict
	}

	quantities := make(map[string]int, len(current.Items)+1)
	previousPrices := make(map[string]Money, len(current.Items))
	for _, line := range current.Items {
		quantities[line.VariantID] = line.Quantity
		previousPrices[line.VariantID] = line.UnitPrice
	}
	if quantity == 0 {
		delete(quantities, variantID)
	} else {
		quantities[variantID] = quantity
	}

	variantIDs := make([]string, 0, len(quantities))
	for id := range quantities {
		variantIDs = append(variantIDs, id)
	}
	sort.Strings(variantIDs)
	lines := make([]CartLine, 0, len(variantIDs))
	currency := "INR"
	var subtotal int64
	pricingStatus := "CURRENT"
	allAvailable := true
	for _, id := range variantIDs {
		snapshot, err := service.provider.Resolve(ctx, scope, id)
		if err != nil {
			if id == variantID {
				return Cart{}, false, ErrVariantNotFound
			}
			return Cart{}, false, ErrVariantUnavailable
		}
		if !validSnapshot(snapshot, id) {
			return Cart{}, false, ErrVariantUnavailable
		}
		requested := quantities[id]
		available := snapshot.Available && requested <= snapshot.Stock && requested <= snapshot.MaxPerOrder
		if id == variantID && quantity > 0 && !snapshot.Available {
			return Cart{}, false, ErrVariantUnavailable
		}
		if id == variantID && quantity > 0 && !available {
			return Cart{}, false, ErrQuantityUnavailable
		}
		if len(lines) == 0 {
			currency = snapshot.UnitPrice.Currency
		} else if currency != snapshot.UnitPrice.Currency {
			return Cart{}, false, ErrVariantUnavailable
		}
		lineAmount := snapshot.UnitPrice.AmountMinor * int64(requested)
		previous, hadPrevious := previousPrices[id]
		priceChanged := hadPrevious && previous != snapshot.UnitPrice
		if priceChanged {
			pricingStatus = "REPRICED"
		}
		if !available {
			allAvailable = false
		}
		lines = append(lines, CartLine{
			VariantID: id, ItemID: snapshot.ItemID, ItemName: snapshot.ItemName, VariantName: snapshot.VariantName,
			MediaRef: snapshot.MediaRef, Quantity: requested, UnitPrice: snapshot.UnitPrice,
			LineTotal: Money{AmountMinor: lineAmount, Currency: currency}, Available: available, PriceChanged: priceChanged,
		})
		subtotal += lineAmount
	}

	zero := Money{Currency: currency}
	next := Cart{
		ID: current.ID, Revision: current.Revision + 1, Items: lines,
		Subtotal: Money{AmountMinor: subtotal, Currency: currency}, Discount: zero, Tax: zero, Fees: zero,
		Total: Money{AmountMinor: subtotal, Currency: currency}, PricingStatus: pricingStatus,
		AllowedActions: allowedActions(len(lines), allAvailable), UpdatedAt: service.clock().UTC(),
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, replayExists := service.requests[requestKey]; replayExists {
		if replay.fingerprint != fingerprint {
			return Cart{}, false, ErrIdempotencyConflict
		}
		return cloneCart(replay.result), true, nil
	}
	latest, latestExists := service.carts[key]
	latestRevision := int64(0)
	if latestExists {
		latestRevision = latest.Revision
	}
	if latestRevision != expectedRevision {
		return Cart{}, false, ErrRevisionConflict
	}
	service.carts[key] = cloneCart(next)
	service.requests[requestKey] = idempotencyRecord{fingerprint: fingerprint, result: cloneCart(next)}
	return cloneCart(next), false, nil
}

func emptyCart(scope Scope, now time.Time) Cart {
	digest := sha256.Sum256([]byte(scopeKey(scope)))
	zero := Money{Currency: "INR"}
	return Cart{ID: "cart-" + hex.EncodeToString(digest[:8]), Items: []CartLine{}, Subtotal: zero, Discount: zero, Tax: zero, Fees: zero, Total: zero,
		PricingStatus: "CURRENT", AllowedActions: []string{"BROWSE"}, UpdatedAt: now}
}

func allowedActions(itemCount int, allAvailable bool) []string {
	result := []string{"BROWSE"}
	if itemCount > 0 {
		result = append(result, "EDIT_ITEMS", "REMOVE_ITEMS")
		if allAvailable {
			result = append(result, "CHECKOUT")
		}
	}
	return result
}

func validScope(scope Scope) bool {
	return safeID(scope.TenantID) && safeID(scope.CustomerID) && len(scope.Country) == 2 && scope.Country == strings.ToUpper(scope.Country)
}

func validSnapshot(snapshot VariantSnapshot, expectedID string) bool {
	return snapshot.VariantID == expectedID && safeID(snapshot.ItemID) && strings.TrimSpace(snapshot.ItemName) != "" && strings.TrimSpace(snapshot.VariantName) != "" &&
		snapshot.UnitPrice.AmountMinor >= 0 && len(snapshot.UnitPrice.Currency) == 3 && snapshot.UnitPrice.Currency == strings.ToUpper(snapshot.UnitPrice.Currency) &&
		snapshot.Stock >= 0 && snapshot.MaxPerOrder > 0 && snapshot.MaxPerOrder <= 999
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

func cloneCart(value Cart) Cart {
	value.Items = append([]CartLine(nil), value.Items...)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}
