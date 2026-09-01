package fulfillment

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/order"
)

func (service *PostgresService) SeedSettlement(actor Actor, accountID, referenceID, kind string, gross Money) (LedgerEntry, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "SETTLEMENT_WORKER", "FINANCE", "SUPER_ADMIN") || !fulfillmentIsUUID(accountID) || !fulfillmentIsUUID(referenceID) || !safeID(kind) || gross.AmountMinor <= 0 || len(gross.Currency) != 3 {
		return LedgerEntry{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	policy, err := loadFulfillmentPolicy(ctx, service.pool, actor.TenantID, actor.Country)
	if err != nil {
		return LedgerEntry{}, err
	}
	commission, ok := settlementBasisPoints(gross.AmountMinor, policy.commissionBasisPoints)
	if !ok {
		return LedgerEntry{}, ErrInvalidRequest
	}
	tax, ok := settlementBasisPoints(commission, policy.taxBasisPoints)
	if !ok {
		return LedgerEntry{}, ErrInvalidRequest
	}
	return service.seedPostgresSettlement(actor, accountID, referenceID, kind, gross, commission, tax, "settlement:"+policy.version, policy.settlementCooling)
}

func (service *PostgresService) SeedOrderSettlement(actor Actor, accountID, referenceID, kind, vendorID string, snapshot order.CheckoutSnapshot) (LedgerEntry, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "SETTLEMENT_WORKER", "FINANCE", "SUPER_ADMIN") || !fulfillmentIsUUID(accountID) || !fulfillmentIsUUID(referenceID) || !safeID(kind) || !fulfillmentIsUUID(vendorID) || !safeID(snapshot.PricingPolicyVersion) || !safeID(snapshot.CommercialPolicyVersion) {
		return LedgerEntry{}, ErrForbidden
	}
	var grossMinor, commissionMinor int64
	currency := snapshot.Total.Currency
	for _, line := range snapshot.Lines {
		if line.VendorID != vendorID {
			continue
		}
		if line.LineTotal.Currency != currency || line.DiscountMinor < 0 || line.TaxMinor < 0 || line.CommissionMinor < 0 {
			return LedgerEntry{}, ErrInvalidRequest
		}
		var ok bool
		grossMinor, ok = settlementAdd(grossMinor, line.LineTotal.AmountMinor-line.DiscountMinor)
		if !ok {
			return LedgerEntry{}, ErrInvalidRequest
		}
		grossMinor, ok = settlementAdd(grossMinor, line.TaxMinor)
		if !ok {
			return LedgerEntry{}, ErrInvalidRequest
		}
		commissionMinor, ok = settlementAdd(commissionMinor, line.CommissionMinor)
		if !ok {
			return LedgerEntry{}, ErrInvalidRequest
		}
	}
	if grossMinor <= 0 || commissionMinor > grossMinor {
		return LedgerEntry{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	policy, err := loadFulfillmentPolicy(ctx, service.pool, actor.TenantID, actor.Country)
	if err != nil {
		return LedgerEntry{}, err
	}
	tax, ok := settlementBasisPoints(commissionMinor, policy.taxBasisPoints)
	if !ok || commissionMinor+tax > grossMinor {
		return LedgerEntry{}, ErrInvalidRequest
	}
	return service.seedPostgresSettlement(actor, accountID, referenceID, kind, Money{AmountMinor: grossMinor, Currency: currency}, commissionMinor, tax, "order:"+snapshot.PricingPolicyVersion+":"+snapshot.CommercialPolicyVersion, policy.settlementCooling)
}

func (service *PostgresService) seedPostgresSettlement(actor Actor, accountID, referenceID, kind string, gross Money, commission, tax int64, version string, cooling time.Duration) (LedgerEntry, error) {
	if gross.AmountMinor <= 0 || commission < 0 || tax < 0 || commission+tax > gross.AmountMinor {
		return LedgerEntry{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	now := service.Now()
	value := LedgerEntry{ID: fulfillmentUUID("ledger", accountID+":"+referenceID+":"+kind), AccountID: accountID, ReferenceID: referenceID, Kind: kind, Gross: gross, Commission: Money{AmountMinor: commission, Currency: gross.Currency}, Tax: Money{AmountMinor: tax, Currency: gross.Currency}, Net: Money{AmountMinor: gross.AmountMinor - commission - tax, Currency: gross.Currency}, CalculationVersion: version, AvailableAt: now.Add(cooling), CreatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	_, err := service.pool.Exec(ctx, `INSERT INTO fulfillment.ledger_entries (id,tenant_id,country,account_identity_id,reference_id,kind,gross_minor,commission_minor,tax_minor,net_minor,currency,calculation_version,available_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (account_identity_id,reference_id,kind) DO NOTHING`, value.ID, actor.TenantID, actor.Country, accountID, referenceID, kind, gross.AmountMinor, commission, tax, value.Net.AmountMinor, gross.Currency, version, value.AvailableAt, now)
	if err != nil {
		return LedgerEntry{}, mapFulfillmentError(err)
	}
	return loadPostgresLedgerEntry(ctx, service.pool, actor.TenantID, actor.Country, accountID, referenceID, kind)
}

func (service *PostgresService) Ledger(actor Actor, accountID string) ([]LedgerEntry, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(accountID) || !hasAnyRole(actor, "RIDER", "VENDOR", "RESTAURANT_VENDOR", "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") || !hasAnyRole(actor, "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") && actor.Subject != accountID {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, ledgerSelect+` WHERE tenant_id=$1 AND country=$2 AND account_identity_id=$3 ORDER BY created_at DESC`, actor.TenantID, actor.Country, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []LedgerEntry{}
	for rows.Next() {
		value, err := scanPostgresLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) RequestPayout(actor Actor, key string, entryIDs []string) (Payout, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "RIDER", "VENDOR", "RESTAURANT_VENDOR") || !validKey(key) || len(entryIDs) == 0 || len(entryIDs) > 500 || !allFulfillmentUUIDs(entryIDs) {
		return Payout{}, false, ErrInvalidRequest
	}
	canonical := append([]string(nil), entryIDs...)
	sort.Strings(canonical)
	for index := 1; index < len(canonical); index++ {
		if canonical[index] == canonical[index-1] {
			return Payout{}, false, ErrConflict
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Payout{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fulfillmentCommandLock(ctx, tx, actor, "payout-request", key); err != nil {
		return Payout{}, false, err
	}
	fingerprint := digest(canonical)
	var replay Payout
	if found, err := loadFulfillmentReplay(ctx, tx, actor, "payout-request", key, fingerprint, &replay); err != nil {
		return Payout{}, false, err
	} else if found {
		return replay, true, nil
	}
	now := service.Now()
	var amount int64
	currency := ""
	for _, entryID := range canonical {
		var account, tenant, country, entryCurrency string
		var net int64
		var available time.Time
		err := tx.QueryRow(ctx, `SELECT account_identity_id::text,tenant_id::text,country,net_minor,currency,available_at FROM fulfillment.ledger_entries WHERE id=$1 FOR UPDATE`, entryID).Scan(&account, &tenant, &country, &net, &entryCurrency, &available)
		if errors.Is(err, pgx.ErrNoRows) {
			return Payout{}, false, ErrNotFound
		}
		if err != nil {
			return Payout{}, false, err
		}
		if account != actor.Subject || tenant != actor.TenantID || country != actor.Country || available.After(now) {
			return Payout{}, false, ErrForbidden
		}
		if currency != "" && currency != entryCurrency {
			return Payout{}, false, ErrConflict
		}
		var ok bool
		amount, ok = settlementAdd(amount, net)
		if !ok {
			return Payout{}, false, ErrInvalidRequest
		}
		currency = entryCurrency
	}
	value := Payout{ID: uuid.NewString(), AccountID: actor.Subject, Revision: 1, Amount: Money{AmountMinor: amount, Currency: currency}, Status: "PENDING_REVIEW", EntryIDs: canonical, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.payouts (id,tenant_id,country,account_identity_id,revision,amount_minor,currency,status,entry_ids,attempt_count,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,'PENDING_REVIEW',$7,0,$8,$8)`, value.ID, actor.TenantID, actor.Country, actor.Subject, amount, currency, canonical, now)
	if err != nil {
		return Payout{}, false, fmt.Errorf("insert fulfillment payout: %w", mapFulfillmentError(err))
	}
	for _, entryID := range canonical {
		if _, err := tx.Exec(ctx, `INSERT INTO fulfillment.payout_entry_claims (ledger_entry_id,payout_id) VALUES ($1,$2)`, entryID, value.ID); err != nil {
			return Payout{}, false, fmt.Errorf("claim fulfillment ledger entry: %w", ErrConflict)
		}
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, "payout-request", key, fingerprint, value, now); err != nil {
		return Payout{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payout{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ApprovePayout(actor Actor, key, payoutID string, revision int64, reason string) (Payout, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "FINANCE") || !actor.MFAVerified || !validKey(key) || !fulfillmentIsUUID(payoutID) || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return Payout{}, false, ErrMFARequired
		}
		return Payout{}, false, ErrForbidden
	}
	return service.mutatePayout(actor, key, payoutID, revision, "approve", reason, func(value *Payout) error {
		switch value.Status {
		case "PENDING_REVIEW":
			value.Status, value.FirstApproverID = "FIRST_APPROVED", actor.Subject
		case "FIRST_APPROVED":
			if value.FirstApproverID == actor.Subject {
				return ErrForbidden
			}
			value.Status, value.SecondApproverID = "APPROVED", actor.Subject
		default:
			return ErrInvalidTransition
		}
		return nil
	})
}

func (service *PostgresService) ExecutePayout(actor Actor, key, payoutID string, revision int64, providerReference string, success bool) (Payout, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "PAYOUT_WORKER", "FINANCE") || !validKey(key) || !fulfillmentIsUUID(payoutID) || !safeID(providerReference) {
		return Payout{}, false, ErrForbidden
	}
	return service.mutatePayout(actor, key, payoutID, revision, "execute", providerReference, func(value *Payout) error {
		if value.Status != "APPROVED" && value.Status != "FAILED" {
			return ErrInvalidTransition
		}
		if value.ProviderReference != "" && value.ProviderReference != providerReference {
			return ErrConflict
		}
		value.ProviderReference = providerReference
		value.AttemptCount++
		if success {
			value.Status = "PAID"
		} else {
			value.Status = "FAILED"
		}
		return nil
	})
}

func (service *PostgresService) mutatePayout(actor Actor, key, payoutID string, revision int64, operation, detail string, mutation func(*Payout) error) (Payout, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Payout{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope := "payout-" + operation + ":" + payoutID
	if err := fulfillmentCommandLock(ctx, tx, actor, scope, key); err != nil {
		return Payout{}, false, err
	}
	fingerprint := digest(struct {
		Revision int64
		Detail   string
	}{revision, detail})
	var replay Payout
	if found, err := loadFulfillmentReplay(ctx, tx, actor, scope, key, fingerprint, &replay); err != nil {
		return Payout{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresPayout(ctx, tx, actor.TenantID, actor.Country, payoutID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payout{}, false, ErrNotFound
	}
	if err != nil {
		return Payout{}, false, err
	}
	if value.Revision != revision {
		return Payout{}, false, ErrConflict
	}
	if err := mutation(&value); err != nil {
		return Payout{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.Now()
	_, err = tx.Exec(ctx, `UPDATE fulfillment.payouts SET revision=$2,status=$3,first_approver_identity_id=nullif($4,'')::uuid,second_approver_identity_id=nullif($5,'')::uuid,provider_reference=nullif($6,''),attempt_count=$7,updated_at=$8 WHERE id=$1`, value.ID, value.Revision, value.Status, value.FirstApproverID, value.SecondApproverID, value.ProviderReference, value.AttemptCount, value.UpdatedAt)
	if err != nil {
		return Payout{}, false, err
	}
	if operation == "approve" {
		if err := insertFulfillmentAudit(ctx, tx, actor, "PAYOUT_APPROVED", "PAYOUT", payoutID, detail, value.UpdatedAt); err != nil {
			return Payout{}, false, err
		}
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, scope, key, fingerprint, value, value.UpdatedAt); err != nil {
		return Payout{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payout{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Payouts(actor Actor, accountID string) ([]Payout, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(accountID) || !hasAnyRole(actor, "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") && actor.Subject != accountID {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, payoutSelect+` WHERE tenant_id=$1 AND country=$2 AND account_identity_id=$3 ORDER BY created_at DESC`, actor.TenantID, actor.Country, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Payout{}
	for rows.Next() {
		value, err := scanPostgresPayout(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) Reconcile(actor Actor, accountID string) (Reconciliation, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(accountID) || !hasAnyRole(actor, "FINANCE", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		if !actor.MFAVerified {
			return Reconciliation{}, ErrMFARequired
		}
		return Reconciliation{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	result := Reconciliation{AccountID: accountID, CalculationVersion: "durable-ledger-v1"}
	err := service.pool.QueryRow(ctx, `SELECT coalesce(sum(net_minor),0),coalesce(max(currency),'') FROM fulfillment.ledger_entries WHERE tenant_id=$1 AND country=$2 AND account_identity_id=$3`, actor.TenantID, actor.Country, accountID).Scan(&result.LedgerNetMinor, &result.Currency)
	if err != nil {
		return Reconciliation{}, err
	}
	err = service.pool.QueryRow(ctx, `SELECT coalesce(sum(amount_minor) FILTER (WHERE status='PAID'),0),coalesce(sum(amount_minor) FILTER (WHERE status NOT IN ('PAID','FAILED')),0) FROM fulfillment.payouts WHERE tenant_id=$1 AND country=$2 AND account_identity_id=$3`, actor.TenantID, actor.Country, accountID).Scan(&result.PaidMinor, &result.PendingMinor)
	if err != nil {
		return Reconciliation{}, err
	}
	result.VarianceMinor = result.LedgerNetMinor - result.PaidMinor - result.PendingMinor
	return result, nil
}

const ledgerSelect = `SELECT id::text,account_identity_id::text,reference_id::text,kind,gross_minor,commission_minor,tax_minor,net_minor,currency,calculation_version,available_at,created_at,tenant_id::text,country FROM fulfillment.ledger_entries`
const payoutSelect = `SELECT id::text,account_identity_id::text,revision,amount_minor,currency,status,entry_ids::text[],coalesce(first_approver_identity_id::text,''),coalesce(second_approver_identity_id::text,''),coalesce(provider_reference,''),attempt_count,created_at,updated_at,tenant_id::text,country FROM fulfillment.payouts`

func loadPostgresLedgerEntry(ctx context.Context, query fulfillmentQuerier, tenantID, country, accountID, referenceID, kind string) (LedgerEntry, error) {
	return scanPostgresLedgerEntry(query.QueryRow(ctx, ledgerSelect+` WHERE tenant_id=$1 AND country=$2 AND account_identity_id=$3 AND reference_id=$4 AND kind=$5`, tenantID, country, accountID, referenceID, kind))
}

func scanPostgresLedgerEntry(row fulfillmentRow) (LedgerEntry, error) {
	var value LedgerEntry
	err := row.Scan(&value.ID, &value.AccountID, &value.ReferenceID, &value.Kind, &value.Gross.AmountMinor, &value.Commission.AmountMinor, &value.Tax.AmountMinor, &value.Net.AmountMinor, &value.Gross.Currency, &value.CalculationVersion, &value.AvailableAt, &value.CreatedAt, &value.tenantID, &value.country)
	value.Commission.Currency, value.Tax.Currency, value.Net.Currency = value.Gross.Currency, value.Gross.Currency, value.Gross.Currency
	return value, err
}

func loadPostgresPayout(ctx context.Context, query fulfillmentQuerier, tenantID, country, id string, lock bool) (Payout, error) {
	suffix := " WHERE tenant_id=$1 AND country=$2 AND id=$3"
	if lock {
		suffix += " FOR UPDATE"
	}
	return scanPostgresPayout(query.QueryRow(ctx, payoutSelect+suffix, tenantID, country, id))
}

func scanPostgresPayout(row fulfillmentRow) (Payout, error) {
	var value Payout
	err := row.Scan(&value.ID, &value.AccountID, &value.Revision, &value.Amount.AmountMinor, &value.Amount.Currency, &value.Status, &value.EntryIDs, &value.FirstApproverID, &value.SecondApproverID, &value.ProviderReference, &value.AttemptCount, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country)
	return value, err
}

func allFulfillmentUUIDs(values []string) bool {
	for _, value := range values {
		if !fulfillmentIsUUID(value) {
			return false
		}
	}
	return true
}
