package fulfillment

import (
	"sort"
	"strings"
)

func (service *Service) SeedSettlement(actor Actor, accountID, referenceID, kind string, gross Money) (LedgerEntry, error) {
	if !validActor(actor) || !hasAnyRole(actor, "SETTLEMENT_WORKER", "FINANCE", "SUPER_ADMIN") || !safeID(accountID) || !safeID(referenceID) || !safeID(kind) || gross.AmountMinor <= 0 || len(gross.Currency) != 3 {
		return LedgerEntry{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, entry := range service.ledger {
		if entry.ReferenceID == referenceID && entry.AccountID == accountID && entry.Kind == kind {
			return *entry, nil
		}
	}
	commission := gross.AmountMinor * service.configuration.CommissionBasisPoints / 10000
	tax := commission * service.configuration.TaxBasisPoints / 10000
	service.sequence++
	now := service.clock().UTC()
	value := LedgerEntry{ID: "ledger-entry-" + sequenceID(service.sequence), AccountID: accountID, ReferenceID: referenceID, Kind: kind, Gross: gross, Commission: Money{AmountMinor: commission, Currency: gross.Currency}, Tax: Money{AmountMinor: tax, Currency: gross.Currency}, Net: Money{AmountMinor: gross.AmountMinor - commission - tax, Currency: gross.Currency}, CalculationVersion: "settlement-v1", AvailableAt: now.Add(service.configuration.SettlementCooling), CreatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.ledger[value.ID] = &value
	return value, nil
}

func (service *Service) Ledger(actor Actor, accountID string) ([]LedgerEntry, error) {
	if !validActor(actor) || !hasAnyRole(actor, "RIDER", "VENDOR", "RESTAURANT_VENDOR", "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	if !hasAnyRole(actor, "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") && actor.Subject != accountID {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []LedgerEntry{}
	for _, value := range service.ledger {
		if value.AccountID == accountID && value.tenantID == actor.TenantID && value.country == actor.Country {
			values = append(values, *value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) RequestPayout(actor Actor, key string, entryIDs []string) (Payout, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "RIDER", "VENDOR", "RESTAURANT_VENDOR") || !validKey(key) || len(entryIDs) == 0 || len(entryIDs) > 500 {
		return Payout{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	fingerprint := digest(entryIDs)
	scope := idempotencyScope(actor, "payout-request", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Payout{}, false, ErrIdempotencyConflict
		}
		return clonePayout(*service.payouts[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	seen := map[string]bool{}
	amount := int64(0)
	currency := ""
	for _, entryID := range entryIDs {
		if seen[entryID] || service.paidEntries[entryID] {
			return Payout{}, false, ErrConflict
		}
		seen[entryID] = true
		entry := service.ledger[entryID]
		if entry == nil {
			return Payout{}, false, ErrNotFound
		}
		if entry.AccountID != actor.Subject || entry.tenantID != actor.TenantID || entry.country != actor.Country || entry.AvailableAt.After(now) {
			return Payout{}, false, ErrForbidden
		}
		if currency != "" && currency != entry.Net.Currency {
			return Payout{}, false, ErrConflict
		}
		currency, amount = entry.Net.Currency, amount+entry.Net.AmountMinor
	}
	service.sequence++
	value := &Payout{ID: "payout-" + sequenceID(service.sequence), AccountID: actor.Subject, Revision: 1, Amount: Money{AmountMinor: amount, Currency: currency}, Status: "PENDING_REVIEW", EntryIDs: append([]string(nil), entryIDs...), CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.payouts[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return clonePayout(*value), false, nil
}

func (service *Service) ApprovePayout(actor Actor, key, payoutID string, revision int64, reason string) (Payout, bool, error) {
	if !validActor(actor) || !hasRole(actor, "FINANCE") || !actor.MFAVerified || !validKey(key) || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return Payout{}, false, ErrMFARequired
		}
		return Payout{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.payouts[payoutID]
	if value == nil {
		return Payout{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return Payout{}, false, ErrForbidden
	}
	fingerprint := digest(struct {
		Revision int64
		Reason   string
	}{revision, reason})
	scope := idempotencyScope(actor, "payout-approve:"+payoutID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Payout{}, false, ErrIdempotencyConflict
		}
		return clonePayout(*value), true, nil
	}
	if value.Revision != revision {
		return Payout{}, false, ErrConflict
	}
	switch value.Status {
	case "PENDING_REVIEW":
		value.Status, value.FirstApproverID = "FIRST_APPROVED", actor.Subject
	case "FIRST_APPROVED":
		if value.FirstApproverID == actor.Subject {
			return Payout{}, false, ErrForbidden
		}
		value.Status, value.SecondApproverID = "APPROVED", actor.Subject
	default:
		return Payout{}, false, ErrInvalidTransition
	}
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	service.recordAuditLocked(actor, "PAYOUT_APPROVED", "PAYOUT", payoutID, reason)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return clonePayout(*value), false, nil
}

func (service *Service) ExecutePayout(actor Actor, key, payoutID string, revision int64, providerReference string, success bool) (Payout, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "PAYOUT_WORKER", "FINANCE") || !validKey(key) || !safeID(providerReference) {
		return Payout{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.payouts[payoutID]
	if value == nil {
		return Payout{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return Payout{}, false, ErrForbidden
	}
	fingerprint := digest(struct {
		Revision  int64
		Reference string
		Success   bool
	}{revision, providerReference, success})
	scope := idempotencyScope(actor, "payout-execute:"+payoutID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Payout{}, false, ErrIdempotencyConflict
		}
		return clonePayout(*value), true, nil
	}
	if value.Revision != revision || (value.Status != "APPROVED" && value.Status != "FAILED") {
		return Payout{}, false, ErrInvalidTransition
	}
	if value.ProviderReference != "" && value.ProviderReference != providerReference {
		return Payout{}, false, ErrConflict
	}
	value.ProviderReference = providerReference
	value.AttemptCount++
	value.Revision++
	value.UpdatedAt = service.clock().UTC()
	if success {
		value.Status = "PAID"
		for _, entryID := range value.EntryIDs {
			service.paidEntries[entryID] = true
		}
	} else {
		value.Status = "FAILED"
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return clonePayout(*value), false, nil
}

func (service *Service) Payouts(actor Actor, accountID string) ([]Payout, error) {
	if !validActor(actor) || (!hasAnyRole(actor, "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") && actor.Subject != accountID) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []Payout{}
	for _, value := range service.payouts {
		if value.AccountID == accountID && value.tenantID == actor.TenantID && value.country == actor.Country {
			values = append(values, clonePayout(*value))
		}
	}
	return values, nil
}

func (service *Service) Reconcile(actor Actor, accountID string) (Reconciliation, error) {
	if !validActor(actor) || !hasAnyRole(actor, "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		if !actor.MFAVerified {
			return Reconciliation{}, ErrMFARequired
		}
		return Reconciliation{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := Reconciliation{AccountID: accountID, CalculationVersion: "settlement-v1"}
	for _, entry := range service.ledger {
		if entry.AccountID == accountID && entry.tenantID == actor.TenantID && entry.country == actor.Country {
			result.LedgerNetMinor += entry.Net.AmountMinor
			result.Currency = entry.Net.Currency
		}
	}
	for _, payout := range service.payouts {
		if payout.AccountID != accountID || payout.tenantID != actor.TenantID || payout.country != actor.Country {
			continue
		}
		if payout.Status == "PAID" {
			result.PaidMinor += payout.Amount.AmountMinor
		} else if payout.Status != "FAILED" {
			result.PendingMinor += payout.Amount.AmountMinor
		}
	}
	result.VarianceMinor = result.LedgerNetMinor - result.PaidMinor - result.PendingMinor
	return result, nil
}

func clonePayout(value Payout) Payout {
	value.EntryIDs = append([]string(nil), value.EntryIDs...)
	return value
}
