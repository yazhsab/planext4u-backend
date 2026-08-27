package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type replayRecord struct {
	fingerprint string
	value       Reservation
}

type Service struct {
	clock        func() time.Time
	mu           sync.Mutex
	stock        map[string]int
	reservations map[string]Reservation
	requests     map[string]replayRecord
}

func NewService(seed []SeedStock, clock func() time.Time) (*Service, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	service := &Service{
		clock: clock, stock: map[string]int{}, reservations: map[string]Reservation{}, requests: map[string]replayRecord{},
	}
	for _, item := range seed {
		if !validScope(item.Scope) || !safeID(item.VariantID) || item.Quantity < 0 {
			return nil, ErrInvalidRequest
		}
		key := stockKey(item.Scope, item.VariantID)
		if _, exists := service.stock[key]; exists {
			return nil, ErrInvalidRequest
		}
		service.stock[key] = item.Quantity
	}
	return service, nil
}

func (service *Service) Available(scope Scope, variantID string) (int, error) {
	if !validScope(scope) || !safeID(variantID) {
		return 0, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.stock[stockKey(scope, variantID)], nil
}

func (service *Service) Reserve(scope Scope, idempotencyKey, orderReference string, lines []Line, expiresAt time.Time) (Reservation, bool, error) {
	now := service.clock().UTC()
	normalized, fingerprint, err := normalizeRequest(scope, idempotencyKey, orderReference, lines, expiresAt, now)
	if err != nil {
		return Reservation{}, false, err
	}
	requestKey := scopeKey(scope) + "\x00" + idempotencyKey
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked(now)
	if replay, exists := service.requests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Reservation{}, false, ErrIdempotencyConflict
		}
		return cloneReservation(replay.value), true, nil
	}
	for _, line := range normalized {
		if service.stock[stockKey(scope, line.VariantID)] < line.Quantity {
			return Reservation{}, false, ErrInsufficientStock
		}
	}
	for _, line := range normalized {
		service.stock[stockKey(scope, line.VariantID)] -= line.Quantity
	}
	digest := sha256.Sum256([]byte(requestKey))
	value := Reservation{
		ID: "reservation-" + hex.EncodeToString(digest[:8]), TenantID: scope.TenantID, Country: scope.Country,
		OrderReference: orderReference, State: StateReserved, Lines: normalized, CreatedAt: now, ExpiresAt: expiresAt.UTC(), UpdatedAt: now,
	}
	service.reservations[value.ID] = cloneReservation(value)
	service.requests[requestKey] = replayRecord{fingerprint: fingerprint, value: cloneReservation(value)}
	return cloneReservation(value), false, nil
}

func (service *Service) Commit(scope Scope, reservationID string) (Reservation, error) {
	return service.transition(scope, reservationID, StateCommitted)
}

func (service *Service) Release(scope Scope, reservationID string) (Reservation, error) {
	return service.transition(scope, reservationID, StateReleased)
}

func (service *Service) transition(scope Scope, reservationID string, target ReservationState) (Reservation, error) {
	if !validScope(scope) || !safeID(reservationID) || (target != StateCommitted && target != StateReleased) {
		return Reservation{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.clock().UTC()
	service.expireLocked(now)
	value, exists := service.reservations[reservationID]
	if !exists || value.TenantID != scope.TenantID || value.Country != scope.Country {
		return Reservation{}, ErrReservationNotFound
	}
	if value.State == target {
		return cloneReservation(value), nil
	}
	if value.State != StateReserved {
		return Reservation{}, ErrInvalidTransition
	}
	if target == StateReleased {
		service.restoreLocked(scope, value.Lines)
	}
	value.State = target
	value.UpdatedAt = now
	service.reservations[reservationID] = value
	return cloneReservation(value), nil
}

func (service *Service) Expire() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.expireLocked(service.clock().UTC())
}

func (service *Service) expireLocked(now time.Time) int {
	expired := 0
	for id, value := range service.reservations {
		if value.State != StateReserved || now.Before(value.ExpiresAt) {
			continue
		}
		scope := Scope{TenantID: value.TenantID, Country: value.Country}
		service.restoreLocked(scope, value.Lines)
		value.State = StateReleased
		value.UpdatedAt = now
		service.reservations[id] = value
		expired++
	}
	return expired
}

func (service *Service) restoreLocked(scope Scope, lines []Line) {
	for _, line := range lines {
		service.stock[stockKey(scope, line.VariantID)] += line.Quantity
	}
}

func normalizeRequest(scope Scope, idempotencyKey, orderReference string, lines []Line, expiresAt, now time.Time) ([]Line, string, error) {
	if !validScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderReference) || len(lines) == 0 || len(lines) > 100 || !expiresAt.After(now) || expiresAt.After(now.Add(30*time.Minute)) {
		return nil, "", ErrInvalidRequest
	}
	quantities := map[string]int{}
	for _, line := range lines {
		if !safeID(line.VariantID) || line.Quantity < 1 || line.Quantity > 999 {
			return nil, "", ErrInvalidRequest
		}
		quantities[line.VariantID] += line.Quantity
		if quantities[line.VariantID] > 999 {
			return nil, "", ErrInvalidRequest
		}
	}
	ids := make([]string, 0, len(quantities))
	for id := range quantities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	normalized := make([]Line, 0, len(ids))
	parts := []string{scopeKey(scope), idempotencyKey, orderReference, expiresAt.UTC().Format(time.RFC3339Nano)}
	for _, id := range ids {
		normalized = append(normalized, Line{VariantID: id, Quantity: quantities[id]})
		parts = append(parts, fmt.Sprintf("%s:%d", id, quantities[id]))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return normalized, hex.EncodeToString(digest[:]), nil
}

func validScope(scope Scope) bool {
	return safeID(scope.TenantID) && len(scope.Country) == 2 && strings.ToUpper(scope.Country) == scope.Country
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

func scopeKey(scope Scope) string                   { return scope.TenantID + "\x00" + scope.Country }
func stockKey(scope Scope, variantID string) string { return scopeKey(scope) + "\x00" + variantID }

func cloneReservation(value Reservation) Reservation {
	value.Lines = append([]Line(nil), value.Lines...)
	return value
}
