package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

const checkoutStoreTimeout = 10 * time.Second

type stateStore interface {
	Ready(context.Context) error
	Addresses(Scope) ([]Address, error)
	Address(Scope, string) (Address, bool, error)
	CreateAddress(Scope, string, string, Address) (Address, bool, error)
	UpdateAddress(Scope, string, string, string, int64, Address) (Address, bool, error)
	DeleteAddress(Scope, string, string, string, int64) (bool, error)
	Quote(Scope, string) (Quote, bool, error)
	LoadQuote(Scope, string, string) (Quote, bool, error)
	SaveQuote(Scope, string, string, Quote) (Quote, bool, error)
	LoadPlace(Scope, string, string) (PlaceResult, bool, error)
	SavePlace(Scope, string, string, PlaceResult, process) (PlaceResult, bool, error)
	LoadRefill(Scope, string, string) (WalletRefillResult, bool, error)
	SaveRefill(Scope, string, string, WalletRefillResult, refillProcess) (WalletRefillResult, bool, error)
	Process(string) (process, bool, error)
	Refill(string) (refillProcess, bool, error)
}

// PostgresStore persists checkout orchestration records. Financial and stock
// side effects remain owned by their service schemas; this store retains the
// durable links needed to resume/finalize those workflows after a restart.
type PostgresStore struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresStore(pool *pgxpool.Pool, clock func() time.Time) (*PostgresStore, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresStore{pool: pool, clock: clock}, nil
}

