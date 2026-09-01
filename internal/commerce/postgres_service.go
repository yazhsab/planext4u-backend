package commerce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultCartIdempotencyTTL = 24 * time.Hour

// PostgresService is the durable, multi-instance cart implementation. Every
// mutation is serialized per customer scope and persisted with its response so
// retries after process restarts return the original result.
type PostgresService struct {
	pool           *pgxpool.Pool
	provider       SnapshotProvider
	clock          func() time.Time
	idempotencyTTL time.Duration
}

func NewPostgresService(pool *pgxpool.Pool, provider SnapshotProvider, clock func() time.Time) (*PostgresService, error) {
	return NewPostgresServiceWithTTL(pool, provider, defaultCartIdempotencyTTL, clock)
}

func NewPostgresServiceWithTTL(pool *pgxpool.Pool, provider SnapshotProvider, ttl time.Duration, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || provider == nil || clock == nil || ttl < time.Minute || ttl > 7*24*time.Hour {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, provider: provider, clock: clock, idempotencyTTL: ttl}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('commerce.carts') IS NOT NULL
		   AND to_regclass('commerce.cart_items') IS NOT NULL
		   AND to_regclass('commerce.idempotency_records') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check commerce schema readiness: %w", err)
	}
	if !ready {
		return errors.New("commerce schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Get(scope Scope) (Cart, error) {
	if !postgresCartScope(scope) {
		return Cart{}, ErrInvalidRequest
	}
	value, found, err := loadPostgresCart(context.Background(), service.pool, scope)
	if err != nil {
		return Cart{}, err
	}
	if !found {
		return emptyPostgresCart(scope, service.clock().UTC()), nil
	}
	return value, nil
}

func (service *PostgresService) Price(ctx context.Context, scope Scope, expectedRevision int64) (Cart, error) {
	if ctx == nil || expectedRevision < 0 || !postgresCartScope(scope) {
		return Cart{}, ErrInvalidRequest
	}
	current, err := service.Get(scope)
	if err != nil {
		return Cart{}, err
	}
	if current.Revision != expectedRevision {
		return Cart{}, ErrRevisionConflict
	}
	result, err := service.priceLines(ctx, scope, current.Items)
	if err != nil {
		return Cart{}, err
	}
	result.ID = current.ID
	result.Revision = current.Revision
	result.UpdatedAt = current.UpdatedAt
	latest, err := service.Get(scope)
	if err != nil {
		return Cart{}, err
	}
	if latest.Revision != expectedRevision {
		return Cart{}, ErrRevisionConflict
	}
	return result, nil
}

func (service *PostgresService) Change(ctx context.Context, scope Scope, idempotencyKey string, expectedRevision int64, variantID string, quantity int) (Cart, bool, error) {
	if ctx == nil || !postgresCartScope(scope) || !postgresUUID(variantID) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || expectedRevision < 0 || quantity < 0 || quantity > 999 {
		return Cart{}, false, ErrInvalidRequest
	}
	fingerprint := cartChangeFingerprint(expectedRevision, variantID, quantity)
	if replay, found, err := loadCartReplay(ctx, service.pool, scope, idempotencyKey, fingerprint, service.clock().UTC()); err != nil {
		return Cart{}, false, err
	} else if found {
		return replay, true, nil
	}
	current, err := service.Get(scope)
	if err != nil {
		return Cart{}, false, err
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
	lines, err := service.resolveLines(ctx, scope, quantities, previousPrices, variantID, quantity)
	if err != nil {
		return Cart{}, false, err
	}
	next := cartFromLines(current.ID, current.Revision+1, lines, service.clock().UTC())
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Cart{}, false, fmt.Errorf("begin cart mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockScope := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockScope); err != nil {
		return Cart{}, false, fmt.Errorf("lock cart scope: %w", err)
	}
	if replay, found, replayErr := loadCartReplay(ctx, tx, scope, idempotencyKey, fingerprint, service.clock().UTC()); replayErr != nil {
		return Cart{}, false, replayErr
	} else if found {
		return replay, true, nil
	}
	latest, found, err := loadPostgresCart(ctx, tx, scope)
	if err != nil {
		return Cart{}, false, err
	}
	latestRevision := int64(0)
	if found {
		latestRevision = latest.Revision
	}
	if latestRevision != expectedRevision {
		return Cart{}, false, ErrRevisionConflict
	}
	if found {
		next.ID = latest.ID
	} else {
		next.ID = emptyPostgresCart(scope, next.UpdatedAt).ID
	}
	if err := persistPostgresCart(ctx, tx, scope, next); err != nil {
		return Cart{}, false, err
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return Cart{}, false, fmt.Errorf("encode cart replay: %w", err)
	}
	now := service.clock().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO commerce.idempotency_records
			(tenant_id, country, customer_identity_id, idempotency_key,
			 request_fingerprint, response_payload, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)`,
		scope.TenantID, scope.Country, scope.CustomerID, idempotencyKey,
		fingerprint, payload, now, now.Add(service.idempotencyTTL)); err != nil {
		return Cart{}, false, fmt.Errorf("store cart replay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Cart{}, false, fmt.Errorf("commit cart mutation: %w", err)
	}
	return next, false, nil
}

func (service *PostgresService) priceLines(ctx context.Context, scope Scope, source []CartLine) (Cart, error) {
	quantities := make(map[string]int, len(source))
	previous := make(map[string]Money, len(source))
	for _, line := range source {
		quantities[line.VariantID] = line.Quantity
		previous[line.VariantID] = line.UnitPrice
	}
	lines, err := service.resolveLines(ctx, scope, quantities, previous, "", 0)
	if err != nil {
		return Cart{}, err
	}
	return cartFromLines("", 0, lines, time.Time{}), nil
}

func (service *PostgresService) resolveLines(ctx context.Context, scope Scope, quantities map[string]int, previous map[string]Money, changedID string, changedQuantity int) ([]CartLine, error) {
	ids := make([]string, 0, len(quantities))
	for id := range quantities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	lines := make([]CartLine, 0, len(ids))
	currency := ""
	for _, id := range ids {
		snapshot, err := service.provider.Resolve(ctx, scope, id)
		if err != nil {
			if id == changedID {
				return nil, ErrVariantNotFound
			}
			return nil, ErrVariantUnavailable
		}
		if !validPostgresSnapshot(snapshot, id) {
			return nil, ErrVariantUnavailable
		}
		if currency == "" {
			currency = snapshot.UnitPrice.Currency
		} else if snapshot.UnitPrice.Currency != currency {
			return nil, ErrVariantUnavailable
		}
		requested := quantities[id]
		available := snapshot.Available && requested <= snapshot.Stock && requested <= snapshot.MaxPerOrder
		if id == changedID && changedQuantity > 0 && !snapshot.Available {
			return nil, ErrVariantUnavailable
		}
		if id == changedID && changedQuantity > 0 && !available {
			return nil, ErrQuantityUnavailable
		}
		old, existed := previous[id]
		lines = append(lines, CartLine{
			VariantID: id, ItemID: snapshot.ItemID, VendorID: snapshot.VendorID,
			ItemName: snapshot.ItemName, VariantName: snapshot.VariantName, MediaRef: snapshot.MediaRef,
			Quantity: requested, UnitPrice: snapshot.UnitPrice,
			LineTotal: Money{AmountMinor: snapshot.UnitPrice.AmountMinor * int64(requested), Currency: snapshot.UnitPrice.Currency},
			Available: available, PriceChanged: existed && old != snapshot.UnitPrice,
		})
	}
	return lines, nil
}

func cartFromLines(id string, revision int64, lines []CartLine, updatedAt time.Time) Cart {
	currency := "INR"
	if len(lines) > 0 {
		currency = lines[0].UnitPrice.Currency
	}
	var subtotal int64
	allAvailable := true
	pricingStatus := "CURRENT"
	for _, line := range lines {
		if line.UnitPrice.Currency != currency {
			allAvailable = false
		}
		subtotal += line.LineTotal.AmountMinor
		allAvailable = allAvailable && line.Available
		if line.PriceChanged {
			pricingStatus = "REPRICED"
		}
	}
	zero := Money{Currency: currency}
	return Cart{ID: id, Revision: revision, Items: lines,
		Subtotal: Money{AmountMinor: subtotal, Currency: currency}, Discount: zero, Tax: zero, Fees: zero,
		Total: Money{AmountMinor: subtotal, Currency: currency}, PricingStatus: pricingStatus,
		AllowedActions: allowedActions(len(lines), allAvailable), UpdatedAt: updatedAt}
}

type postgresCartQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadPostgresCart(ctx context.Context, querier postgresCartQuerier, scope Scope) (Cart, bool, error) {
	var value Cart
	var currency string
	err := querier.QueryRow(ctx, `
		SELECT id::text, revision, currency, subtotal_minor, discount_minor,
		       tax_minor, fees_minor, total_minor, pricing_status,
		       allowed_actions, updated_at
		FROM commerce.carts
		WHERE tenant_id = $1 AND country = $2 AND customer_identity_id = $3`,
		scope.TenantID, scope.Country, scope.CustomerID).Scan(
		&value.ID, &value.Revision, &currency, &value.Subtotal.AmountMinor,
		&value.Discount.AmountMinor, &value.Tax.AmountMinor, &value.Fees.AmountMinor,
		&value.Total.AmountMinor, &value.PricingStatus, &value.AllowedActions, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cart{}, false, nil
	}
	if err != nil {
		return Cart{}, false, fmt.Errorf("load cart: %w", err)
	}
	value.Subtotal.Currency, value.Discount.Currency, value.Tax.Currency, value.Fees.Currency, value.Total.Currency = currency, currency, currency, currency, currency
	rows, err := querier.Query(ctx, `
		SELECT variant_id::text, item_id::text, COALESCE(vendor_id::text, ''),
		       item_name, variant_name, COALESCE(media_asset_id::text, ''), quantity,
		       unit_price_minor, line_total_minor, available, price_changed
		FROM commerce.cart_items WHERE cart_id = $1 ORDER BY variant_id`, value.ID)
	if err != nil {
		return Cart{}, false, fmt.Errorf("load cart lines: %w", err)
	}
	defer rows.Close()
	value.Items = []CartLine{}
	for rows.Next() {
		var line CartLine
		if err := rows.Scan(&line.VariantID, &line.ItemID, &line.VendorID, &line.ItemName, &line.VariantName, &line.MediaRef, &line.Quantity,
			&line.UnitPrice.AmountMinor, &line.LineTotal.AmountMinor, &line.Available, &line.PriceChanged); err != nil {
			return Cart{}, false, fmt.Errorf("scan cart line: %w", err)
		}
		line.UnitPrice.Currency, line.LineTotal.Currency = currency, currency
		value.Items = append(value.Items, line)
	}
	if err := rows.Err(); err != nil {
		return Cart{}, false, fmt.Errorf("iterate cart lines: %w", err)
	}
	return value, true, nil
}

func persistPostgresCart(ctx context.Context, tx pgx.Tx, scope Scope, value Cart) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO commerce.carts
			(id, tenant_id, country, customer_identity_id, revision, currency,
			 subtotal_minor, discount_minor, tax_minor, fees_minor, total_minor,
			 pricing_status, allowed_actions, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (tenant_id, country, customer_identity_id) DO UPDATE SET
			revision = EXCLUDED.revision, currency = EXCLUDED.currency,
			subtotal_minor = EXCLUDED.subtotal_minor, discount_minor = EXCLUDED.discount_minor,
			tax_minor = EXCLUDED.tax_minor, fees_minor = EXCLUDED.fees_minor,
			total_minor = EXCLUDED.total_minor, pricing_status = EXCLUDED.pricing_status,
			allowed_actions = EXCLUDED.allowed_actions, updated_at = EXCLUDED.updated_at`,
		value.ID, scope.TenantID, scope.Country, scope.CustomerID, value.Revision,
		value.Total.Currency, value.Subtotal.AmountMinor, value.Discount.AmountMinor,
		value.Tax.AmountMinor, value.Fees.AmountMinor, value.Total.AmountMinor,
		value.PricingStatus, value.AllowedActions, value.UpdatedAt)
	if err != nil {
		return fmt.Errorf("persist cart: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM commerce.cart_items WHERE cart_id = $1`, value.ID); err != nil {
		return fmt.Errorf("replace cart lines: %w", err)
	}
	for _, line := range value.Items {
		var media any
		if postgresUUID(line.MediaRef) {
			media = line.MediaRef
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO commerce.cart_items
				(cart_id, variant_id, item_id, vendor_id, item_name, variant_name,
				 media_asset_id, quantity, unit_price_minor, line_total_minor, available, price_changed)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			value.ID, line.VariantID, line.ItemID, line.VendorID, line.ItemName,
			line.VariantName, media, line.Quantity, line.UnitPrice.AmountMinor,
			line.LineTotal.AmountMinor, line.Available, line.PriceChanged); err != nil {
			return fmt.Errorf("persist cart line: %w", err)
		}
	}
	return nil
}

type postgresCartReplayStore interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadCartReplay(ctx context.Context, tx postgresCartReplayStore, scope Scope, key, fingerprint string, now time.Time) (Cart, bool, error) {
	var storedFingerprint string
	var payload []byte
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT request_fingerprint, response_payload, expires_at
		FROM commerce.idempotency_records
		WHERE tenant_id = $1 AND country = $2 AND customer_identity_id = $3 AND idempotency_key = $4`,
		scope.TenantID, scope.Country, scope.CustomerID, key).Scan(&storedFingerprint, &payload, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cart{}, false, nil
	}
	if err != nil {
		return Cart{}, false, fmt.Errorf("load cart replay: %w", err)
	}
	if !expiresAt.After(now) {
		if _, err := tx.Exec(ctx, `DELETE FROM commerce.idempotency_records WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND idempotency_key=$4`, scope.TenantID, scope.Country, scope.CustomerID, key); err != nil {
			return Cart{}, false, fmt.Errorf("expire cart replay: %w", err)
		}
		return Cart{}, false, nil
	}
	if storedFingerprint != fingerprint {
		return Cart{}, false, ErrIdempotencyConflict
	}
	var value Cart
	if err := json.Unmarshal(payload, &value); err != nil {
		return Cart{}, false, fmt.Errorf("decode cart replay: %w", err)
	}
	return value, true, nil
}

func emptyPostgresCart(scope Scope, now time.Time) Cart {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(scopeKey(scope))).String()
	currency := "INR"
	if scope.Country == "NG" {
		currency = "NGN"
	}
	zero := Money{Currency: currency}
	return Cart{ID: id, Items: []CartLine{}, Subtotal: zero, Discount: zero, Tax: zero, Fees: zero, Total: zero,
		PricingStatus: "CURRENT", AllowedActions: []string{"BROWSE"}, UpdatedAt: now}
}

func cartChangeFingerprint(expectedRevision int64, variantID string, quantity int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%d", expectedRevision, variantID, quantity)))
	return hex.EncodeToString(digest[:])
}

func postgresCartScope(scope Scope) bool {
	return postgresUUID(scope.TenantID) && postgresUUID(scope.CustomerID) && len(scope.Country) == 2 && scope.Country[0] >= 'A' && scope.Country[0] <= 'Z' && scope.Country[1] >= 'A' && scope.Country[1] <= 'Z'
}

func postgresUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validPostgresSnapshot(snapshot VariantSnapshot, expectedID string) bool {
	return validSnapshot(snapshot, expectedID) && postgresUUID(snapshot.VariantID) && postgresUUID(snapshot.ItemID) && postgresUUID(snapshot.VendorID)
}
