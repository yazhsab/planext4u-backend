package booking

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

const bookingOperationTimeout = 30 * time.Second

// PostgresService provides the production booking runtime. Capacity checks,
// holds, lifecycle revisions, timelines and command replays are committed in
// the same PostgreSQL transaction. Financial operations are delegated to the
// durable payment and wallet runtimes under a per-command advisory lock.
type PostgresService struct {
	pool      *pgxpool.Pool
	payments  PaymentService
	wallet    WalletService
	clock     func() time.Time
	otpCipher cipher.AEAD
}

func NewPostgresService(pool *pgxpool.Pool, payments PaymentService, walletService WalletService, clock func() time.Time, otpKey []byte) (*PostgresService, error) {
	if pool == nil || payments == nil || clock == nil || len(otpKey) != 32 {
		return nil, ErrInvalidRequest
	}
	block, err := aes.NewCipher(otpKey)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, payments: payments, wallet: walletService, clock: clock, otpCipher: aead}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('booking.offerings') IS NOT NULL
		   AND to_regclass('booking.policies') IS NOT NULL
		   AND to_regclass('booking.slot_holds') IS NOT NULL
		   AND to_regclass('booking.bookings') IS NOT NULL
		   AND to_regclass('booking.idempotency_records') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check booking schema readiness: %w", err)
	}
	if !ready {
		return errors.New("booking schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Offerings(scope Scope, postalCode, categoryID string) ([]Offering, error) {
	if !postgresBookingScope(scope) || !safePostalCode(postalCode) || (categoryID != "" && !postgresUUID(categoryID)) {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `
		SELECT o.id::text,o.provider_id::text,o.provider_name,o.category_id::text,o.name,o.summary,
		       o.duration_minutes,o.price_minor,o.advance_minor,o.currency,o.payment_mode,
		       o.verified_provider,o.rating_average::float8,o.completed_bookings,o.live_engagements,
		       o.cancellation_policy_ref,o.reschedule_policy_ref,o.active,
		       COALESCE(array_agg(z.postal_code ORDER BY z.postal_code) FILTER (WHERE z.postal_code IS NOT NULL),'{}')
		FROM booking.offerings o
		JOIN booking.service_zones requested_zone ON requested_zone.offering_id=o.id AND upper(requested_zone.postal_code)=upper($3)
		LEFT JOIN booking.service_zones z ON z.offering_id=o.id
		WHERE o.tenant_id=$1 AND o.country=$2 AND o.active=true AND ($4='' OR o.category_id::text=$4)
		GROUP BY o.id ORDER BY o.verified_provider DESC,o.rating_average DESC,o.id`, scope.TenantID, scope.Country, postalCode, categoryID)
	if err != nil {
		return nil, fmt.Errorf("list booking offerings: %w", err)
	}
	defer rows.Close()
	result := []Offering{}
	for rows.Next() {
		value, err := scanOffering(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate booking offerings: %w", err)
	}
	return result, nil
}

func (service *PostgresService) Offering(scope Scope, offeringID, postalCode string) (Offering, error) {
	if !postgresBookingScope(scope) || !postgresUUID(offeringID) || !safePostalCode(postalCode) {
		return Offering{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	row := service.pool.QueryRow(ctx, `
		SELECT o.id::text,o.provider_id::text,o.provider_name,o.category_id::text,o.name,o.summary,
		       o.duration_minutes,o.price_minor,o.advance_minor,o.currency,o.payment_mode,
		       o.verified_provider,o.rating_average::float8,o.completed_bookings,o.live_engagements,
		       o.cancellation_policy_ref,o.reschedule_policy_ref,o.active,
		       COALESCE(array_agg(z.postal_code ORDER BY z.postal_code) FILTER (WHERE z.postal_code IS NOT NULL),'{}')
		FROM booking.offerings o
		JOIN booking.service_zones requested_zone ON requested_zone.offering_id=o.id AND upper(requested_zone.postal_code)=upper($4)
		LEFT JOIN booking.service_zones z ON z.offering_id=o.id
		WHERE o.id=$3 AND o.tenant_id=$1 AND o.country=$2 AND o.active=true GROUP BY o.id`, scope.TenantID, scope.Country, offeringID, postalCode)
	value, err := scanOffering(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Offering{}, ErrOfferingNotFound
	}
	return value, err
}

func (service *PostgresService) Slots(scope Scope, offeringID string, from, to time.Time) ([]Slot, error) {
	if !postgresBookingScope(scope) || !postgresUUID(offeringID) || from.IsZero() || to.IsZero() || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	var exists bool
	if err := service.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM booking.offerings WHERE id=$1 AND tenant_id=$2 AND country=$3 AND active=true)`, offeringID, scope.TenantID, scope.Country).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check booking offering: %w", err)
	}
	if !exists {
		return nil, ErrOfferingNotFound
	}
	rows, err := service.pool.Query(ctx, slotQuery+` WHERE s.offering_id=$1 AND o.tenant_id=$2 AND o.country=$3 AND s.starts_at >= $4 AND s.starts_at < $5 AND s.starts_at > $6 ORDER BY s.starts_at`, offeringID, scope.TenantID, scope.Country, from.UTC(), to.UTC(), service.Now())
	if err != nil {
		return nil, fmt.Errorf("list booking slots: %w", err)
	}
	defer rows.Close()
	result := []Slot{}
	for rows.Next() {
		value, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate booking slots: %w", err)
	}
	return result, nil
}

func (service *PostgresService) Hold(scope Scope, idempotencyKey, slotID, postalCode string) (SlotHold, bool, error) {
	if !postgresBookingScope(scope) || !validIdempotencyKey(idempotencyKey) || !postgresUUID(slotID) || !safePostalCode(postalCode) {
		return SlotHold{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	// A row lock on the slot serializes the capacity count. READ COMMITTED is
	// intentional here so a waiter observes the winner's newly committed hold.
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return SlotHold{}, false, fmt.Errorf("begin booking hold: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := bookingCommandLock(ctx, tx, scope.TenantID, scope.CustomerID, "hold:"+idempotencyKey); err != nil {
		return SlotHold{}, false, err
	}
	fingerprint := bookingFingerprint(slotID + "\x00" + strings.ToUpper(strings.TrimSpace(postalCode)))
	var replay SlotHold
	if found, err := loadBookingReplay(ctx, tx, scope.TenantID, scope.Country, scope.CustomerID, "hold:"+idempotencyKey, fingerprint, &replay); err != nil {
		return SlotHold{}, false, err
	} else if found {
		replay.scope = scope
		return replay, true, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE booking.slot_holds SET status='EXPIRED' WHERE slot_id=$1 AND status='HELD' AND expires_at <= $2`, slotID, service.Now()); err != nil {
		return SlotHold{}, false, fmt.Errorf("expire booking holds: %w", err)
	}
	var offeringID string
	var capacity, reserved int
	err = tx.QueryRow(ctx, `
		SELECT s.offering_id::text,s.capacity,
		       (SELECT count(*) FROM booking.slot_holds h WHERE h.slot_id=s.id AND h.status='HELD' AND h.expires_at>$5) +
		       (SELECT count(*) FROM booking.bookings b WHERE b.slot_id=s.id AND b.status NOT IN ('CANCELLED','DECLINED'))
		FROM booking.slots s
		JOIN booking.offerings o ON o.id=s.offering_id
		JOIN booking.service_zones z ON z.offering_id=o.id AND upper(z.postal_code)=upper($4)
		WHERE s.id=$1 AND o.tenant_id=$2 AND o.country=$3 AND o.active=true AND s.starts_at>$5
		FOR UPDATE OF s`, slotID, scope.TenantID, scope.Country, postalCode, service.Now()).Scan(&offeringID, &capacity, &reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return SlotHold{}, false, ErrSlotNotFound
	}
	if err != nil {
		return SlotHold{}, false, fmt.Errorf("lock booking slot: %w", err)
	}
	if reserved >= capacity {
		return SlotHold{}, false, ErrSlotUnavailable
	}
	policy, err := loadPolicy(ctx, tx, scope.TenantID, scope.Country)
	if err != nil {
		return SlotHold{}, false, err
	}
	now := service.Now()
	value := SlotHold{ID: bookingUUID("hold", scopeKey(scope)+":"+idempotencyKey), SlotID: slotID, OfferingID: offeringID, Status: HoldActive, ExpiresAt: now.Add(policy.HoldTTL), CreatedAt: now, AllowedActions: []string{"CREATE_BOOKING", "RELEASE"}, scope: scope}
	if _, err := tx.Exec(ctx, `INSERT INTO booking.slot_holds (id,tenant_id,country,customer_identity_id,slot_id,status,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, value.ID, scope.TenantID, scope.Country, scope.CustomerID, slotID, value.Status, value.ExpiresAt, value.CreatedAt); err != nil {
		return SlotHold{}, false, fmt.Errorf("insert booking hold: %w", err)
	}
	if err := storeBookingReplay(ctx, tx, scope.TenantID, scope.Country, scope.CustomerID, "hold:"+idempotencyKey, fingerprint, value, now); err != nil {
		return SlotHold{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SlotHold{}, false, mapBookingCommit(err)
	}
	return value, false, nil
}

func (service *PostgresService) ReleaseHold(scope Scope, holdID string) error {
	if !postgresBookingScope(scope) || !postgresUUID(holdID) {
		return ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	var status HoldStatus
	err := service.pool.QueryRow(ctx, `UPDATE booking.slot_holds SET status=CASE WHEN status='HELD' THEN 'RELEASED' ELSE status END WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4 RETURNING status`, holdID, scope.TenantID, scope.Country, scope.CustomerID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHoldNotFound
	}
	if err != nil {
		return fmt.Errorf("release booking hold: %w", err)
	}
	if status != HoldReleased {
		return ErrInvalidTransition
	}
	return nil
}

func (service *PostgresService) Create(ctx context.Context, scope Scope, idempotencyKey string, request CreateBookingRequest) (Booking, bool, error) {
	if ctx == nil || !postgresBookingScope(scope) || !validIdempotencyKey(idempotencyKey) || !postgresUUID(request.HoldID) || !validBookingMethod(request.PaymentMethod) {
		return Booking{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, bookingOperationTimeout)
	defer cancel()
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return Booking{}, false, fmt.Errorf("acquire booking command connection: %w", err)
	}
	defer connection.Release()
	lockKey := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID + ":create:" + idempotencyKey
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return Booking{}, false, fmt.Errorf("lock booking creation: %w", err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockKey)
	}()
	fingerprint := bookingFingerprint(request.HoldID + "\x00" + string(request.PaymentMethod))
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Booking{}, false, fmt.Errorf("begin booking creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var replay Booking
	if found, err := loadBookingReplay(ctx, tx, scope.TenantID, scope.Country, scope.CustomerID, "create:"+idempotencyKey, fingerprint, &replay); err != nil {
		return Booking{}, false, err
	} else if found {
		replay.scope = scope
		return replay, true, nil
	}
	var holdStatus HoldStatus
	var expiresAt time.Time
	var slotID, offeringID string
	err = tx.QueryRow(ctx, `SELECT status,expires_at,slot_id::text FROM booking.slot_holds WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4 FOR UPDATE`, request.HoldID, scope.TenantID, scope.Country, scope.CustomerID).Scan(&holdStatus, &expiresAt, &slotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, false, ErrHoldNotFound
	}
	if err != nil {
		return Booking{}, false, fmt.Errorf("load booking hold: %w", err)
	}
	if holdStatus == HoldExpired || !expiresAt.After(service.Now()) {
		return Booking{}, false, ErrHoldExpired
	}
	if holdStatus != HoldActive {
		return Booking{}, false, ErrInvalidTransition
	}
	offering, err := loadOfferingBySlot(ctx, tx, scope, slotID)
	if err != nil {
		return Booking{}, false, err
	}
	offeringID = offering.ID
	slot, err := loadSlot(ctx, tx, slotID, scope, true, service.Now())
	if err != nil {
		return Booking{}, false, err
	}
	policy, err := loadPolicy(ctx, tx, scope.TenantID, scope.Country)
	if err != nil {
		return Booking{}, false, err
	}
	amountDue := offering.Price
	if offering.PaymentMode == PaymentAdvance {
		amountDue = offering.Advance
	}
	bookingID := bookingUUID("booking", scopeKey(scope)+":"+idempotencyKey)
	var walletDebit *wallet.LedgerEntry
	if request.PaymentMethod == payment.MethodWallet {
		if service.wallet == nil {
			return Booking{}, false, ErrPolicyDenied
		}
		points := (amountDue.AmountMinor + policy.WalletPointValueMinor - 1) / policy.WalletPointValueMinor
		entry, _, err := service.wallet.Redeem(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet", bookingID, points)
		if err != nil {
			return Booking{}, false, err
		}
		walletDebit = &entry
	}
	paymentValue, _, err := service.payments.Create(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-payment", bookingID, request.PaymentMethod, payment.Money{AmountMinor: amountDue.AmountMinor, Currency: amountDue.Currency})
	if err != nil {
		if walletDebit != nil {
			_, _, _ = service.wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet-rollback", walletDebit.ID, bookingID)
		}
		return Booking{}, false, err
	}
	now := service.Now()
	status := StatusPendingPayment
	if paymentCaptured(paymentValue.Status) {
		status = StatusRequested
	}
	value := Booking{ID: bookingID, Revision: 1, Status: status, Offering: offering, Slot: slot, Price: offering.Price, AmountDue: amountDue, Payment: paymentValue, RescheduleCount: 0, FreeReschedulesLeft: policy.MaximumFreeReschedules, AllowedActions: customerActions(Booking{Status: status}), Timeline: []TimelineEvent{{Status: status, Actor: "CUSTOMER", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now, scope: scope, walletDebit: walletDebit, paymentExpiresAt: expiresAt}
	if err := insertBooking(ctx, tx, value); err != nil {
		return Booking{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE booking.slot_holds SET status='CONSUMED' WHERE id=$1`, request.HoldID); err != nil {
		return Booking{}, false, fmt.Errorf("consume booking hold: %w", err)
	}
	if err := insertTimeline(ctx, tx, value.ID, value.Timeline); err != nil {
		return Booking{}, false, err
	}
	if err := storeBookingReplay(ctx, tx, scope.TenantID, scope.Country, scope.CustomerID, "create:"+idempotencyKey, fingerprint, value, now); err != nil {
		return Booking{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Booking{}, false, mapBookingCommit(err)
	}
	_ = offeringID
	return value, false, nil
}

func (service *PostgresService) ConfirmPayment(scope Scope, idempotencyKey, bookingID string, expectedRevision int64) (Booking, bool, error) {
	return service.mutateCustomer(scope, idempotencyKey, bookingID, expectedRevision, "confirm-payment", bookingID, func(ctx context.Context, tx pgx.Tx, value *Booking) error {
		if value.Status == StatusRequested {
			return nil
		}
		if value.Status != StatusPendingPayment {
			return ErrInvalidTransition
		}
		paymentValue, err := service.payments.Get(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, value.Payment.ID)
		if err != nil || !paymentCaptured(paymentValue.Status) {
			return ErrPolicyDenied
		}
		value.Payment = paymentValue
		service.transition(value, StatusRequested, "PAYMENT", "")
		return nil
	})
}

func (service *PostgresService) Reschedule(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, request RescheduleRequest) (Booking, bool, error) {
	reason := strings.TrimSpace(request.Reason)
	if !postgresUUID(request.HoldID) || len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateCustomer(scope, idempotencyKey, bookingID, expectedRevision, "reschedule", request.HoldID+"\x00"+reason, func(ctx context.Context, tx pgx.Tx, value *Booking) error {
		if value.Status != StatusRequested && value.Status != StatusAccepted {
			return ErrInvalidTransition
		}
		policy, err := loadPolicy(ctx, tx, scope.TenantID, scope.Country)
		if err != nil {
			return err
		}
		if value.RescheduleCount >= policy.MaximumFreeReschedules || value.Slot.StartsAt.Sub(service.Now()) < policy.CancellationCutoff {
			return ErrPolicyDenied
		}
		var status HoldStatus
		var expiresAt time.Time
		var slotID, offeringID string
		err = tx.QueryRow(ctx, `SELECT h.status,h.expires_at,h.slot_id::text,s.offering_id::text FROM booking.slot_holds h JOIN booking.slots s ON s.id=h.slot_id WHERE h.id=$1 AND h.tenant_id=$2 AND h.country=$3 AND h.customer_identity_id=$4 FOR UPDATE OF h`, request.HoldID, scope.TenantID, scope.Country, scope.CustomerID).Scan(&status, &expiresAt, &slotID, &offeringID)
		if errors.Is(err, pgx.ErrNoRows) || status != HoldActive || !expiresAt.After(service.Now()) || offeringID != value.Offering.ID || slotID == value.Slot.ID {
			return ErrSlotUnavailable
		}
		if err != nil {
			return fmt.Errorf("lock reschedule hold: %w", err)
		}
		slot, err := loadSlot(ctx, tx, slotID, scope, true, service.Now())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE booking.slot_holds SET status='CONSUMED' WHERE id=$1`, request.HoldID); err != nil {
			return fmt.Errorf("consume reschedule hold: %w", err)
		}
		value.Slot = slot
		value.RescheduleCount++
		value.FreeReschedulesLeft = policy.MaximumFreeReschedules - value.RescheduleCount
		service.transition(value, StatusRescheduleRequested, "CUSTOMER", reason)
		service.transition(value, StatusRequested, "SYSTEM", "Reschedule accepted")
		return nil
	})
}

func (service *PostgresService) Cancel(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateCustomer(scope, idempotencyKey, bookingID, expectedRevision, "cancel", reason, func(ctx context.Context, tx pgx.Tx, value *Booking) error {
		if value.Status != StatusPendingPayment && value.Status != StatusRequested && value.Status != StatusAccepted {
			return ErrInvalidTransition
		}
		policy, err := loadPolicy(ctx, tx, scope.TenantID, scope.Country)
		if err != nil {
			return err
		}
		if value.Slot.StartsAt.Sub(service.Now()) < policy.CancellationCutoff {
			return ErrPolicyDenied
		}
		service.transition(value, StatusCancelRequested, "CUSTOMER", reason)
		paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
		if value.walletDebit != nil {
			if service.wallet == nil {
				return ErrPolicyDenied
			}
			if _, _, err := service.wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet-refund", value.walletDebit.ID, value.ID); err != nil && !errors.Is(err, wallet.ErrAlreadyReversed) {
				return err
			}
			value.Payment, err = service.payments.RequestRefund(paymentScope, value.Payment.ID, payment.Money{AmountMinor: value.AmountDue.AmountMinor, Currency: value.AmountDue.Currency})
		} else if paymentCaptured(value.Payment.Status) {
			value.Payment, err = service.payments.RequestRefund(paymentScope, value.Payment.ID, payment.Money{AmountMinor: value.AmountDue.AmountMinor, Currency: value.AmountDue.Currency})
		} else {
			value.Payment, err = service.payments.CancelUncaptured(paymentScope, value.Payment.ID)
		}
		if err != nil {
			return err
		}
		service.transition(value, StatusCancelled, "SYSTEM", "Financial compensation recorded")
		return nil
	})
}

func (service *PostgresService) ProviderTransition(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, target Status, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	return service.mutateActor(actor, idempotencyKey, bookingID, expectedRevision, "provider-transition", string(target)+"\x00"+reason, func(value Booking) bool { return providerOwns(actor, value) }, func(ctx context.Context, tx pgx.Tx, value *Booking) error {
		if !validProviderTransition(value.Status, target) {
			return ErrInvalidTransition
		}
		if target == StatusAccepted {
			policy, err := loadPolicy(ctx, tx, value.scope.TenantID, value.scope.Country)
			if err != nil {
				return err
			}
			otp := deterministicOTP(value.ID, service.Now())
			value.StartOTP = otp
			value.startOTPDigest = sha256.Sum256([]byte(otp))
			value.otpExpiresAt = service.Now().Add(policy.StartOTPValidity)
			if value.otpExpiresAt.After(value.Slot.EndsAt) {
				value.otpExpiresAt = value.Slot.EndsAt
			}
		}
		service.transition(value, target, "PROVIDER", reason)
		return nil
	})
}

func (service *PostgresService) Start(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, otp string) (Booking, bool, error) {
	if len(otp) != 6 {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateActor(actor, idempotencyKey, bookingID, expectedRevision, "service-start", otp, func(value Booking) bool { return providerOwns(actor, value) }, func(_ context.Context, _ pgx.Tx, value *Booking) error {
		if value.Status != StatusStartOTPRequired {
			return ErrInvalidTransition
		}
		provided := sha256.Sum256([]byte(otp))
		if service.Now().After(value.otpExpiresAt) || subtle.ConstantTimeCompare(provided[:], value.startOTPDigest[:]) != 1 {
			return ErrOTPInvalid
		}
		value.StartOTP = ""
		value.startOTPDigest = [32]byte{}
		value.otpExpiresAt = time.Time{}
		service.transition(value, StatusInProgress, "PROVIDER", "Start OTP verified")
		return nil
	})
}

func (service *PostgresService) Complete(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, photoAssetID string) (Booking, bool, error) {
	if !postgresUUID(photoAssetID) {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateActor(actor, idempotencyKey, bookingID, expectedRevision, "service-complete", photoAssetID, func(value Booking) bool { return providerOwns(actor, value) }, func(_ context.Context, _ pgx.Tx, value *Booking) error {
		if value.Status != StatusCompletionEvidenceRequired {
			return ErrInvalidTransition
		}
		value.CompletionEvidence = &CompletionEvidence{PhotoAssetID: photoAssetID, CapturedAt: service.Now(), SubmittedBy: actor.Subject}
		service.transition(value, StatusCompletedPendingConfirmation, "PROVIDER", "Completion evidence submitted")
		return nil
	})
}

func (service *PostgresService) ConfirmCompletion(scope Scope, idempotencyKey, bookingID string, expectedRevision int64) (Booking, bool, error) {
	return service.mutateCustomer(scope, idempotencyKey, bookingID, expectedRevision, "confirm-completion", bookingID, func(_ context.Context, _ pgx.Tx, value *Booking) error {
		if value.Status != StatusCompletedPendingConfirmation || value.CompletionEvidence == nil {
			return ErrEvidenceRequired
		}
		service.transition(value, StatusCompleted, "CUSTOMER", "Completion confirmed")
		return nil
	})
}

func (service *PostgresService) NoShow(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateActor(actor, idempotencyKey, bookingID, expectedRevision, "no-show", reason, func(value Booking) bool { return customerOwns(actor, value) || providerOwns(actor, value) }, func(_ context.Context, _ pgx.Tx, value *Booking) error {
		if value.Status != StatusAccepted && value.Status != StatusProviderEnRoute && value.Status != StatusArrived && value.Status != StatusStartOTPRequired {
			return ErrInvalidTransition
		}
		target := StatusProviderNoShow
		if providerOwns(actor, *value) {
			target = StatusCustomerNoShow
		}
		service.transition(value, target, roleLabel(actor), reason)
		return nil
	})
}

func (service *PostgresService) Dispute(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateActor(actor, idempotencyKey, bookingID, expectedRevision, "booking-dispute", reason, func(value Booking) bool { return customerOwns(actor, value) || providerOwns(actor, value) }, func(ctx context.Context, tx pgx.Tx, value *Booking) error {
		if value.Status != StatusCustomerNoShow && value.Status != StatusProviderNoShow && value.Status != StatusCompletedPendingConfirmation && value.Status != StatusCompleted {
			return ErrInvalidTransition
		}
		if _, err := tx.Exec(ctx, `INSERT INTO booking.disputes (id,booking_id,opened_by,reason,status,created_at,updated_at) VALUES ($1,$2,$3,$4,'OPEN',$5,$5)`, bookingUUID("dispute", value.ID+":"+idempotencyKey), value.ID, actor.Subject, reason, service.Now()); err != nil {
			return fmt.Errorf("insert booking dispute: %w", err)
		}
		service.transition(value, StatusDisputed, roleLabel(actor), reason)
		return nil
	})
}

func (service *PostgresService) Get(actor Actor, bookingID string) (Booking, error) {
	if !postgresBookingActor(actor) || !postgresUUID(bookingID) {
		return Booking{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	value, err := service.loadBooking(ctx, service.pool, bookingID, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!customerOwns(actor, value) && !providerOwns(actor, value) && !hasRole(actor, "ADMIN")) {
		return Booking{}, ErrBookingNotFound
	}
	if err != nil {
		return Booking{}, err
	}
	if !customerOwns(actor, value) {
		value.StartOTP = ""
	}
	return value, nil
}

func (service *PostgresService) List(actor Actor) ([]Booking, error) {
	if !postgresBookingActor(actor) {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	query := `SELECT id::text FROM booking.bookings WHERE tenant_id=$1 AND country=$2 AND (customer_identity_id=$3 OR provider_id=$3)`
	if hasRole(actor, "ADMIN") {
		query = `SELECT id::text FROM booking.bookings WHERE tenant_id=$1 AND country=$2 AND ($3::uuid IS NOT NULL)`
	}
	rows, err := service.pool.Query(ctx, query+` ORDER BY created_at DESC LIMIT 500`, actor.TenantID, actor.Country, actor.Subject)
	if err != nil {
		return nil, fmt.Errorf("list booking identifiers: %w", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan booking identifier: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	result := make([]Booking, 0, len(ids))
	for _, id := range ids {
		value, err := service.loadBooking(ctx, service.pool, id, false)
		if err != nil {
			return nil, err
		}
		if !customerOwns(actor, value) {
			value.StartOTP = ""
		}
		result = append(result, value)
	}
	return result, nil
}

type bookingMutation func(context.Context, pgx.Tx, *Booking) error
type bookingAuthorization func(Booking) bool

func (service *PostgresService) mutateCustomer(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, action, detail string, mutation bookingMutation) (Booking, bool, error) {
	if !postgresBookingScope(scope) {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutate(scope.TenantID, scope.Country, scope.CustomerID, idempotencyKey, bookingID, expectedRevision, action, detail, func(value Booking) bool { return value.scope == scope }, mutation)
}

func (service *PostgresService) mutateActor(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, action, detail string, authorize bookingAuthorization, mutation bookingMutation) (Booking, bool, error) {
	if !postgresBookingActor(actor) {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutate(actor.TenantID, actor.Country, actor.Subject, idempotencyKey, bookingID, expectedRevision, action, detail, authorize, mutation)
}

func (service *PostgresService) mutate(tenantID, country, subjectID, idempotencyKey, bookingID string, expectedRevision int64, action, detail string, authorize bookingAuthorization, mutation bookingMutation) (Booking, bool, error) {
	if !validIdempotencyKey(idempotencyKey) || !postgresUUID(bookingID) || expectedRevision < 1 || authorize == nil || mutation == nil {
		return Booking{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), bookingOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Booking{}, false, fmt.Errorf("begin booking mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	commandKey := action + ":" + idempotencyKey
	if err := bookingCommandLock(ctx, tx, tenantID, subjectID, commandKey); err != nil {
		return Booking{}, false, err
	}
	fingerprint := bookingFingerprint(fmt.Sprintf("%s\x00%d\x00%s", bookingID, expectedRevision, detail))
	var replay Booking
	if found, err := loadBookingReplay(ctx, tx, tenantID, country, subjectID, commandKey, fingerprint, &replay); err != nil {
		return Booking{}, false, err
	} else if found {
		persisted, loadErr := service.loadBooking(ctx, tx, replay.ID, false)
		if loadErr == nil {
			replay.scope = persisted.scope
			replay.walletDebit = persisted.walletDebit
		}
		return replay, true, nil
	}
	value, err := service.loadBooking(ctx, tx, bookingID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, false, ErrBookingNotFound
	}
	if err != nil {
		return Booking{}, false, err
	}
	if value.scope.TenantID != tenantID || value.scope.Country != country {
		return Booking{}, false, ErrBookingNotFound
	}
	if !authorize(value) {
		return Booking{}, false, ErrForbidden
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	oldTimelineLength := len(value.Timeline)
	if err := mutation(ctx, tx, &value); err != nil {
		return Booking{}, false, err
	}
	if err := service.updateBooking(ctx, tx, value); err != nil {
		return Booking{}, false, err
	}
	if err := insertTimeline(ctx, tx, value.ID, value.Timeline[oldTimelineLength:]); err != nil {
		return Booking{}, false, err
	}
	if value.CompletionEvidence != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO booking.completion_evidence (booking_id,photo_asset_id,submitted_by,captured_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (booking_id) DO UPDATE SET photo_asset_id=EXCLUDED.photo_asset_id,submitted_by=EXCLUDED.submitted_by,captured_at=EXCLUDED.captured_at`, value.ID, value.CompletionEvidence.PhotoAssetID, value.CompletionEvidence.SubmittedBy, value.CompletionEvidence.CapturedAt); err != nil {
			return Booking{}, false, fmt.Errorf("store completion evidence: %w", err)
		}
	}
	if err := storeBookingReplay(ctx, tx, tenantID, country, subjectID, commandKey, fingerprint, value, service.Now()); err != nil {
		return Booking{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Booking{}, false, mapBookingCommit(err)
	}
	return value, false, nil
}

func (service *PostgresService) transition(value *Booking, status Status, actor, reason string) {
	now := service.Now()
	value.Status = status
	value.Revision++
	value.UpdatedAt = now
	value.Timeline = append(value.Timeline, TimelineEvent{Status: status, Actor: actor, Reason: reason, CreatedAt: now})
	value.AllowedActions = customerActions(*value)
}

// Expire releases stale holds and cancels expired, uncaptured payment windows.
// It is safe to call concurrently from multiple workers.
func (service *PostgresService) Expire(ctx context.Context, limit int) (int, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return 0, ErrInvalidRequest
	}
	if _, err := service.pool.Exec(ctx, `UPDATE booking.slot_holds SET status='EXPIRED' WHERE status='HELD' AND expires_at <= $1`, service.Now()); err != nil {
		return 0, fmt.Errorf("expire stale booking holds: %w", err)
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text,tenant_id::text,country,customer_identity_id::text,revision FROM booking.bookings WHERE status='PENDING_PAYMENT' AND payment_expires_at <= $1 ORDER BY payment_expires_at FOR UPDATE SKIP LOCKED LIMIT $2`, service.Now(), limit)
	if err != nil {
		return 0, fmt.Errorf("list expired booking payments: %w", err)
	}
	type candidate struct {
		id, tenant, country, customer string
		revision                      int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.id, &value.tenant, &value.country, &value.customer, &value.revision); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired booking payment: %w", err)
		}
		candidates = append(candidates, value)
	}
	rows.Close()
	processed := 0
	for _, candidate := range candidates {
		scope := Scope{TenantID: candidate.tenant, Country: candidate.country, CustomerID: candidate.customer}
		key := "payment-expiry-" + strings.ReplaceAll(candidate.id, "-", "")
		_, replay, err := service.mutateCustomer(scope, key, candidate.id, candidate.revision, "payment-expiry", candidate.id, func(_ context.Context, _ pgx.Tx, value *Booking) error {
			if value.Status != StatusPendingPayment || value.paymentExpiresAt.After(service.Now()) {
				return ErrInvalidTransition
			}
			paymentValue, err := service.payments.CancelUncaptured(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, value.Payment.ID)
			if err != nil {
				return err
			}
			value.Payment = paymentValue
			service.transition(value, StatusCancelled, "SYSTEM", "Payment window expired")
			return nil
		})
		if err == nil && !replay {
			processed++
		}
	}
	return processed, nil
}

type rowScanner interface{ Scan(...any) error }

func scanOffering(row rowScanner) (Offering, error) {
	var value Offering
	var paymentMode string
	err := row.Scan(&value.ID, &value.ProviderID, &value.ProviderName, &value.CategoryID, &value.Name, &value.Summary,
		&value.DurationMinutes, &value.Price.AmountMinor, &value.Advance.AmountMinor, &value.Price.Currency, &paymentMode,
		&value.VerifiedProvider, &value.RatingAverage, &value.CompletedBookings, &value.LiveEngagements,
		&value.CancellationPolicyRef, &value.ReschedulePolicyRef, &value.Active, &value.ServicePostalCodes)
	if err != nil {
		return Offering{}, err
	}
	value.Advance.Currency = value.Price.Currency
	value.PaymentMode = PaymentMode(paymentMode)
	return value, nil
}

const slotQuery = `
	SELECT s.id::text,s.offering_id::text,s.provider_id::text,s.starts_at,s.ends_at,s.timezone,s.capacity,
	       GREATEST(s.capacity-
	         (SELECT count(*) FROM booking.slot_holds h WHERE h.slot_id=s.id AND h.status='HELD' AND h.expires_at>$6)-
	         (SELECT count(*) FROM booking.bookings b WHERE b.slot_id=s.id AND b.status NOT IN ('CANCELLED','DECLINED')),0)::integer,
	       s.buffer_minutes,o.price_minor,o.advance_minor,o.currency,s.policy_version,
	       to_char(s.starts_at AT TIME ZONE s.timezone,'YYYY-MM-DD'),s.provider_revision
	FROM booking.slots s JOIN booking.offerings o ON o.id=s.offering_id`

func scanSlot(row rowScanner) (Slot, error) {
	var value Slot
	err := row.Scan(&value.ID, &value.OfferingID, &value.ProviderID, &value.StartsAt, &value.EndsAt, &value.TimeZone, &value.Capacity, &value.Remaining, &value.BufferMinutes, &value.Price.AmountMinor, &value.Advance.AmountMinor, &value.Price.Currency, &value.PolicyVersion, &value.ServiceDate, &value.ProviderVersion)
	if err != nil {
		return Slot{}, err
	}
	value.Advance.Currency = value.Price.Currency
	if value.Remaining > 0 && value.StartsAt.After(time.Now().UTC()) {
		value.AllowedActions = []string{"HOLD"}
	}
	return value, nil
}

type bookingQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadPolicy(ctx context.Context, query bookingQuerier, tenantID, country string) (Policy, error) {
	var value Policy
	var holdTTL, cutoff, otpValidity, completion int64
	err := query.QueryRow(ctx, `SELECT version,country,hold_ttl_seconds,cancellation_cutoff_seconds,maximum_free_reschedules,start_otp_validity_seconds,completion_confirm_seconds,wallet_point_value_minor FROM booking.policies WHERE tenant_id=$1 AND country=$2`, tenantID, country).Scan(&value.Version, &value.Country, &holdTTL, &cutoff, &value.MaximumFreeReschedules, &otpValidity, &completion, &value.WalletPointValueMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrPolicyDenied
	}
	if err != nil {
		return Policy{}, fmt.Errorf("load booking policy: %w", err)
	}
	value.HoldTTL = time.Duration(holdTTL) * time.Second
	value.CancellationCutoff = time.Duration(cutoff) * time.Second
	value.StartOTPValidity = time.Duration(otpValidity) * time.Second
	value.CompletionConfirmWindow = time.Duration(completion) * time.Second
	return value, nil
}

func loadOfferingBySlot(ctx context.Context, query bookingQuerier, scope Scope, slotID string) (Offering, error) {
	row := query.QueryRow(ctx, `
		SELECT o.id::text,o.provider_id::text,o.provider_name,o.category_id::text,o.name,o.summary,
		       o.duration_minutes,o.price_minor,o.advance_minor,o.currency,o.payment_mode,
		       o.verified_provider,o.rating_average::float8,o.completed_bookings,o.live_engagements,
		       o.cancellation_policy_ref,o.reschedule_policy_ref,o.active,
		       COALESCE(array_agg(z.postal_code ORDER BY z.postal_code) FILTER (WHERE z.postal_code IS NOT NULL),'{}')
		FROM booking.slots s JOIN booking.offerings o ON o.id=s.offering_id LEFT JOIN booking.service_zones z ON z.offering_id=o.id
		WHERE s.id=$1 AND o.tenant_id=$2 AND o.country=$3 AND o.active=true GROUP BY o.id`, slotID, scope.TenantID, scope.Country)
	value, err := scanOffering(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Offering{}, ErrOfferingNotFound
	}
	if err != nil {
		return Offering{}, fmt.Errorf("load booking offering: %w", err)
	}
	return value, nil
}

func loadSlot(ctx context.Context, query bookingQuerier, slotID string, scope Scope, requireFuture bool, now time.Time) (Slot, error) {
	row := query.QueryRow(ctx, `
		SELECT s.id::text,s.offering_id::text,s.provider_id::text,s.starts_at,s.ends_at,s.timezone,s.capacity,
		       GREATEST(s.capacity-
		         (SELECT count(*) FROM booking.slot_holds h WHERE h.slot_id=s.id AND h.status='HELD' AND h.expires_at>$4)-
		         (SELECT count(*) FROM booking.bookings b WHERE b.slot_id=s.id AND b.status NOT IN ('CANCELLED','DECLINED')),0)::integer,
		       s.buffer_minutes,o.price_minor,o.advance_minor,o.currency,s.policy_version,
		       to_char(s.starts_at AT TIME ZONE s.timezone,'YYYY-MM-DD'),s.provider_revision
		FROM booking.slots s JOIN booking.offerings o ON o.id=s.offering_id
		WHERE s.id=$1 AND o.tenant_id=$2 AND o.country=$3 AND (NOT $5 OR s.starts_at>$4)`, slotID, scope.TenantID, scope.Country, now, requireFuture)
	value, err := scanSlot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Slot{}, ErrSlotNotFound
	}
	if err != nil {
		return Slot{}, fmt.Errorf("load booking slot: %w", err)
	}
	value.AllowedActions = nil
	if value.Remaining > 0 && value.StartsAt.After(now) {
		value.AllowedActions = []string{"HOLD"}
	}
	return value, nil
}

func insertBooking(ctx context.Context, tx pgx.Tx, value Booking) error {
	offeringSnapshot, err := json.Marshal(value.Offering)
	if err != nil {
		return fmt.Errorf("encode booking offering snapshot: %w", err)
	}
	slotSnapshot, err := json.Marshal(value.Slot)
	if err != nil {
		return fmt.Errorf("encode booking slot snapshot: %w", err)
	}
	paymentSnapshot, err := json.Marshal(value.Payment)
	if err != nil {
		return fmt.Errorf("encode booking payment snapshot: %w", err)
	}
	var walletDebitID any
	if value.walletDebit != nil {
		walletDebitID = value.walletDebit.ID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO booking.bookings
		(id,tenant_id,country,customer_identity_id,provider_id,offering_id,slot_id,revision,status,
		 offering_snapshot,slot_snapshot,price_minor,amount_due_minor,currency,payment_id,wallet_debit_entry_id,
		 reschedule_count,start_otp_digest,start_otp_expires_at,allowed_actions,created_at,updated_at,
		 payment_snapshot,payment_expires_at,start_otp_ciphertext,free_reschedules_left)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,NULL,NULL,$18,$19,$20,$21,$22,NULL,$23)`,
		value.ID, value.scope.TenantID, value.scope.Country, value.scope.CustomerID, value.Offering.ProviderID, value.Offering.ID, value.Slot.ID,
		value.Revision, value.Status, offeringSnapshot, slotSnapshot, value.Price.AmountMinor, value.AmountDue.AmountMinor, value.Price.Currency,
		value.Payment.ID, walletDebitID, value.RescheduleCount, value.AllowedActions, value.CreatedAt, value.UpdatedAt, paymentSnapshot, value.paymentExpiresAt, value.FreeReschedulesLeft)
	if err != nil {
		return fmt.Errorf("insert booking: %w", err)
	}
	return nil
}

func (service *PostgresService) updateBooking(ctx context.Context, tx pgx.Tx, value Booking) error {
	slotSnapshot, err := json.Marshal(value.Slot)
	if err != nil {
		return fmt.Errorf("encode booking slot snapshot: %w", err)
	}
	paymentSnapshot, err := json.Marshal(value.Payment)
	if err != nil {
		return fmt.Errorf("encode booking payment snapshot: %w", err)
	}
	var digest, ciphertext any
	var otpExpiry any
	if value.StartOTP != "" {
		digest = value.startOTPDigest[:]
		otpExpiry = value.otpExpiresAt
		ciphertextBytes, err := service.sealOTP(value.ID, value.StartOTP)
		if err != nil {
			return err
		}
		ciphertext = ciphertextBytes
	}
	command, err := tx.Exec(ctx, `
		UPDATE booking.bookings SET slot_id=$2,slot_snapshot=$3,revision=$4,status=$5,payment_snapshot=$6,
		reschedule_count=$7,free_reschedules_left=$8,start_otp_digest=$9,start_otp_expires_at=$10,
		start_otp_ciphertext=$11,allowed_actions=$12,updated_at=$13
		WHERE id=$1`, value.ID, value.Slot.ID, slotSnapshot, value.Revision, value.Status, paymentSnapshot,
		value.RescheduleCount, value.FreeReschedulesLeft, digest, otpExpiry, ciphertext, value.AllowedActions, value.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update booking lifecycle: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrBookingNotFound
	}
	return nil
}

func insertTimeline(ctx context.Context, tx pgx.Tx, bookingID string, values []TimelineEvent) error {
	for _, value := range values {
		if _, err := tx.Exec(ctx, `INSERT INTO booking.timeline_events (booking_id,status,actor_type,reason,created_at) VALUES ($1,$2,$3,NULLIF($4,''),$5)`, bookingID, value.Status, value.Actor, value.Reason, value.CreatedAt); err != nil {
			return fmt.Errorf("insert booking timeline: %w", err)
		}
	}
	return nil
}

func (service *PostgresService) loadBooking(ctx context.Context, query bookingQuerier, bookingID string, forUpdate bool) (Booking, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var value Booking
	var offeringSnapshot, slotSnapshot, paymentSnapshot []byte
	var walletDebitID string
	var digest, ciphertext []byte
	var otpExpiry *time.Time
	err := query.QueryRow(ctx, `
		SELECT id::text,tenant_id::text,country,customer_identity_id::text,revision,status,
		       offering_snapshot,slot_snapshot,price_minor,amount_due_minor,currency,payment_snapshot,
		       COALESCE(wallet_debit_entry_id::text,''),reschedule_count,free_reschedules_left,
		       COALESCE(start_otp_digest,decode('','hex')),start_otp_expires_at,COALESCE(start_otp_ciphertext,decode('','hex')),
		       allowed_actions,created_at,updated_at,payment_expires_at
		FROM booking.bookings WHERE id=$1`+suffix, bookingID).Scan(&value.ID, &value.scope.TenantID, &value.scope.Country, &value.scope.CustomerID, &value.Revision, &value.Status,
		&offeringSnapshot, &slotSnapshot, &value.Price.AmountMinor, &value.AmountDue.AmountMinor, &value.Price.Currency, &paymentSnapshot,
		&walletDebitID, &value.RescheduleCount, &value.FreeReschedulesLeft, &digest, &otpExpiry, &ciphertext,
		&value.AllowedActions, &value.CreatedAt, &value.UpdatedAt, &value.paymentExpiresAt)
	if err != nil {
		return Booking{}, err
	}
	value.AmountDue.Currency = value.Price.Currency
	if err := json.Unmarshal(offeringSnapshot, &value.Offering); err != nil {
		return Booking{}, fmt.Errorf("decode booking offering snapshot: %w", err)
	}
	if err := json.Unmarshal(slotSnapshot, &value.Slot); err != nil {
		return Booking{}, fmt.Errorf("decode booking slot snapshot: %w", err)
	}
	if err := json.Unmarshal(paymentSnapshot, &value.Payment); err != nil {
		return Booking{}, fmt.Errorf("decode booking payment snapshot: %w", err)
	}
	if walletDebitID != "" {
		value.walletDebit = &wallet.LedgerEntry{ID: walletDebitID}
	}
	if len(digest) == sha256.Size {
		copy(value.startOTPDigest[:], digest)
	}
	if otpExpiry != nil {
		value.otpExpiresAt = otpExpiry.UTC()
	}
	if len(ciphertext) > 0 {
		value.StartOTP, err = service.openOTP(value.ID, ciphertext)
		if err != nil {
			return Booking{}, err
		}
	}
	rows, err := query.Query(ctx, `SELECT status,actor_type,COALESCE(reason,''),created_at FROM booking.timeline_events WHERE booking_id=$1 ORDER BY id`, bookingID)
	if err != nil {
		return Booking{}, fmt.Errorf("load booking timeline: %w", err)
	}
	for rows.Next() {
		var event TimelineEvent
		if err := rows.Scan(&event.Status, &event.Actor, &event.Reason, &event.CreatedAt); err != nil {
			rows.Close()
			return Booking{}, fmt.Errorf("scan booking timeline: %w", err)
		}
		value.Timeline = append(value.Timeline, event)
	}
	rows.Close()
	var evidence CompletionEvidence
	err = query.QueryRow(ctx, `SELECT photo_asset_id::text,captured_at,submitted_by::text FROM booking.completion_evidence WHERE booking_id=$1`, bookingID).Scan(&evidence.PhotoAssetID, &evidence.CapturedAt, &evidence.SubmittedBy)
	if err == nil {
		value.CompletionEvidence = &evidence
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, fmt.Errorf("load booking completion evidence: %w", err)
	}
	return value, nil
}

func bookingCommandLock(ctx context.Context, tx pgx.Tx, tenantID, subjectID, key string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":"+subjectID+":"+key); err != nil {
		return fmt.Errorf("lock booking command: %w", err)
	}
	return nil
}

func loadBookingReplay(ctx context.Context, tx pgx.Tx, tenantID, country, subjectID, key, fingerprint string, destination any) (bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM booking.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND idempotency_key=$4`, tenantID, country, subjectID, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load booking command replay: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(storedFingerprint), []byte(fingerprint)) != 1 {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return false, fmt.Errorf("decode booking command replay: %w", err)
	}
	return true, nil
}

func storeBookingReplay(ctx context.Context, tx pgx.Tx, tenantID, country, subjectID, key, fingerprint string, value any, now time.Time) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode booking command replay: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO booking.idempotency_records (tenant_id,country,subject_id,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenantID, country, subjectID, key, fingerprint, payload, now)
	if err != nil {
		return fmt.Errorf("store booking command replay: %w", err)
	}
	return nil
}

func (service *PostgresService) sealOTP(bookingID, otp string) ([]byte, error) {
	nonce := make([]byte, service.otpCipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate booking otp nonce: %w", err)
	}
	return service.otpCipher.Seal(nonce, nonce, []byte(otp), []byte(bookingID)), nil
}

func (service *PostgresService) openOTP(bookingID string, ciphertext []byte) (string, error) {
	nonceSize := service.otpCipher.NonceSize()
	if len(ciphertext) <= nonceSize {
		return "", errors.New("booking otp ciphertext is invalid")
	}
	plaintext, err := service.otpCipher.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], []byte(bookingID))
	if err != nil || len(plaintext) != 6 {
		return "", errors.New("booking otp ciphertext cannot be authenticated")
	}
	return string(plaintext), nil
}

func bookingFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func bookingUUID(kind, source string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("planext4u:"+kind+":"+source)).String()
}

func postgresBookingScope(scope Scope) bool {
	return postgresUUID(scope.TenantID) && len(scope.Country) == 2 && scope.Country == strings.ToUpper(scope.Country) && postgresUUID(scope.CustomerID)
}

func postgresBookingActor(actor Actor) bool {
	return postgresUUID(actor.TenantID) && len(actor.Country) == 2 && actor.Country == strings.ToUpper(actor.Country) && postgresUUID(actor.Subject) && len(actor.Roles) > 0
}

func postgresUUID(value string) bool { return uuid.Validate(strings.TrimSpace(value)) == nil }

func mapBookingCommit(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01") {
		return ErrRevisionConflict
	}
	return fmt.Errorf("commit booking transaction: %w", err)
}