func (store *PostgresStore) Ready(ctx context.Context) error {
	var ready bool
	if err := store.pool.QueryRow(ctx, `
		SELECT to_regclass('commerce.checkout_commands') IS NOT NULL
		   AND to_regclass('commerce.checkout_processes') IS NOT NULL
		   AND to_regclass('commerce.checkout_quotes') IS NOT NULL
		   AND to_regclass('commerce.customer_addresses') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check checkout schema readiness: %w", err)
	}
	if !ready {
		return errors.New("checkout schema is unavailable")
	}
	return nil
}

func (store *PostgresStore) Addresses(scope Scope) ([]Address, error) {
	if !postgresCheckoutScope(scope) {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	rows, err := store.pool.Query(ctx, `
		SELECT id::text,label,line1,line2,postal_code,locality,
		       COALESCE(latitude,0),COALESCE(longitude,0),serviceable,is_default,revision
		FROM commerce.customer_addresses
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND active
		ORDER BY is_default DESC,id`, scope.TenantID, scope.Country, scope.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("load checkout addresses: %w", err)
	}
	defer rows.Close()
	values := []Address{}
	for rows.Next() {
		value, err := scanCheckoutAddress(rows, scope)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *PostgresStore) Address(scope Scope, id string) (Address, bool, error) {
	if !postgresCheckoutScope(scope) || !checkoutUUID(id) {
		return Address{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	value, found, err := loadCheckoutAddress(ctx, store.pool, scope, id, false)
	return value, found, err
}

func (store *PostgresStore) CreateAddress(scope Scope, key, fingerprint string, value Address) (Address, bool, error) {
	if !postgresCheckoutScope(scope) {
		return Address{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Address{}, false, fmt.Errorf("begin address creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCheckoutCommand(ctx, tx, scope, "ADDRESS_CREATE", key); err != nil {
		return Address{}, false, err
	}
	if replay, found, err := loadCheckoutCommand[Address](ctx, tx, scope, "ADDRESS_CREATE", key, fingerprint); err != nil {
		return Address{}, false, err
	} else if found {
		return replay, true, nil
	}
	if value.Default {
		if _, err := tx.Exec(ctx, `UPDATE commerce.customer_addresses SET is_default=false,updated_at=$4 WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND active`, scope.TenantID, scope.Country, scope.CustomerID, store.clock().UTC()); err != nil {
			return Address{}, false, fmt.Errorf("clear default checkout address: %w", err)
		}
	} else {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commerce.customer_addresses WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND active`, scope.TenantID, scope.Country, scope.CustomerID).Scan(&count); err != nil {
			return Address{}, false, fmt.Errorf("count checkout addresses: %w", err)
		}
		value.Default = count == 0
	}
	value.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(scope.TenantID+":"+scope.Country+":"+scope.CustomerID+":"+key)).String()
	value.Revision, value.TenantID, value.Country, value.CustomerID = 1, scope.TenantID, scope.Country, scope.CustomerID
	now := store.clock().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO commerce.customer_addresses
			(id,tenant_id,country,customer_identity_id,label,postal_code,locality,active,created_at,updated_at,
			 line1,line2,latitude,longitude,serviceable,is_default,revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8,$8,$9,$10,$11,$12,$13,$14,1)`,
		value.ID, scope.TenantID, scope.Country, scope.CustomerID, value.Label,
		value.PostalCode, value.Locality, now, value.Line1, value.Line2,
		nullableCoordinate(value.Latitude), nullableCoordinate(value.Longitude), value.Serviceable, value.Default); err != nil {
		return Address{}, false, fmt.Errorf("insert checkout address: %w", err)
	}
	if err := storeCheckoutCommand(ctx, tx, scope, "ADDRESS_CREATE", key, fingerprint, value, now); err != nil {
		return Address{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Address{}, false, fmt.Errorf("commit address creation: %w", err)
	}
	return value, false, nil
}

func (store *PostgresStore) UpdateAddress(scope Scope, key, fingerprint, id string, expectedRevision int64, value Address) (Address, bool, error) {
	if !postgresCheckoutScope(scope) || !checkoutUUID(id) {
		return Address{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Address{}, false, fmt.Errorf("begin address update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCheckoutCommand(ctx, tx, scope, "ADDRESS_UPDATE", key); err != nil {
		return Address{}, false, err
	}
	if replay, found, err := loadCheckoutCommand[Address](ctx, tx, scope, "ADDRESS_UPDATE", key, fingerprint); err != nil {
		return Address{}, false, err
	} else if found {
		return replay, true, nil
	}
	current, found, err := loadCheckoutAddress(ctx, tx, scope, id, true)
	if err != nil {
		return Address{}, false, err
	}
	if !found {
		return Address{}, false, ErrAddressNotFound
	}
	if current.Revision != expectedRevision {
		return Address{}, false, ErrAddressConflict
	}
	if value.Default {
		if _, err := tx.Exec(ctx, `UPDATE commerce.customer_addresses SET is_default=false,updated_at=$4 WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND active AND id<>$5`, scope.TenantID, scope.Country, scope.CustomerID, store.clock().UTC(), id); err != nil {
			return Address{}, false, fmt.Errorf("replace default checkout address: %w", err)
		}
	}
	value.ID, value.Revision, value.TenantID, value.Country, value.CustomerID = id, current.Revision+1, scope.TenantID, scope.Country, scope.CustomerID
	now := store.clock().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE commerce.customer_addresses SET label=$2,line1=$3,line2=$4,postal_code=$5,locality=$6,
			latitude=$7,longitude=$8,serviceable=$9,is_default=$10,revision=$11,updated_at=$12
		WHERE id=$1`, id, value.Label, value.Line1, value.Line2, value.PostalCode,
		value.Locality, nullableCoordinate(value.Latitude), nullableCoordinate(value.Longitude),
		value.Serviceable, value.Default, value.Revision, now); err != nil {
		return Address{}, false, fmt.Errorf("update checkout address: %w", err)
	}
	if err := storeCheckoutCommand(ctx, tx, scope, "ADDRESS_UPDATE", key, fingerprint, value, now); err != nil {
		return Address{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Address{}, false, fmt.Errorf("commit address update: %w", err)
	}
	return value, false, nil
}

func (store *PostgresStore) DeleteAddress(scope Scope, key, fingerprint, id string, expectedRevision int64) (bool, error) {
	if !postgresCheckoutScope(scope) || !checkoutUUID(id) {
		return false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, fmt.Errorf("begin address deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCheckoutCommand(ctx, tx, scope, "ADDRESS_DELETE", key); err != nil {
		return false, err
	}
	if _, found, err := loadCheckoutCommand[map[string]bool](ctx, tx, scope, "ADDRESS_DELETE", key, fingerprint); err != nil {
		return false, err
	} else if found {
		return true, nil
	}
	current, found, err := loadCheckoutAddress(ctx, tx, scope, id, true)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrAddressNotFound
	}
	if current.Revision != expectedRevision {
		return false, ErrAddressConflict
	}
	now := store.clock().UTC()
	if _, err := tx.Exec(ctx, `UPDATE commerce.customer_addresses SET active=false,is_default=false,revision=revision+1,updated_at=$2 WHERE id=$1`, id, now); err != nil {
		return false, fmt.Errorf("delete checkout address: %w", err)
	}
	if current.Default {
		if _, err := tx.Exec(ctx, `
			UPDATE commerce.customer_addresses SET is_default=true,updated_at=$4
			WHERE id=(SELECT id FROM commerce.customer_addresses WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND active ORDER BY created_at,id LIMIT 1)`, scope.TenantID, scope.Country, scope.CustomerID, now); err != nil {
			return false, fmt.Errorf("promote checkout address: %w", err)
		}
	}
	if err := storeCheckoutCommand(ctx, tx, scope, "ADDRESS_DELETE", key, fingerprint, map[string]bool{"deleted": true}, now); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit address deletion: %w", err)
	}
	return false, nil
}

