package food

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pay "github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

const foodOperationTimeout = 30 * time.Second

type PaymentService interface {
	Create(pay.Scope, string, string, pay.Method, pay.Money) (pay.Payment, bool, error)
	RequestRefund(pay.Scope, string, pay.Money) (pay.Payment, error)
	CancelUncaptured(pay.Scope, string) (pay.Payment, error)
}

type WalletService interface {
	Redeem(wallet.Scope, string, string, int64) (wallet.LedgerEntry, bool, error)
	ReverseDebit(wallet.Scope, string, string, string) (wallet.LedgerEntry, bool, error)
}

type PostgresService struct {
	pool     *pgxpool.Pool
	payments PaymentService
	wallet   WalletService
	clock    func() time.Time
}

func NewPostgresService(pool *pgxpool.Pool, payments PaymentService, walletService WalletService, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || payments == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, payments: payments, wallet: walletService, clock: clock}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `SELECT to_regclass('food.restaurants') IS NOT NULL AND to_regclass('food.policies') IS NOT NULL AND to_regclass('food.orders') IS NOT NULL AND to_regclass('food.idempotency_records') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check food schema readiness: %w", err)
	}
	if !ready {
		return errors.New("food schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Restaurants(actor Actor, postalCode, cuisine string) ([]Restaurant, error) {
	if !postgresFoodActor(actor) || !hasRole(actor, "CUSTOMER") || !validPostal(postalCode) {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `SELECT id::text,owner_identity_id::text,name,cuisine,postal_codes,rating::float8,verified,open,accept_until_minute,preparation_minutes,delivery_fee_minor,minimum_order_minor,currency,updated_at FROM food.restaurants WHERE tenant_id=$1 AND country=$2 AND verified=true AND $3=ANY(postal_codes) AND ($4='' OR EXISTS(SELECT 1 FROM unnest(cuisine) c WHERE lower(c)=lower($4))) ORDER BY rating DESC`, actor.TenantID, actor.Country, postalCode, cuisine)
	if err != nil {
		return nil, fmt.Errorf("list restaurants: %w", err)
	}
	defer rows.Close()
	result := []Restaurant{}
	for rows.Next() {
		value, err := scanRestaurant(rows, actor.TenantID, actor.Country)
		if err != nil {
			return nil, err
		}
		result = append(result, publicRestaurant(value))
	}
	return result, rows.Err()
}

func (service *PostgresService) Menu(actor Actor, restaurantID, postalCode string) ([]MenuItem, error) {
	if !postgresFoodActor(actor) || !hasRole(actor, "CUSTOMER") || !postgresFoodUUID(restaurantID) || !validPostal(postalCode) {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	restaurant, err := loadRestaurant(ctx, service.pool, restaurantID, actor.TenantID, actor.Country, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !contains(restaurant.PostalCodes, postalCode) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text FROM food.menu_items WHERE restaurant_id=$1 ORDER BY name`, restaurantID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	result := make([]MenuItem, 0, len(ids))
	for _, id := range ids {
		item, err := loadMenuItem(ctx, service.pool, id, actor.TenantID, actor.Country)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (service *PostgresService) SetCart(actor Actor, key string, request CartRequest) (Cart, bool, error) {
	if !postgresFoodActor(actor) || !hasRole(actor, "CUSTOMER") || !validKey(key) || !postgresFoodUUID(request.RestaurantID) || !validPostal(request.PostalCode) || len(request.Lines) == 0 || len(request.Lines) > 30 {
		return Cart{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Cart{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := foodCommandLock(ctx, tx, actor, "cart", key); err != nil {
		return Cart{}, false, err
	}
	fingerprint := digest(request)
	var replay Cart
	if found, err := loadFoodReplay(ctx, tx, actor, "cart", key, fingerprint, &replay); err != nil {
		return Cart{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	restaurant, err := loadRestaurant(ctx, tx, request.RestaurantID, actor.TenantID, actor.Country, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !contains(restaurant.PostalCodes, request.PostalCode) {
		return Cart{}, false, ErrNotFound
	}
	if err != nil {
		return Cart{}, false, err
	}
	if !restaurant.Open || !beforeCutoff(service.Now(), restaurant.AcceptUntilMinute) {
		return Cart{}, false, ErrRestaurantClosed
	}
	lines := make([]CartLine, 0, len(request.Lines))
	subtotal := int64(0)
	for _, requested := range request.Lines {
		line, err := pricePostgresLine(ctx, tx, actor, restaurant, requested)
		if err != nil {
			return Cart{}, false, err
		}
		lines = append(lines, line)
		subtotal += line.LineTotal.AmountMinor
	}
	if subtotal < restaurant.MinimumOrder.AmountMinor {
		return Cart{}, false, ErrInvalidRequest
	}
	policy, err := loadFoodPolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return Cart{}, false, err
	}
	now := service.Now()
	tax := subtotal * policy.taxBasisPoints / 10000
	value := Cart{ID: foodUUID("cart", actor.TenantID+":"+actor.Subject+":"+key), CustomerID: actor.Subject, Restaurant: publicRestaurant(restaurant), PostalCode: request.PostalCode, Revision: 1, Lines: lines, Subtotal: Money{AmountMinor: subtotal, Currency: restaurant.MinimumOrder.Currency}, DeliveryFee: restaurant.DeliveryFee, Tax: Money{AmountMinor: tax, Currency: restaurant.MinimumOrder.Currency}, Total: Money{AmountMinor: subtotal + restaurant.DeliveryFee.AmountMinor + tax, Currency: restaurant.MinimumOrder.Currency}, PricingVersion: policy.version, ExpiresAt: now.Add(policy.cartTTL), UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	encodedLines, _ := json.Marshal(value.Lines)
	if _, err := tx.Exec(ctx, `INSERT INTO food.carts (id,tenant_id,country,customer_identity_id,restaurant_id,revision,postal_code,lines,subtotal_minor,delivery_fee_minor,tax_minor,total_minor,currency,pricing_version,expires_at,updated_at) VALUES ($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, value.ID, actor.TenantID, actor.Country, actor.Subject, request.RestaurantID, request.PostalCode, encodedLines, value.Subtotal.AmountMinor, value.DeliveryFee.AmountMinor, value.Tax.AmountMinor, value.Total.AmountMinor, value.Total.Currency, value.PricingVersion, value.ExpiresAt, value.UpdatedAt); err != nil {
		return Cart{}, false, mapFoodError(err)
	}
	if err := storeFoodReplay(ctx, tx, actor, "cart", key, fingerprint, value, now); err != nil {
		return Cart{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Cart{}, false, mapFoodError(err)
	}
	return value, false, nil
}

func (service *PostgresService) CreateOrder(actor Actor, key string, request CreateOrderRequest) (Order, bool, error) {
	method := pay.Method(request.PaymentMethod)
	if !postgresFoodActor(actor) || !hasRole(actor, "CUSTOMER") || !validKey(key) || !postgresFoodUUID(request.CartID) || (method != pay.MethodWallet && method != pay.MethodRazorpay && method != pay.MethodPaystack) {
		return Order{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return Order{}, false, err
	}
	defer connection.Release()
	lockKey := actor.TenantID + ":" + actor.Subject + ":food-order:" + key
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return Order{}, false, err
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockKey)
	}()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Order{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fingerprint := digest(request)
	var replay Order
	if found, err := loadFoodReplay(ctx, tx, actor, "order", key, fingerprint, &replay); err != nil {
		return Order{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	cart, err := loadCart(ctx, tx, request.CartID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, ErrNotFound
	}
	if err != nil {
		return Order{}, false, err
	}
	if cart.CustomerID != actor.Subject || cart.tenantID != actor.TenantID || cart.country != actor.Country {
		return Order{}, false, ErrForbidden
	}
	if !cart.ExpiresAt.After(service.Now()) {
		return Order{}, false, ErrConflict
	}
	restaurant, err := loadRestaurant(ctx, tx, cart.Restaurant.ID, actor.TenantID, actor.Country, true)
	if err != nil {
		return Order{}, false, err
	}
	if !restaurant.Open || !beforeCutoff(service.Now(), restaurant.AcceptUntilMinute) {
		return Order{}, false, ErrRestaurantClosed
	}
	policy, err := loadFoodPolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return Order{}, false, err
	}
	orderID := foodUUID("order", actor.TenantID+":"+actor.Subject+":"+key)
	walletDebitID := ""
	if method == pay.MethodWallet {
		if service.wallet == nil {
			return Order{}, false, ErrForbidden
		}
		points := (cart.Total.AmountMinor + policy.walletPointValueMinor - 1) / policy.walletPointValueMinor
		entry, _, err := service.wallet.Redeem(wallet.Scope{TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject}, key+"-wallet", orderID, points)
		if err != nil {
			return Order{}, false, err
		}
		walletDebitID = entry.ID
	}
	paymentValue, _, err := service.payments.Create(pay.Scope{TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject}, key+"-payment", orderID, method, pay.Money{AmountMinor: cart.Total.AmountMinor, Currency: cart.Total.Currency})
	if err != nil {
		if walletDebitID != "" {
			_, _, _ = service.wallet.ReverseDebit(wallet.Scope{TenantID: actor.TenantID, Country: actor.Country, CustomerID: actor.Subject}, key+"-wallet-rollback", walletDebitID, orderID)
		}
		return Order{}, false, err
	}
	now := service.Now()
	value := Order{ID: orderID, CustomerID: actor.Subject, RestaurantID: restaurant.ID, Revision: 1, Status: StatusPendingRestaurant, Lines: cloneLines(cart.Lines), Subtotal: cart.Subtotal, DeliveryFee: cart.DeliveryFee, Tax: cart.Tax, Total: cart.Total, Payment: Payment{Method: request.PaymentMethod, Status: string(paymentValue.Status), Reference: paymentValue.ID}, PostalCode: cart.PostalCode, PricingVersion: cart.PricingVersion, AcceptBy: now.Add(policy.acceptanceTTL), AllowedActions: restaurantActions(StatusPendingRestaurant), Timeline: []OrderEvent{{Status: StatusPendingRestaurant, Actor: actor.Subject, CreatedAt: now}}, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country, walletDebitID: walletDebitID}
	if err := insertFoodOrder(ctx, tx, value, cart.ID); err != nil {
		return Order{}, false, err
	}
	if err := appendFoodTimeline(ctx, tx, value.ID, value.Timeline); err != nil {
		return Order{}, false, err
	}
	if err := storeFoodReplay(ctx, tx, actor, "order", key, fingerprint, value, now); err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, mapFoodError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Orders(actor Actor) ([]Order, error) {
	if !postgresFoodActor(actor) || !hasAnyRole(actor, "CUSTOMER", "RESTAURANT_VENDOR", "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	query, arguments := `SELECT o.id::text FROM food.orders o WHERE o.tenant_id=$1 AND o.country=$2`, []any{actor.TenantID, actor.Country}
	if hasRole(actor, "CUSTOMER") {
		query += ` AND o.customer_identity_id=$3`
		arguments = append(arguments, actor.Subject)
	} else if hasRole(actor, "RESTAURANT_VENDOR") {
		query += ` AND EXISTS(SELECT 1 FROM food.restaurants r WHERE r.id=o.restaurant_id AND r.owner_identity_id=$3)`
		arguments = append(arguments, actor.Subject)
	}
	query += ` ORDER BY o.created_at DESC LIMIT 500`
	rows, err := service.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	result := make([]Order, 0, len(ids))
	for _, id := range ids {
		value, err := loadFoodOrder(ctx, service.pool, id, false)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (service *PostgresService) Order(actor Actor, orderID string) (Order, error) {
	if !postgresFoodActor(actor) || !postgresFoodUUID(orderID) {
		return Order{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	value, err := loadFoodOrder(ctx, service.pool, orderID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	if err := service.authorizeOrder(ctx, actor, value); err != nil {
		return Order{}, err
	}
	return value, nil
}

func (service *PostgresService) RestaurantTransition(actor Actor, key, orderID string, revision int64, request RestaurantTransitionRequest) (Order, bool, error) {
	if !postgresFoodActor(actor) || !hasRole(actor, "RESTAURANT_VENDOR") || !validKey(key) {
		return Order{}, false, ErrForbidden
	}
	if request.Status == StatusRejected && len(strings.TrimSpace(request.Reason)) < 8 {
		return Order{}, false, ErrInvalidRequest
	}
	return service.mutateOrder(actor, key, orderID, revision, "restaurant-transition", request, func(ctx context.Context, tx pgx.Tx, value *Order) error {
		restaurant, err := loadRestaurant(ctx, tx, value.RestaurantID, actor.TenantID, actor.Country, false)
		if err != nil || restaurant.OwnerID != actor.Subject {
			return ErrForbidden
		}
		if !allowedRestaurantTransition(value.Status, request.Status) {
			return ErrInvalidTransition
		}
		value.Status = request.Status
		value.AllowedActions = restaurantActions(request.Status)
		if request.Status == StatusAccepted {
			readyAt := service.Now().Add(time.Duration(restaurant.PreparationMinutes) * time.Minute)
			value.EstimatedReadyAt = &readyAt
		}
		if request.Status == StatusRejected {
			value.RejectionReason = strings.TrimSpace(request.Reason)
			if err := service.compensate(value, key); err != nil {
				return err
			}
		}
		value.Timeline = append(value.Timeline, OrderEvent{Status: request.Status, Actor: actor.Subject, Reason: strings.TrimSpace(request.Reason), CreatedAt: service.Now()})
		return nil
	})
}

func (service *PostgresService) DispatchTransition(actor Actor, key, orderID string, revision int64, status OrderStatus) (Order, bool, error) {
	if !postgresFoodActor(actor) || !hasAnyRole(actor, "DISPATCH", "RIDER", "OPS_ADMIN", "SUPER_ADMIN") || !validKey(key) {
		return Order{}, false, ErrForbidden
	}
	return service.mutateOrder(actor, key, orderID, revision, "dispatch-transition", status, func(_ context.Context, _ pgx.Tx, value *Order) error {
		allowed := map[OrderStatus]OrderStatus{StatusReady: StatusRiderAssigned, StatusRiderAssigned: StatusPickedUp, StatusPickedUp: StatusDelivered}
		if allowed[value.Status] != status {
			return ErrInvalidTransition
		}
		value.Status = status
		value.AllowedActions = nil
		value.Timeline = append(value.Timeline, OrderEvent{Status: status, Actor: actor.Subject, CreatedAt: service.Now()})
		return nil
	})
}

type foodOrderMutation func(context.Context, pgx.Tx, *Order) error

func (service *PostgresService) mutateOrder(actor Actor, key, orderID string, revision int64, operation string, input any, mutation foodOrderMutation) (Order, bool, error) {
	if !postgresFoodUUID(orderID) || revision < 1 {
		return Order{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), foodOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Order{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation += ":" + orderID
	if err := foodCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Order{}, false, err
	}
	fingerprint := digest(input)
	var replay Order
	if found, err := loadFoodReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Order{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	value, err := loadFoodOrder(ctx, tx, orderID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, ErrNotFound
	}
	if err != nil {
		return Order{}, false, err
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return Order{}, false, ErrForbidden
	}
	if value.Revision != revision {
		return Order{}, false, ErrConflict
	}
	previousTimelineLength := len(value.Timeline)
	if err := mutation(ctx, tx, &value); err != nil {
		return Order{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.Now()
	if err := updateFoodOrder(ctx, tx, value); err != nil {
		return Order{}, false, err
	}
	if err := appendFoodTimeline(ctx, tx, value.ID, value.Timeline[previousTimelineLength:]); err != nil {
		return Order{}, false, err
	}
	if err := storeFoodReplay(ctx, tx, actor, operation, key, fingerprint, value, value.UpdatedAt); err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, mapFoodError(err)
	}
	return value, false, nil
}

func (service *PostgresService) compensate(value *Order, key string) error {
	paymentScope := pay.Scope{TenantID: value.tenantID, Country: value.country, CustomerID: value.CustomerID}
	if value.walletDebitID != "" {
		if service.wallet == nil {
			return ErrForbidden
		}
		if _, _, err := service.wallet.ReverseDebit(wallet.Scope{TenantID: value.tenantID, Country: value.country, CustomerID: value.CustomerID}, key+"-wallet-refund", value.walletDebitID, value.ID); err != nil && !errors.Is(err, wallet.ErrAlreadyReversed) {
			return err
		}
	}
	var paymentValue pay.Payment
	var err error
	if value.Payment.Status == string(pay.StatusCaptured) || value.Payment.Status == string(pay.StatusReconciled) {
		paymentValue, err = service.payments.RequestRefund(paymentScope, value.Payment.Reference, pay.Money{AmountMinor: value.Total.AmountMinor, Currency: value.Total.Currency})
	} else {
		paymentValue, err = service.payments.CancelUncaptured(paymentScope, value.Payment.Reference)
	}
	if err != nil {
		return err
	}
	value.Payment.Status, value.Payment.RefundState = string(paymentValue.Status), "SUBMITTED"
	return nil
}

func (service *PostgresService) authorizeOrder(ctx context.Context, actor Actor, value Order) error {
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return ErrForbidden
	}
	if hasRole(actor, "CUSTOMER") && value.CustomerID == actor.Subject || hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN", "RIDER") {
		return nil
	}
	if hasRole(actor, "RESTAURANT_VENDOR") {
		var owns bool
		if err := service.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM food.restaurants WHERE id=$1 AND owner_identity_id=$2)`, value.RestaurantID, actor.Subject).Scan(&owns); err == nil && owns {
			return nil
		}
	}
	return ErrForbidden
}

func (service *PostgresService) Expire(ctx context.Context, limit int) (int, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return 0, ErrInvalidRequest
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text,tenant_id::text,country,customer_identity_id::text,revision FROM food.orders WHERE status='PENDING_RESTAURANT' AND accept_by <= $1 ORDER BY accept_by LIMIT $2`, service.Now(), limit)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id, tenant, country, customer string
		revision                      int64
	}
	values := []candidate{}
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.id, &value.tenant, &value.country, &value.customer, &value.revision); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	rows.Close()
	processed := 0
	for _, candidate := range values {
		actor := Actor{TenantID: candidate.tenant, Country: candidate.country, Subject: candidate.customer, Roles: []string{"CUSTOMER"}}
		key := "food-timeout-" + strings.ReplaceAll(candidate.id, "-", "")
		_, replay, err := service.mutateOrder(actor, key, candidate.id, candidate.revision, "timeout", candidate.id, func(_ context.Context, _ pgx.Tx, value *Order) error {
			if value.Status != StatusPendingRestaurant || value.AcceptBy.After(service.Now()) {
				return ErrInvalidTransition
			}
			value.Status, value.RejectionReason, value.AllowedActions = StatusTimedOut, "Restaurant acceptance window expired", nil
			if err := service.compensate(value, key); err != nil {
				return err
			}
			value.Timeline = append(value.Timeline, OrderEvent{Status: StatusTimedOut, Actor: candidate.customer, Reason: value.RejectionReason, CreatedAt: service.Now()})
			return nil
		})
		if err == nil && !replay {
			processed++
		}
	}
	return processed, nil
}

type foodQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type foodPolicy struct {
	version               string
	cartTTL               time.Duration
	acceptanceTTL         time.Duration
	taxBasisPoints        int64
	walletPointValueMinor int64
}

func loadFoodPolicy(ctx context.Context, query foodQuerier, tenantID, country string) (foodPolicy, error) {
	var value foodPolicy
	var cartSeconds, acceptanceSeconds int64
	err := query.QueryRow(ctx, `SELECT version,cart_ttl_seconds,acceptance_ttl_seconds,tax_basis_points,wallet_point_value_minor FROM food.policies WHERE tenant_id=$1 AND country=$2`, tenantID, country).Scan(&value.version, &cartSeconds, &acceptanceSeconds, &value.taxBasisPoints, &value.walletPointValueMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return foodPolicy{}, ErrConflict
	}
	if err != nil {
		return foodPolicy{}, err
	}
	value.cartTTL, value.acceptanceTTL = time.Duration(cartSeconds)*time.Second, time.Duration(acceptanceSeconds)*time.Second
	return value, nil
}

type foodScanner interface{ Scan(...any) error }

func scanRestaurant(row foodScanner, tenantID, country string) (Restaurant, error) {
	var value Restaurant
	err := row.Scan(&value.ID, &value.OwnerID, &value.Name, &value.Cuisine, &value.PostalCodes, &value.Rating, &value.Verified, &value.Open, &value.AcceptUntilMinute, &value.PreparationMinutes, &value.DeliveryFee.AmountMinor, &value.MinimumOrder.AmountMinor, &value.DeliveryFee.Currency, &value.UpdatedAt)
	if err != nil {
		return Restaurant{}, err
	}
	value.MinimumOrder.Currency, value.tenantID, value.country = value.DeliveryFee.Currency, tenantID, country
	return value, nil
}

func loadRestaurant(ctx context.Context, query foodQuerier, restaurantID, tenantID, country string, forUpdate bool) (Restaurant, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	return scanRestaurant(query.QueryRow(ctx, `SELECT id::text,owner_identity_id::text,name,cuisine,postal_codes,rating::float8,verified,open,accept_until_minute,preparation_minutes,delivery_fee_minor,minimum_order_minor,currency,updated_at FROM food.restaurants WHERE id=$1 AND tenant_id=$2 AND country=$3`+suffix, restaurantID, tenantID, country), tenantID, country)
}

func loadMenuItem(ctx context.Context, query foodQuerier, itemID, tenantID, country string) (MenuItem, error) {
	var value MenuItem
	err := query.QueryRow(ctx, `SELECT m.id::text,m.restaurant_id::text,m.name,m.description,m.category,m.vegetarian,m.base_price_minor,m.currency,m.available,COALESCE(m.image_asset_id::text,'') FROM food.menu_items m JOIN food.restaurants r ON r.id=m.restaurant_id WHERE m.id=$1 AND r.tenant_id=$2 AND r.country=$3`, itemID, tenantID, country).Scan(&value.ID, &value.RestaurantID, &value.Name, &value.Description, &value.Category, &value.Vegetarian, &value.BasePrice.AmountMinor, &value.BasePrice.Currency, &value.Available, &value.ImageAssetID)
	if err != nil {
		return MenuItem{}, err
	}
	value.tenantID, value.country = tenantID, country
	groups, err := query.Query(ctx, `SELECT id::text,name,minimum,maximum,instructions FROM food.option_groups WHERE menu_item_id=$1 ORDER BY id`, itemID)
	if err != nil {
		return MenuItem{}, err
	}
	groupValues := []OptionGroup{}
	for groups.Next() {
		var group OptionGroup
		if err := groups.Scan(&group.ID, &group.Name, &group.Minimum, &group.Maximum, &group.Instructions); err != nil {
			groups.Close()
			return MenuItem{}, err
		}
		groupValues = append(groupValues, group)
	}
	groups.Close()
	for _, group := range groupValues {
		options, err := query.Query(ctx, `SELECT id::text,name,price_delta_minor,currency,available FROM food.options WHERE option_group_id=$1 ORDER BY id`, group.ID)
		if err != nil {
			return MenuItem{}, err
		}
		for options.Next() {
			var option Option
			if err := options.Scan(&option.ID, &option.Name, &option.PriceDelta.AmountMinor, &option.PriceDelta.Currency, &option.Available); err != nil {
				options.Close()
				return MenuItem{}, err
			}
			group.Options = append(group.Options, option)
		}
		options.Close()
		value.OptionGroups = append(value.OptionGroups, group)
	}
	return value, nil
}

func pricePostgresLine(ctx context.Context, query foodQuerier, actor Actor, restaurant Restaurant, request CartLineRequest) (CartLine, error) {
	if request.Quantity < 1 || request.Quantity > 20 || len(request.Note) > 240 || !postgresFoodUUID(request.MenuItemID) {
		return CartLine{}, ErrInvalidRequest
	}
	item, err := loadMenuItem(ctx, query, request.MenuItemID, actor.TenantID, actor.Country)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && item.RestaurantID != restaurant.ID {
		return CartLine{}, ErrNotFound
	}
	if err != nil {
		return CartLine{}, err
	}
	if !item.Available {
		return CartLine{}, ErrUnavailable
	}
	selected := map[string]bool{}
	for _, optionID := range request.OptionIDs {
		if !postgresFoodUUID(optionID) || selected[optionID] {
			return CartLine{}, ErrInvalidRequest
		}
		selected[optionID] = true
	}
	unit := item.BasePrice.AmountMinor
	options := []PricedOption{}
	for _, group := range item.OptionGroups {
		count := 0
		for _, option := range group.Options {
			if !selected[option.ID] {
				continue
			}
			if !option.Available {
				return CartLine{}, ErrUnavailable
			}
			count++
			unit += option.PriceDelta.AmountMinor
			options = append(options, PricedOption{ID: option.ID, Name: option.Name, PriceDelta: option.PriceDelta})
			delete(selected, option.ID)
		}
		if count < group.Minimum || count > group.Maximum {
			return CartLine{}, ErrInvalidRequest
		}
	}
	if len(selected) != 0 {
		return CartLine{}, ErrInvalidRequest
	}
	return CartLine{MenuItemID: item.ID, Name: item.Name, Quantity: request.Quantity, UnitPrice: Money{AmountMinor: unit, Currency: item.BasePrice.Currency}, Options: options, Note: strings.TrimSpace(request.Note), LineTotal: Money{AmountMinor: unit * int64(request.Quantity), Currency: item.BasePrice.Currency}}, nil
}

func loadCart(ctx context.Context, query foodQuerier, cartID string, forUpdate bool) (Cart, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var value Cart
	var lines []byte
	var restaurantID string
	err := query.QueryRow(ctx, `SELECT id::text,tenant_id::text,country,customer_identity_id::text,restaurant_id::text,revision,postal_code,lines,subtotal_minor,delivery_fee_minor,tax_minor,total_minor,currency,pricing_version,expires_at,updated_at FROM food.carts WHERE id=$1`+suffix, cartID).Scan(&value.ID, &value.tenantID, &value.country, &value.CustomerID, &restaurantID, &value.Revision, &value.PostalCode, &lines, &value.Subtotal.AmountMinor, &value.DeliveryFee.AmountMinor, &value.Tax.AmountMinor, &value.Total.AmountMinor, &value.Total.Currency, &value.PricingVersion, &value.ExpiresAt, &value.UpdatedAt)
	if err != nil {
		return Cart{}, err
	}
	value.Subtotal.Currency, value.DeliveryFee.Currency, value.Tax.Currency = value.Total.Currency, value.Total.Currency, value.Total.Currency
	if err := json.Unmarshal(lines, &value.Lines); err != nil {
		return Cart{}, err
	}
	value.Restaurant, err = loadRestaurant(ctx, query, restaurantID, value.tenantID, value.country, false)
	if err != nil {
		return Cart{}, err
	}
	value.Restaurant = publicRestaurant(value.Restaurant)
	return value, nil
}

func insertFoodOrder(ctx context.Context, tx pgx.Tx, value Order, cartID string) error {
	lines, _ := json.Marshal(value.Lines)
	var walletDebit any
	if value.walletDebitID != "" {
		walletDebit = value.walletDebitID
	}
	_, err := tx.Exec(ctx, `INSERT INTO food.orders (id,tenant_id,country,customer_identity_id,restaurant_id,revision,status,lines,subtotal_minor,delivery_fee_minor,tax_minor,total_minor,currency,pricing_version,payment_reference,payment_status,accept_by,estimated_ready_at,rejection_reason,created_at,updated_at,source_cart_id,postal_code,payment_method,refund_state,wallet_debit_entry_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,''),$20,$21,$22,$23,$24,NULLIF($25,''),$26)`, value.ID, value.tenantID, value.country, value.CustomerID, value.RestaurantID, value.Revision, value.Status, lines, value.Subtotal.AmountMinor, value.DeliveryFee.AmountMinor, value.Tax.AmountMinor, value.Total.AmountMinor, value.Total.Currency, value.PricingVersion, value.Payment.Reference, value.Payment.Status, value.AcceptBy, value.EstimatedReadyAt, value.RejectionReason, value.CreatedAt, value.UpdatedAt, cartID, value.PostalCode, value.Payment.Method, value.Payment.RefundState, walletDebit)
	if err != nil {
		return mapFoodError(err)
	}
	return nil
}

func loadFoodOrder(ctx context.Context, query foodQuerier, orderID string, forUpdate bool) (Order, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var value Order
	var lines []byte
	var rejection, refundState, walletDebit *string
	err := query.QueryRow(ctx, `SELECT id::text,tenant_id::text,country,customer_identity_id::text,restaurant_id::text,revision,status,lines,subtotal_minor,delivery_fee_minor,tax_minor,total_minor,currency,pricing_version,payment_reference,payment_status,accept_by,estimated_ready_at,rejection_reason,created_at,updated_at,postal_code,payment_method,refund_state,wallet_debit_entry_id::text FROM food.orders WHERE id=$1`+suffix, orderID).Scan(&value.ID, &value.tenantID, &value.country, &value.CustomerID, &value.RestaurantID, &value.Revision, &value.Status, &lines, &value.Subtotal.AmountMinor, &value.DeliveryFee.AmountMinor, &value.Tax.AmountMinor, &value.Total.AmountMinor, &value.Total.Currency, &value.PricingVersion, &value.Payment.Reference, &value.Payment.Status, &value.AcceptBy, &value.EstimatedReadyAt, &rejection, &value.CreatedAt, &value.UpdatedAt, &value.PostalCode, &value.Payment.Method, &refundState, &walletDebit)
	if err != nil {
		return Order{}, err
	}
	value.Subtotal.Currency, value.DeliveryFee.Currency, value.Tax.Currency = value.Total.Currency, value.Total.Currency, value.Total.Currency
	if rejection != nil {
		value.RejectionReason = *rejection
	}
	if refundState != nil {
		value.Payment.RefundState = *refundState
	}
	if walletDebit != nil {
		value.walletDebitID = *walletDebit
	}
	if err := json.Unmarshal(lines, &value.Lines); err != nil {
		return Order{}, err
	}
	rows, err := query.Query(ctx, `SELECT status,COALESCE(actor_identity_id::text,''),COALESCE(reason,''),created_at FROM food.order_timeline WHERE order_id=$1 ORDER BY id`, orderID)
	if err != nil {
		return Order{}, err
	}
	for rows.Next() {
		var event OrderEvent
		if err := rows.Scan(&event.Status, &event.Actor, &event.Reason, &event.CreatedAt); err != nil {
			rows.Close()
			return Order{}, err
		}
		value.Timeline = append(value.Timeline, event)
	}
	rows.Close()
	value.AllowedActions = restaurantActions(value.Status)
	return value, nil
}

func updateFoodOrder(ctx context.Context, tx pgx.Tx, value Order) error {
	_, err := tx.Exec(ctx, `UPDATE food.orders SET revision=$2,status=$3,payment_status=$4,estimated_ready_at=$5,rejection_reason=NULLIF($6,''),refund_state=NULLIF($7,''),updated_at=$8 WHERE id=$1`, value.ID, value.Revision, value.Status, value.Payment.Status, value.EstimatedReadyAt, value.RejectionReason, value.Payment.RefundState, value.UpdatedAt)
	return err
}

func appendFoodTimeline(ctx context.Context, tx pgx.Tx, orderID string, values []OrderEvent) error {
	for _, value := range values {
		var actor any
		if postgresFoodUUID(value.Actor) {
			actor = value.Actor
		}
		if _, err := tx.Exec(ctx, `INSERT INTO food.order_timeline (order_id,status,actor_identity_id,reason,created_at) VALUES ($1,$2,$3,NULLIF($4,''),$5)`, orderID, value.Status, actor, value.Reason, value.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func foodCommandLock(ctx context.Context, tx pgx.Tx, actor Actor, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":"+actor.Subject+":"+operation+":"+key)
	return err
}

func loadFoodReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, destination any) (bool, error) {
	var stored string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM food.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation=$4 AND idempotency_key=$5`, actor.TenantID, actor.Country, actor.Subject, operation, key).Scan(&stored, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != fingerprint {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return false, err
	}
	return true, nil
}

func storeFoodReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, value any, now time.Time) error {
	payload, _ := json.Marshal(value)
	_, err := tx.Exec(ctx, `INSERT INTO food.idempotency_records (tenant_id,country,subject_id,operation,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.Subject, operation, key, fingerprint, payload, now)
	return err
}

func foodUUID(kind, source string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("planext4u:food:"+kind+":"+source)).String()
}
func postgresFoodUUID(value string) bool { return uuid.Validate(strings.TrimSpace(value)) == nil }
func postgresFoodActor(actor Actor) bool {
	return postgresFoodUUID(actor.TenantID) && postgresFoodUUID(actor.Subject) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(actor.Country) && len(actor.Roles) > 0
}

func mapFoodError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505", "40001", "40P01":
			return ErrConflict
		}
	}
	return err
}