func (store *PostgresStore) Quote(scope Scope, id string) (Quote, bool, error) {
	if !postgresCheckoutScope(scope) || !checkoutUUID(id) {
		return Quote{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	var payload []byte
	err := store.pool.QueryRow(ctx, `SELECT snapshot FROM commerce.checkout_quotes WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4`, id, scope.TenantID, scope.Country, scope.CustomerID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Quote{}, false, nil
	}
	if err != nil {
		return Quote{}, false, fmt.Errorf("load checkout quote: %w", err)
	}
	var value Quote
	if json.Unmarshal(payload, &value) != nil {
		return Quote{}, false, fmt.Errorf("decode checkout quote: %w", ErrInvalidRequest)
	}
	value.scope = scope
	return value, true, nil
}

func (store *PostgresStore) LoadQuote(scope Scope, key, fingerprint string) (Quote, bool, error) {
	payload, found, err := store.loadRawCommand(scope, "QUOTE", key, fingerprint)
	if err != nil || !found {
		return Quote{}, found, err
	}
	var value Quote
	if err := json.Unmarshal(payload, &value); err != nil {
		return Quote{}, false, fmt.Errorf("decode checkout quote replay: %w", err)
	}
	value.scope = scope
	return value, true, nil
}

func (store *PostgresStore) SaveQuote(scope Scope, key, fingerprint string, value Quote) (Quote, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Quote{}, false, fmt.Errorf("begin checkout quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCheckoutCommand(ctx, tx, scope, "QUOTE", key); err != nil {
		return Quote{}, false, err
	}
	if replay, found, err := loadCheckoutCommand[Quote](ctx, tx, scope, "QUOTE", key, fingerprint); err != nil {
		return Quote{}, false, err
	} else if found {
		replay.scope = scope
		return replay, true, nil
	}
	if !checkoutUUID(value.cartID) {
		return Quote{}, false, ErrInvalidRequest
	}
	value.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(scope.TenantID+":"+scope.Country+":"+scope.CustomerID+":quote:"+key)).String()
	payload, _ := json.Marshal(value)
	requestFingerprint := checkoutFingerprint(fingerprint)
	if _, err := tx.Exec(ctx, `
		INSERT INTO commerce.checkout_quotes
			(id,tenant_id,country,customer_identity_id,cart_id,cart_revision,pricing_policy_version,
			 request_fingerprint,snapshot,created_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11)`,
		value.ID, scope.TenantID, scope.Country, scope.CustomerID, value.cartID,
		value.CartRevision, value.PricingPolicyVersion, requestFingerprint, payload,
		value.CreatedAt, value.ExpiresAt); err != nil {
		return Quote{}, false, fmt.Errorf("insert checkout quote: %w", err)
	}
	if err := storeCheckoutCommand(ctx, tx, scope, "QUOTE", key, fingerprint, value, value.CreatedAt); err != nil {
		return Quote{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Quote{}, false, fmt.Errorf("commit checkout quote: %w", err)
	}
	return value, false, nil
}

func (store *PostgresStore) LoadPlace(scope Scope, key, fingerprint string) (PlaceResult, bool, error) {
	payload, found, err := store.loadRawCommand(scope, "PLACE", key, fingerprint)
	if err != nil || !found {
		return PlaceResult{}, found, err
	}
	var value PlaceResult
	if err := json.Unmarshal(payload, &value); err != nil {
		return PlaceResult{}, false, fmt.Errorf("decode checkout placement: %w", err)
	}
	return value, true, nil
}

func (store *PostgresStore) SavePlace(scope Scope, key, fingerprint string, value PlaceResult, checkoutProcess process) (PlaceResult, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PlaceResult{}, false, fmt.Errorf("begin checkout placement record: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCheckoutCommand(ctx, tx, scope, "PLACE", key); err != nil {
		return PlaceResult{}, false, err
	}
	if replay, found, err := loadCheckoutCommand[PlaceResult](ctx, tx, scope, "PLACE", key, fingerprint); err != nil {
		return PlaceResult{}, false, err
	} else if found {
		return replay, true, nil
	}
	now := store.clock().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO commerce.checkout_processes
			(payment_id,tenant_id,country,customer_identity_id,process_type,reservation_id,order_id,wallet_debit_id,created_at)
		VALUES ($1,$2,$3,$4,'CHECKOUT',$5,$6,$7,$8)
		ON CONFLICT (payment_id) DO NOTHING`, value.Payment.ID, scope.TenantID, scope.Country, scope.CustomerID,
		checkoutProcess.reservationID, checkoutProcess.orderID, nullableUUID(checkoutProcess.walletDebitID), now); err != nil {
		return PlaceResult{}, false, fmt.Errorf("insert checkout process: %w", err)
	}
	if err := storeCheckoutCommand(ctx, tx, scope, "PLACE", key, fingerprint, value, now); err != nil {
		return PlaceResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlaceResult{}, false, fmt.Errorf("commit checkout placement record: %w", err)
	}
	return value, false, nil
}

func (store *PostgresStore) LoadRefill(scope Scope, key, fingerprint string) (WalletRefillResult, bool, error) {
	payload, found, err := store.loadRawCommand(scope, "WALLET_REFILL", key, fingerprint)
	if err != nil || !found {
		return WalletRefillResult{}, found, err
	}
	var value WalletRefillResult
	if err := json.Unmarshal(payload, &value); err != nil {
		return WalletRefillResult{}, false, fmt.Errorf("decode wallet refill: %w", err)
	}
	return value, true, nil
}

func (store *PostgresStore) SaveRefill(scope Scope, key, fingerprint string, value WalletRefillResult, refill refillProcess) (WalletRefillResult, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return WalletRefillResult{}, false, fmt.Errorf("begin wallet refill record: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCheckoutCommand(ctx, tx, scope, "WALLET_REFILL", key); err != nil {
		return WalletRefillResult{}, false, err
	}
	if replay, found, err := loadCheckoutCommand[WalletRefillResult](ctx, tx, scope, "WALLET_REFILL", key, fingerprint); err != nil {
		return WalletRefillResult{}, false, err
	} else if found {
		return replay, true, nil
	}
	offer, _ := json.Marshal(refill.offer)
	now := store.clock().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO commerce.checkout_processes
			(payment_id,tenant_id,country,customer_identity_id,process_type,refill_offer,created_at)
		VALUES ($1,$2,$3,$4,'WALLET_REFILL',$5::jsonb,$6)
		ON CONFLICT (payment_id) DO NOTHING`, value.Payment.ID, scope.TenantID, scope.Country, scope.CustomerID, offer, now); err != nil {
		return WalletRefillResult{}, false, fmt.Errorf("insert wallet refill process: %w", err)
	}
	if err := storeCheckoutCommand(ctx, tx, scope, "WALLET_REFILL", key, fingerprint, value, now); err != nil {
		return WalletRefillResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WalletRefillResult{}, false, fmt.Errorf("commit wallet refill record: %w", err)
	}
	return value, false, nil
}

func (store *PostgresStore) Process(paymentID string) (process, bool, error) {
	value, kind, offer, found, err := store.loadProcess(paymentID)
	_ = offer
	if err != nil || !found || kind != "CHECKOUT" {
		return process{}, false, err
	}
	return value, true, nil
}

func (store *PostgresStore) Refill(paymentID string) (refillProcess, bool, error) {
	value, kind, offer, found, err := store.loadProcess(paymentID)
	if err != nil || !found || kind != "WALLET_REFILL" {
		return refillProcess{}, false, err
	}
	return refillProcess{scope: value.scope, offer: offer}, true, nil
}

func (store *PostgresStore) loadProcess(paymentID string) (process, string, wallet.RefillOffer, bool, error) {
	if !checkoutUUID(paymentID) {
		return process{}, "", wallet.RefillOffer{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	var scope Scope
	var kind string
	var reservationID, orderID, debitID *string
	var offerPayload []byte
	err := store.pool.QueryRow(ctx, `
		SELECT tenant_id::text,country,customer_identity_id::text,process_type,
		       reservation_id::text,order_id::text,wallet_debit_id::text,refill_offer
		FROM commerce.checkout_processes WHERE payment_id=$1`, paymentID).Scan(
		&scope.TenantID, &scope.Country, &scope.CustomerID, &kind,
		&reservationID, &orderID, &debitID, &offerPayload)
	if errors.Is(err, pgx.ErrNoRows) {
		return process{}, "", wallet.RefillOffer{}, false, nil
	}
	if err != nil {
		return process{}, "", wallet.RefillOffer{}, false, fmt.Errorf("load checkout process: %w", err)
	}
	value := process{scope: scope}
	if reservationID != nil {
		value.reservationID = *reservationID
	}
	if orderID != nil {
		value.orderID = *orderID
	}
	if debitID != nil {
		value.walletDebitID = *debitID
	}
	var offer wallet.RefillOffer
	if len(offerPayload) > 0 && json.Unmarshal(offerPayload, &offer) != nil {
		return process{}, "", wallet.RefillOffer{}, false, fmt.Errorf("decode wallet refill process: %w", ErrInvalidRequest)
	}
	return value, kind, offer, true, nil
}

func (store *PostgresStore) loadRawCommand(scope Scope, commandType, key, fingerprint string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkoutStoreTimeout)
	defer cancel()
	var storedFingerprint string
	var payload []byte
	err := store.pool.QueryRow(ctx, `
		SELECT request_fingerprint,response_payload FROM commerce.checkout_commands
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND command_type=$4 AND idempotency_key=$5`,
		scope.TenantID, scope.Country, scope.CustomerID, commandType, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load checkout command: %w", err)
	}
	if storedFingerprint != checkoutFingerprint(fingerprint) {
		return nil, false, ErrIdempotencyConflict
	}
	return payload, true, nil
}

type checkoutAddressQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadCheckoutAddress(ctx context.Context, querier checkoutAddressQuerier, scope Scope, id string, lock bool) (Address, bool, error) {
	query := `SELECT id::text,label,line1,line2,postal_code,locality,COALESCE(latitude,0),COALESCE(longitude,0),serviceable,is_default,revision
		FROM commerce.customer_addresses WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4 AND active`
	if lock {
		query += ` FOR UPDATE`
	}
	value, err := scanCheckoutAddress(querier.QueryRow(ctx, query, id, scope.TenantID, scope.Country, scope.CustomerID), scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return Address{}, false, nil
	}
	if err != nil {
		return Address{}, false, err
	}
	return value, true, nil
}

type checkoutScanner interface{ Scan(...any) error }

func scanCheckoutAddress(scanner checkoutScanner, scope Scope) (Address, error) {
	var value Address
	if err := scanner.Scan(&value.ID, &value.Label, &value.Line1, &value.Line2,
		&value.PostalCode, &value.Locality, &value.Latitude, &value.Longitude,
		&value.Serviceable, &value.Default, &value.Revision); err != nil {
		return Address{}, err
	}
	value.TenantID, value.Country, value.CustomerID = scope.TenantID, scope.Country, scope.CustomerID
	return value, nil
}

func lockCheckoutCommand(ctx context.Context, tx pgx.Tx, scope Scope, commandType, key string) error {
	lockKey := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID + ":" + commandType + ":" + key
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return fmt.Errorf("lock checkout command: %w", err)
	}
	return nil
}

func loadCheckoutCommand[T any](ctx context.Context, tx pgx.Tx, scope Scope, commandType, key, fingerprint string) (T, bool, error) {
	var zero T
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT request_fingerprint,response_payload FROM commerce.checkout_commands
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND command_type=$4 AND idempotency_key=$5`,
		scope.TenantID, scope.Country, scope.CustomerID, commandType, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, fmt.Errorf("load checkout command: %w", err)
	}
	if storedFingerprint != checkoutFingerprint(fingerprint) {
		return zero, false, ErrIdempotencyConflict
	}
	var value T
	if json.Unmarshal(payload, &value) != nil {
		return zero, false, fmt.Errorf("decode checkout command: %w", ErrInvalidRequest)
	}
	return value, true, nil
}

func storeCheckoutCommand(ctx context.Context, tx pgx.Tx, scope Scope, commandType, key, fingerprint string, value any, now time.Time) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode checkout command: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO commerce.checkout_commands
			(tenant_id,country,customer_identity_id,command_type,idempotency_key,request_fingerprint,response_payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`,
		scope.TenantID, scope.Country, scope.CustomerID, commandType, key,
		checkoutFingerprint(fingerprint), payload, now); err != nil {
		return fmt.Errorf("store checkout command: %w", err)
	}
	return nil
}

func checkoutFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func postgresCheckoutScope(scope Scope) bool {
	return checkoutUUID(scope.TenantID) && checkoutUUID(scope.CustomerID) && len(scope.Country) == 2 && scope.Country == strings.ToUpper(scope.Country)
}

func checkoutUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func nullableCoordinate(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableUUID(value string) any {
	if checkoutUUID(value) {
		return value
	}
	return nil
}
