package fulfillment

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fulfillmentOperationTimeout = 25 * time.Second

type fulfillmentPolicy struct {
	version               string
	offerTTL              time.Duration
	locationTTL           time.Duration
	chatAfterDeliveryTTL  time.Duration
	settlementCooling     time.Duration
	commissionBasisPoints int64
	taxBasisPoints        int64
}

// PostgresService is the durable fulfillment runtime. Mutations, revision
// checks, audit records and idempotency responses share one database commit.
type PostgresService struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	err := service.pool.QueryRow(ctx, `SELECT to_regclass('fulfillment.rider_profiles') IS NOT NULL AND to_regclass('fulfillment.delivery_tasks') IS NOT NULL AND to_regclass('fulfillment.policies') IS NOT NULL AND to_regclass('fulfillment.payout_entry_claims') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check fulfillment schema readiness: %w", err)
	}
	if !ready {
		return errors.New("fulfillment schema is unavailable")
	}
	return nil
}

func (service *PostgresService) RegisterRider(actor Actor, key string, request RiderRegistrationRequest) (RiderProfile, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || !validRiderRegistration(request) || !allFulfillmentUUIDDocuments(request.Documents) {
		return RiderProfile{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RiderProfile{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fulfillmentCommandLock(ctx, tx, actor, "rider-register", key); err != nil {
		return RiderProfile{}, false, err
	}
	fingerprint := digest(request)
	var replay RiderProfile
	if found, err := loadFulfillmentReplay(ctx, tx, actor, "rider-register", key, fingerprint, &replay); err != nil {
		return RiderProfile{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	documents := cloneRiderDocuments(request.Documents)
	for index := range documents {
		documents[index].OCRStatus = "REVIEW_REQUIRED"
	}
	encoded, _ := json.Marshal(documents)
	now := service.Now()
	value := RiderProfile{ID: fulfillmentUUID("rider", actor.TenantID+":"+actor.Country+":"+actor.Subject), RiderID: actor.Subject, Revision: 1, Status: RiderKYCReview, FullName: strings.TrimSpace(request.FullName), PhoneMasked: request.PhoneMasked, VehicleType: request.VehicleType, VehicleNumber: strings.ToUpper(request.VehicleNumber), Documents: documents, BankReference: request.BankReference, BankStatus: "PENDING_VERIFICATION", Zones: append([]string(nil), request.Zones...), MaxConcurrent: 2, AllowedActions: []string{"VIEW_REVIEW_STATUS"}, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.rider_profiles (id,tenant_id,country,rider_identity_id,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`, value.ID, actor.TenantID, actor.Country, actor.Subject, value.Status, value.FullName, value.PhoneMasked, value.VehicleType, value.VehicleNumber, value.BankReference, value.BankStatus, value.Zones, value.MaxConcurrent, encoded, now)
	if err != nil {
		return RiderProfile{}, false, mapFulfillmentError(err)
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, "rider-register", key, fingerprint, value, now); err != nil {
		return RiderProfile{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RiderProfile{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Rider(actor Actor) (RiderProfile, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") {
		return RiderProfile{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	value, err := loadPostgresRider(ctx, service.pool, actor.TenantID, actor.Country, actor.Subject, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderProfile{}, ErrNotFound
	}
	return value, err
}

func (service *PostgresService) ReviewRider(actor Actor, key, riderID string, revision int64, approved bool, reason string) (RiderProfile, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validKey(key) || !fulfillmentIsUUID(riderID) || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return RiderProfile{}, false, ErrMFARequired
		}
		return RiderProfile{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RiderProfile{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "rider-review:" + riderID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return RiderProfile{}, false, err
	}
	fingerprint := digest(struct {
		Revision int64
		Approved bool
		Reason   string
	}{revision, approved, reason})
	var replay RiderProfile
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return RiderProfile{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresRider(ctx, tx, actor.TenantID, actor.Country, riderID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderProfile{}, false, ErrNotFound
	}
	if err != nil {
		return RiderProfile{}, false, err
	}
	if value.Revision != revision || value.Status != RiderKYCReview {
		return RiderProfile{}, false, ErrConflict
	}
	for index := range value.Documents {
		if approved {
			value.Documents[index].OCRStatus = "VERIFIED"
		} else {
			value.Documents[index].OCRStatus = "REJECTED"
		}
	}
	if approved {
		value.Status, value.BankStatus, value.AllowedActions = RiderApproved, "VERIFIED", []string{"START_DUTY", "VIEW_EARNINGS"}
	} else {
		value.Status, value.AllowedActions = RiderSuspended, nil
	}
	value.Revision++
	value.UpdatedAt = service.Now()
	documents, _ := json.Marshal(value.Documents)
	_, err = tx.Exec(ctx, `UPDATE fulfillment.rider_profiles SET revision=$2,status=$3,bank_status=$4,documents=$5,updated_at=$6 WHERE id=$1`, value.ID, value.Revision, value.Status, value.BankStatus, documents, value.UpdatedAt)
	if err != nil {
		return RiderProfile{}, false, err
	}
	if err := insertFulfillmentAudit(ctx, tx, actor, "RIDER_REVIEW", "RIDER_PROFILE", riderID, reason, value.UpdatedAt); err != nil {
		return RiderProfile{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, value.UpdatedAt); err != nil {
		return RiderProfile{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RiderProfile{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) StartDuty(actor Actor, key, zoneID string) (DutySession, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || !safeID(zoneID) {
		return DutySession{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DutySession{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fulfillmentCommandLock(ctx, tx, actor, "duty-start", key); err != nil {
		return DutySession{}, false, err
	}
	fingerprint := digest(zoneID)
	var replay DutySession
	if found, err := loadFulfillmentReplay(ctx, tx, actor, "duty-start", key, fingerprint, &replay); err != nil {
		return DutySession{}, false, err
	} else if found {
		return replay, true, nil
	}
	profile, err := loadPostgresRider(ctx, tx, actor.TenantID, actor.Country, actor.Subject, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (profile.Status != RiderApproved || !contains(profile.Zones, zoneID)) {
		return DutySession{}, false, ErrForbidden
	}
	if err != nil {
		return DutySession{}, false, err
	}
	now := service.Now()
	value := DutySession{ID: fulfillmentUUID("duty", actor.Subject+":"+key), RiderID: actor.Subject, Revision: 1, Status: "ACTIVE", StartedAt: now, LastSeenAt: now, ZoneID: zoneID, tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.duty_sessions (id,tenant_id,country,rider_identity_id,revision,status,zone_id,started_at,last_seen_at) VALUES ($1,$2,$3,$4,1,'ACTIVE',$5,$6,$6)`, value.ID, actor.TenantID, actor.Country, actor.Subject, zoneID, now)
	if err != nil {
		return DutySession{}, false, mapFulfillmentError(err)
	}
	if err := insertFulfillmentAttendance(ctx, tx, actor, value.ID, "DUTY_START", Point{}, "RIDER_APP", now); err != nil {
		return DutySession{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, "duty-start", key, fingerprint, value, now); err != nil {
		return DutySession{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DutySession{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) EndDuty(actor Actor, key string, revision int64) (DutySession, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) {
		return DutySession{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DutySession{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fulfillmentCommandLock(ctx, tx, actor, "duty-end", key); err != nil {
		return DutySession{}, false, err
	}
	fingerprint := digest(revision)
	var replay DutySession
	if found, err := loadFulfillmentReplay(ctx, tx, actor, "duty-end", key, fingerprint, &replay); err != nil {
		return DutySession{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresDuty(ctx, tx, actor, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return DutySession{}, false, ErrNotFound
	}
	if err != nil {
		return DutySession{}, false, err
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND assigned_rider_identity_id=$3 AND status IN ('ASSIGNED','PICKED_UP')`, actor.TenantID, actor.Country, actor.Subject).Scan(&active); err != nil {
		return DutySession{}, false, err
	}
	if value.Revision != revision || active != 0 {
		return DutySession{}, false, ErrConflict
	}
	now := service.Now()
	value.Status, value.Revision, value.EndedAt, value.LastSeenAt = "ENDED", value.Revision+1, &now, now
	_, err = tx.Exec(ctx, `UPDATE fulfillment.duty_sessions SET status='ENDED',revision=$2,ended_at=$3,last_seen_at=$3 WHERE id=$1`, value.ID, value.Revision, now)
	if err != nil {
		return DutySession{}, false, err
	}
	if err := insertFulfillmentAttendance(ctx, tx, actor, value.ID, "DUTY_END", Point{}, "RIDER_APP", now); err != nil {
		return DutySession{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, "duty-end", key, fingerprint, value, now); err != nil {
		return DutySession{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DutySession{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Duty(actor Actor) (DutySession, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") {
		return DutySession{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	value, err := loadPostgresDuty(ctx, service.pool, actor, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return DutySession{}, ErrNotFound
	}
	if err != nil {
		return DutySession{}, err
	}
	if err := service.pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND assigned_rider_identity_id=$3 AND status IN ('ASSIGNED','PICKED_UP')`, actor.TenantID, actor.Country, actor.Subject).Scan(&value.ActiveTasks); err != nil {
		return DutySession{}, err
	}
	return value, nil
}

func (service *PostgresService) SeedTask(actor Actor, seed TaskSeed) (DeliveryTask, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validPostgresTaskSeed(seed) {
		if !actor.MFAVerified {
			return DeliveryTask{}, ErrMFARequired
		}
		return DeliveryTask{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DeliveryTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	pickup, _ := json.Marshal(seed.Pickup)
	dropoff, _ := json.Marshal(seed.Dropoff)
	now := service.Now()
	value := DeliveryTask{ID: seed.ID, Revision: 1, OrderID: seed.OrderID, OrderType: seed.OrderType, RegionID: seed.RegionID, TerritoryID: seed.TerritoryID, ZoneID: seed.ZoneID, Status: "READY_FOR_DISPATCH", Pickup: seed.Pickup, Dropoff: seed.Dropoff, DistanceMeters: seed.DistanceMeters, Earning: seed.Earning, AllowedActions: []string{"OFFER"}, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country, deliveryOTPHash: digest(seed.DeliveryOTP)}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.delivery_tasks (id,tenant_id,country,order_id,order_type,region_id,territory_id,zone_id,revision,status,pickup,dropoff,distance_meters,earning_minor,currency,delivery_otp_digest,reassignment_count,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,'READY_FOR_DISPATCH',$9,$10,$11,$12,$13,decode($14,'hex'),0,$15)`, value.ID, actor.TenantID, actor.Country, seed.OrderID, seed.OrderType, seed.RegionID, seed.TerritoryID, seed.ZoneID, pickup, dropoff, seed.DistanceMeters, seed.Earning.AmountMinor, seed.Earning.Currency, value.deliveryOTPHash, now)
	if err != nil {
		return DeliveryTask{}, mapFulfillmentError(err)
	}
	conversationID := fulfillmentUUID("conversation", actor.TenantID+":"+seed.OrderID)
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.conversations (id,tenant_id,country,order_id,participant_identity_ids,expires_at,blocked) VALUES ($1,$2,$3,$4,$5,$6,false)`, conversationID, actor.TenantID, actor.Country, seed.OrderID, []string{seed.CustomerID, seed.CounterpartyID}, now.Add(24*time.Hour))
	if err != nil {
		return DeliveryTask{}, mapFulfillmentError(err)
	}
	if err := insertFulfillmentAudit(ctx, tx, actor, "TASK_CREATED", "DELIVERY_TASK", value.ID, "Dispatch task created", now); err != nil {
		return DeliveryTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeliveryTask{}, mapFulfillmentError(err)
	}
	return value, nil
}

func (service *PostgresService) OfferTask(actor Actor, key, taskID string, revision int64) (DeliveryTask, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !validKey(key) || !fulfillmentIsUUID(taskID) {
		if !actor.MFAVerified {
			return DeliveryTask{}, false, ErrMFARequired
		}
		return DeliveryTask{}, false, ErrForbidden
	}
	return service.mutateTask(actor, key, taskID, revision, "offer", func(ctx context.Context, tx pgx.Tx, value *DeliveryTask, policy fulfillmentPolicy, now time.Time) error {
		if value.Status != "READY_FOR_DISPATCH" && value.Status != "REASSIGNMENT_REQUIRED" {
			return ErrInvalidTransition
		}
		expires := now.Add(policy.offerTTL)
		value.Status, value.AssignedRiderID, value.OfferExpiresAt = "OFFERED", "", &expires
		return insertFulfillmentAudit(ctx, tx, actor, "TASK_OFFERED", "DELIVERY_TASK", value.ID, "Task published to eligible riders", now)
	})
}

func (service *PostgresService) Offers(actor Actor) ([]DeliveryTask, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	profile, err := loadPostgresRider(ctx, service.pool, actor.TenantID, actor.Country, actor.Subject, false)
	if err != nil || profile.Status != RiderApproved {
		return nil, ErrForbidden
	}
	duty, err := loadPostgresDuty(ctx, service.pool, actor, false)
	if err != nil || duty.Status != "ACTIVE" {
		return nil, ErrForbidden
	}
	rows, err := service.pool.Query(ctx, taskSelect+` WHERE tenant_id=$1 AND country=$2 AND zone_id=$3 AND status='OFFERED' AND offer_expires_at>$4 ORDER BY offer_expires_at`, actor.TenantID, actor.Country, duty.ZoneID, service.Now())
	if err != nil {
		return nil, err
	}
	return scanPostgresTasks(rows)
}

func (service *PostgresService) AcceptOffer(actor Actor, key, taskID string, revision int64) (DeliveryTask, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || !fulfillmentIsUUID(taskID) {
		return DeliveryTask{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DeliveryTask{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "accept:" + taskID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return DeliveryTask{}, false, err
	}
	fingerprint := digest(revision)
	var replay DeliveryTask
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return DeliveryTask{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresTask(ctx, tx, actor.TenantID, actor.Country, taskID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTask{}, false, ErrNotFound
	}
	if err != nil {
		return DeliveryTask{}, false, mapFulfillmentError(err)
	}
	profile, err := loadPostgresRider(ctx, tx, actor.TenantID, actor.Country, actor.Subject, true)
	if err != nil || profile.Status != RiderApproved {
		return DeliveryTask{}, false, ErrForbidden
	}
	duty, err := loadPostgresDuty(ctx, tx, actor, true)
	if err != nil || duty.Status != "ACTIVE" || duty.ZoneID != value.ZoneID {
		return DeliveryTask{}, false, ErrForbidden
	}
	if value.OfferExpiresAt == nil || !value.OfferExpiresAt.After(service.Now()) {
		return DeliveryTask{}, false, ErrOfferExpired
	}
	if value.Revision != revision || value.Status != "OFFERED" || value.AssignedRiderID != "" {
		return DeliveryTask{}, false, ErrConflict
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND assigned_rider_identity_id=$3 AND status IN ('ASSIGNED','PICKED_UP')`, actor.TenantID, actor.Country, actor.Subject).Scan(&active); err != nil {
		return DeliveryTask{}, false, err
	}
	if active >= profile.MaxConcurrent {
		return DeliveryTask{}, false, ErrConflict
	}
	now := service.Now()
	value.Status, value.Revision, value.AssignedRiderID, value.AcceptedAt, value.UpdatedAt = "ASSIGNED", value.Revision+1, actor.Subject, &now, now
	_, err = tx.Exec(ctx, `UPDATE fulfillment.delivery_tasks SET status='ASSIGNED',revision=$2,assigned_rider_identity_id=$3,accepted_at=$4,updated_at=$4 WHERE id=$1`, value.ID, value.Revision, actor.Subject, now)
	if err != nil {
		return DeliveryTask{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE fulfillment.duty_sessions SET last_seen_at=$2 WHERE id=$1`, duty.ID, now)
	if err != nil {
		return DeliveryTask{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE fulfillment.conversations SET participant_identity_ids=array_append(participant_identity_ids,$2::uuid) WHERE order_id=$1 AND NOT ($2::uuid=ANY(participant_identity_ids))`, value.OrderID, actor.Subject)
	if err != nil {
		return DeliveryTask{}, false, err
	}
	value.AllowedActions = taskAllowedActions(value.Status)
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return DeliveryTask{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeliveryTask{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) UpdateLocation(actor Actor, key string, input LocationUpdate) (RiderLocation, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || input.Sequence < 1 || !validPoint(input.Point) || input.AccuracyM <= 0 || input.AccuracyM > 500 || input.CapturedAt.IsZero() {
		return RiderLocation{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RiderLocation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, replay, err := service.updatePostgresLocation(ctx, tx, actor, key, input)
	if err != nil {
		return RiderLocation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RiderLocation{}, false, mapFulfillmentError(err)
	}
	return value, replay, nil
}

func (service *PostgresService) Location(actor Actor, riderID string) (RiderLocation, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN", "CUSTOMER", "RESTAURANT_VENDOR") || !fulfillmentIsUUID(riderID) {
		return RiderLocation{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	value, err := loadPostgresLocation(ctx, service.pool, actor.TenantID, actor.Country, riderID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderLocation{}, ErrNotFound
	}
	if err != nil {
		return RiderLocation{}, err
	}
	if !value.ExpiresAt.After(service.Now()) {
		return RiderLocation{}, ErrLocationStale
	}
	return value, nil
}

func (service *PostgresService) MarkPickedUp(actor Actor, key, taskID string, revision int64) (DeliveryTask, bool, error) {
	return service.mutateRiderTask(actor, key, taskID, revision, "PICKED_UP", CompletionRequest{})
}

func (service *PostgresService) CompleteDelivery(actor Actor, key, taskID string, revision int64, request CompletionRequest) (DeliveryTask, bool, error) {
	if !fulfillmentIsUUID(request.BlurredPhotoAssetID) || request.OTP == "" && !fulfillmentIsUUID(request.SignatureAssetID) {
		return DeliveryTask{}, false, ErrInvalidRequest
	}
	return service.mutateRiderTask(actor, key, taskID, revision, "DELIVERED", request)
}

func (service *PostgresService) Tasks(actor Actor) ([]DeliveryTask, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "RIDER", "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN", "FRANCHISE_ADMIN") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	query := taskSelect + ` WHERE tenant_id=$1 AND country=$2`
	args := []any{actor.TenantID, actor.Country}
	if hasRole(actor, "RIDER") {
		query += ` AND assigned_rider_identity_id=$3`
		args = append(args, actor.Subject)
	} else if hasRole(actor, "FRANCHISE_ADMIN") {
		query += ` AND territory_id IN (SELECT id::text FROM fulfillment.territories WHERE tenant_id=$1 AND country=$2 AND franchise_identity_id=$3)`
		args = append(args, actor.Subject)
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := service.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanPostgresTasks(rows)
}

func (service *PostgresService) Reassign(actor Actor, key, taskID string, revision int64, reason string) (DeliveryTask, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return DeliveryTask{}, false, ErrMFARequired
		}
		return DeliveryTask{}, false, ErrForbidden
	}
	return service.mutateTask(actor, key, taskID, revision, "reassign", func(ctx context.Context, tx pgx.Tx, value *DeliveryTask, policy fulfillmentPolicy, now time.Time) error {
		if value.Status != "ASSIGNED" && value.Status != "REASSIGNMENT_REQUIRED" {
			return ErrInvalidTransition
		}
		expires := now.Add(policy.offerTTL)
		value.Status, value.AssignedRiderID, value.OfferExpiresAt = "OFFERED", "", &expires
		value.ReassignmentCount++
		return insertFulfillmentAudit(ctx, tx, actor, "TASK_REASSIGNED", "DELIVERY_TASK", value.ID, reason, now)
	})
}

func (service *PostgresService) RecoverOffline(actor Actor, commands []OfflineCommand) ([]OfflineResult, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || len(commands) == 0 || len(commands) > 100 {
		return nil, ErrInvalidRequest
	}
	sorted := append([]OfflineCommand(nil), commands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].DeviceSequence < sorted[j].DeviceSequence })
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Subject+":offline"); err != nil {
		return nil, err
	}
	var lastSequence int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(device_sequence),0) FROM fulfillment.offline_commands WHERE rider_identity_id=$1`, actor.Subject).Scan(&lastSequence); err != nil {
		return nil, err
	}
	expected := lastSequence + 1
	results := make([]OfflineResult, 0, len(sorted))
	for _, command := range sorted {
		var encoded []byte
		err := tx.QueryRow(ctx, `SELECT result FROM fulfillment.offline_commands WHERE rider_identity_id=$1 AND command_id=$2`, actor.Subject, command.CommandID).Scan(&encoded)
		if err == nil {
			var previous OfflineResult
			if json.Unmarshal(encoded, &previous) != nil {
				return nil, ErrConflict
			}
			results = append(results, previous)
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if !validKey(command.CommandID) || command.DeviceSequence != expected {
			return nil, ErrConflict
		}
		result := OfflineResult{DeviceSequence: command.DeviceSequence, CommandID: command.CommandID, Status: "APPLIED"}
		switch command.Kind {
		case "LOCATION":
			location, err := locationFromPayload(command.Payload)
			if err != nil {
				return nil, err
			}
			var sequence int64
			_ = tx.QueryRow(ctx, `SELECT sequence FROM fulfillment.rider_locations WHERE rider_identity_id=$1 FOR UPDATE`, actor.Subject).Scan(&sequence)
			location.Sequence = sequence + 1
			if _, _, err := service.updatePostgresLocation(ctx, tx, actor, command.CommandID, location); err != nil {
				return nil, err
			}
			result.ResourceStatus = "LOCATION_UPDATED"
		case "PICKUP":
			value, err := loadPostgresTask(ctx, tx, actor.TenantID, actor.Country, command.TaskID, true)
			if err != nil || value.AssignedRiderID != actor.Subject || value.Revision != command.Revision || value.Status != "ASSIGNED" {
				return nil, ErrConflict
			}
			now := service.Now()
			_, err = tx.Exec(ctx, `UPDATE fulfillment.delivery_tasks SET status='PICKED_UP',revision=revision+1,picked_up_at=$2,updated_at=$2 WHERE id=$1`, command.TaskID, now)
			if err != nil {
				return nil, err
			}
			result.ResourceStatus = "PICKED_UP"
		default:
			return nil, ErrInvalidRequest
		}
		resultJSON, _ := json.Marshal(result)
		_, err = tx.Exec(ctx, `INSERT INTO fulfillment.offline_commands (rider_identity_id,device_sequence,command_id,kind,status,result,processed_at) VALUES ($1,$2,$3,$4,'APPLIED',$5,$6)`, actor.Subject, command.DeviceSequence, command.CommandID, command.Kind, resultJSON, service.Now())
		if err != nil {
			return nil, mapFulfillmentError(err)
		}
		expected++
		results = append(results, result)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapFulfillmentError(err)
	}
	return results, nil
}

type fulfillmentRow interface{ Scan(...any) error }
type fulfillmentQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const taskSelect = `SELECT id::text,tenant_id::text,country,order_id::text,order_type,region_id,territory_id,zone_id,revision,status,coalesce(assigned_rider_identity_id::text,''),pickup,dropoff,distance_meters,earning_minor,currency,offer_expires_at,accepted_at,picked_up_at,delivered_at,encode(delivery_otp_digest,'hex'),coalesce(pod_blurred_asset_id::text,''),coalesce(pod_signature_asset_id::text,''),reassignment_count,updated_at FROM fulfillment.delivery_tasks`

func loadPostgresRider(ctx context.Context, query fulfillmentQuerier, tenantID, country, riderID string, lock bool) (RiderProfile, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var value RiderProfile
	var documents []byte
	err := query.QueryRow(ctx, `SELECT id::text,rider_identity_id::text,revision,status,full_name,phone_masked,vehicle_type,vehicle_number,bank_reference,bank_status,zones,max_concurrent,documents,created_at,updated_at,tenant_id::text,country FROM fulfillment.rider_profiles WHERE tenant_id=$1 AND country=$2 AND rider_identity_id=$3`+suffix, tenantID, country, riderID).Scan(&value.ID, &value.RiderID, &value.Revision, &value.Status, &value.FullName, &value.PhoneMasked, &value.VehicleType, &value.VehicleNumber, &value.BankReference, &value.BankStatus, &value.Zones, &value.MaxConcurrent, &documents, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country)
	if err != nil {
		return RiderProfile{}, err
	}
	if err := json.Unmarshal(documents, &value.Documents); err != nil {
		return RiderProfile{}, fmt.Errorf("decode rider documents: %w", err)
	}
	value.AllowedActions = riderAllowedActions(value.Status)
	return value, nil
}

func loadPostgresDuty(ctx context.Context, query fulfillmentQuerier, actor Actor, lock bool) (DutySession, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var value DutySession
	err := query.QueryRow(ctx, `SELECT id::text,rider_identity_id::text,revision,status,started_at,ended_at,last_seen_at,zone_id,tenant_id::text,country FROM fulfillment.duty_sessions WHERE tenant_id=$1 AND country=$2 AND rider_identity_id=$3 AND status='ACTIVE' ORDER BY started_at DESC LIMIT 1`+suffix, actor.TenantID, actor.Country, actor.Subject).Scan(&value.ID, &value.RiderID, &value.Revision, &value.Status, &value.StartedAt, &value.EndedAt, &value.LastSeenAt, &value.ZoneID, &value.tenantID, &value.country)
	return value, err
}

func loadPostgresTask(ctx context.Context, query fulfillmentQuerier, tenantID, country, taskID string, lock bool) (DeliveryTask, error) {
	suffix := " WHERE tenant_id=$1 AND country=$2 AND id=$3"
	if lock {
		suffix += " FOR UPDATE"
	}
	return scanPostgresTask(query.QueryRow(ctx, taskSelect+suffix, tenantID, country, taskID))
}

func scanPostgresTask(row fulfillmentRow) (DeliveryTask, error) {
	var value DeliveryTask
	var pickup, dropoff []byte
	err := row.Scan(&value.ID, &value.tenantID, &value.country, &value.OrderID, &value.OrderType, &value.RegionID, &value.TerritoryID, &value.ZoneID, &value.Revision, &value.Status, &value.AssignedRiderID, &pickup, &dropoff, &value.DistanceMeters, &value.Earning.AmountMinor, &value.Earning.Currency, &value.OfferExpiresAt, &value.AcceptedAt, &value.PickedUpAt, &value.DeliveredAt, &value.deliveryOTPHash, &value.PODBlurredAssetID, &value.PODSignatureID, &value.ReassignmentCount, &value.UpdatedAt)
	if err != nil {
		return DeliveryTask{}, err
	}
	if json.Unmarshal(pickup, &value.Pickup) != nil || json.Unmarshal(dropoff, &value.Dropoff) != nil {
		return DeliveryTask{}, errors.New("decode fulfillment stop")
	}
	value.AllowedActions = taskAllowedActions(value.Status)
	return value, nil
}

func scanPostgresTasks(rows pgx.Rows) ([]DeliveryTask, error) {
	defer rows.Close()
	values := []DeliveryTask{}
	for rows.Next() {
		value, err := scanPostgresTask(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadPostgresLocation(ctx context.Context, query fulfillmentQuerier, tenantID, country, riderID string, lock bool) (RiderLocation, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var value RiderLocation
	err := query.QueryRow(ctx, `SELECT rider_identity_id::text,sequence,latitude,longitude,accuracy_m,updated_at,expires_at,tenant_id::text,country FROM fulfillment.rider_locations WHERE tenant_id=$1 AND country=$2 AND rider_identity_id=$3`+suffix, tenantID, country, riderID).Scan(&value.RiderID, &value.Sequence, &value.Point.Latitude, &value.Point.Longitude, &value.AccuracyM, &value.UpdatedAt, &value.ExpiresAt, &value.tenantID, &value.country)
	return value, err
}

func (service *PostgresService) updatePostgresLocation(ctx context.Context, tx pgx.Tx, actor Actor, key string, input LocationUpdate) (RiderLocation, bool, error) {
	operation := "location"
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return RiderLocation{}, false, err
	}
	fingerprint := digest(input)
	var replay RiderLocation
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return RiderLocation{}, false, err
	} else if found {
		return replay, true, nil
	}
	duty, err := loadPostgresDuty(ctx, tx, actor, true)
	if err != nil || duty.Status != "ACTIVE" {
		return RiderLocation{}, false, ErrForbidden
	}
	existing, err := loadPostgresLocation(ctx, tx, actor.TenantID, actor.Country, actor.Subject, true)
	if err == nil && input.Sequence <= existing.Sequence {
		return RiderLocation{}, false, ErrLocationStale
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RiderLocation{}, false, err
	}
	now := service.Now()
	if input.CapturedAt.After(now.Add(2*time.Minute)) || input.CapturedAt.Before(now.Add(-15*time.Minute)) {
		return RiderLocation{}, false, ErrLocationStale
	}
	policy, err := loadFulfillmentPolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return RiderLocation{}, false, err
	}
	value := RiderLocation{RiderID: actor.Subject, Sequence: input.Sequence, Point: input.Point, AccuracyM: input.AccuracyM, UpdatedAt: now, ExpiresAt: now.Add(policy.locationTTL), tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.rider_locations (rider_identity_id,tenant_id,country,sequence,latitude,longitude,accuracy_m,updated_at,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (rider_identity_id) DO UPDATE SET tenant_id=excluded.tenant_id,country=excluded.country,sequence=excluded.sequence,latitude=excluded.latitude,longitude=excluded.longitude,accuracy_m=excluded.accuracy_m,updated_at=excluded.updated_at,expires_at=excluded.expires_at`, actor.Subject, actor.TenantID, actor.Country, value.Sequence, value.Point.Latitude, value.Point.Longitude, value.AccuracyM, now, value.ExpiresAt)
	if err != nil {
		return RiderLocation{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE fulfillment.duty_sessions SET last_seen_at=$2 WHERE id=$1`, duty.ID, now)
	if err != nil {
		return RiderLocation{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return RiderLocation{}, false, err
	}
	return value, false, nil
}

func (service *PostgresService) mutateRiderTask(actor Actor, key, taskID string, revision int64, next string, completion CompletionRequest) (DeliveryTask, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "RIDER") || !validKey(key) || !fulfillmentIsUUID(taskID) {
		return DeliveryTask{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DeliveryTask{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "task:" + next + ":" + taskID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return DeliveryTask{}, false, err
	}
	fingerprint := digest(struct {
		Revision   int64
		Next       string
		Completion CompletionRequest
	}{revision, next, completion})
	var replay DeliveryTask
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return DeliveryTask{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresTask(ctx, tx, actor.TenantID, actor.Country, taskID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTask{}, false, ErrNotFound
	}
	if err != nil {
		return DeliveryTask{}, false, err
	}
	allowed := map[string]string{"PICKED_UP": "ASSIGNED", "DELIVERED": "PICKED_UP"}
	if value.AssignedRiderID != actor.Subject {
		return DeliveryTask{}, false, ErrForbidden
	}
	if value.Revision != revision || value.Status != allowed[next] {
		return DeliveryTask{}, false, ErrInvalidTransition
	}
	now := service.Now()
	if next == "PICKED_UP" {
		value.PickedUpAt = &now
		_, err = tx.Exec(ctx, `UPDATE fulfillment.delivery_tasks SET status='PICKED_UP',revision=revision+1,picked_up_at=$2,updated_at=$2 WHERE id=$1`, taskID, now)
	} else {
		if completion.OTP != "" && digest(completion.OTP) != value.deliveryOTPHash {
			return DeliveryTask{}, false, ErrForbidden
		}
		policy, policyErr := loadFulfillmentPolicy(ctx, tx, actor.TenantID, actor.Country)
		if policyErr != nil {
			return DeliveryTask{}, false, policyErr
		}
		value.DeliveredAt, value.PODBlurredAssetID, value.PODSignatureID = &now, completion.BlurredPhotoAssetID, completion.SignatureAssetID
		_, err = tx.Exec(ctx, `UPDATE fulfillment.delivery_tasks SET status='DELIVERED',revision=revision+1,delivered_at=$2,pod_blurred_asset_id=$3,pod_signature_asset_id=nullif($4,'')::uuid,updated_at=$2 WHERE id=$1`, taskID, now, completion.BlurredPhotoAssetID, completion.SignatureAssetID)
		if err == nil {
			err = service.createPostgresEarning(ctx, tx, value, policy, now)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE fulfillment.conversations SET expires_at=$2 WHERE order_id=$1`, value.OrderID, now.Add(policy.chatAfterDeliveryTTL))
		}
	}
	if err != nil {
		return DeliveryTask{}, false, err
	}
	value.Status, value.Revision, value.UpdatedAt = next, value.Revision+1, now
	value.AllowedActions = taskAllowedActions(next)
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return DeliveryTask{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeliveryTask{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) mutateTask(actor Actor, key, taskID string, revision int64, operation string, mutation func(context.Context, pgx.Tx, *DeliveryTask, fulfillmentPolicy, time.Time) error) (DeliveryTask, bool, error) {
	if !validKey(key) || !fulfillmentIsUUID(taskID) {
		return DeliveryTask{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DeliveryTask{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope := operation + ":" + taskID
	if err := fulfillmentCommandLock(ctx, tx, actor, scope, key); err != nil {
		return DeliveryTask{}, false, err
	}
	fingerprint := digest(revision)
	var replay DeliveryTask
	if found, err := loadFulfillmentReplay(ctx, tx, actor, scope, key, fingerprint, &replay); err != nil {
		return DeliveryTask{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresTask(ctx, tx, actor.TenantID, actor.Country, taskID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTask{}, false, ErrNotFound
	}
	if err != nil {
		return DeliveryTask{}, false, err
	}
	if value.Revision != revision {
		return DeliveryTask{}, false, ErrConflict
	}
	policy, err := loadFulfillmentPolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return DeliveryTask{}, false, err
	}
	now := service.Now()
	if err := mutation(ctx, tx, &value, policy, now); err != nil {
		return DeliveryTask{}, false, err
	}
	value.Revision++
	value.UpdatedAt = now
	value.AllowedActions = taskAllowedActions(value.Status)
	_, err = tx.Exec(ctx, `UPDATE fulfillment.delivery_tasks SET status=$2,revision=$3,assigned_rider_identity_id=nullif($4,'')::uuid,offer_expires_at=$5,reassignment_count=$6,updated_at=$7 WHERE id=$1`, value.ID, value.Status, value.Revision, value.AssignedRiderID, value.OfferExpiresAt, value.ReassignmentCount, now)
	if err != nil {
		return DeliveryTask{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, scope, key, fingerprint, value, now); err != nil {
		return DeliveryTask{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeliveryTask{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func loadFulfillmentPolicy(ctx context.Context, query fulfillmentQuerier, tenantID, country string) (fulfillmentPolicy, error) {
	var value fulfillmentPolicy
	var offer, location, chat, cooling int64
	err := query.QueryRow(ctx, `SELECT version,offer_ttl_seconds,location_ttl_seconds,chat_after_delivery_seconds,settlement_cooling_seconds,commission_basis_points,tax_basis_points FROM fulfillment.policies WHERE tenant_id=$1 AND country=$2`, tenantID, country).Scan(&value.version, &offer, &location, &chat, &cooling, &value.commissionBasisPoints, &value.taxBasisPoints)
	value.offerTTL, value.locationTTL, value.chatAfterDeliveryTTL, value.settlementCooling = time.Duration(offer)*time.Second, time.Duration(location)*time.Second, time.Duration(chat)*time.Second, time.Duration(cooling)*time.Second
	return value, err
}

func (service *PostgresService) createPostgresEarning(ctx context.Context, tx pgx.Tx, task DeliveryTask, policy fulfillmentPolicy, now time.Time) error {
	commission := task.Earning.AmountMinor * policy.commissionBasisPoints / 10000
	tax := commission * policy.taxBasisPoints / 10000
	_, err := tx.Exec(ctx, `INSERT INTO fulfillment.ledger_entries (id,tenant_id,country,account_identity_id,reference_id,kind,gross_minor,commission_minor,tax_minor,net_minor,currency,calculation_version,available_at,created_at) VALUES ($1,$2,$3,$4,$5,'RIDER_DELIVERY_EARNING',$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (account_identity_id,reference_id,kind) DO NOTHING`, fulfillmentUUID("ledger", task.ID+":"+task.AssignedRiderID), task.tenantID, task.country, task.AssignedRiderID, task.ID, task.Earning.AmountMinor, commission, tax, task.Earning.AmountMinor-commission-tax, task.Earning.Currency, "rider:"+policy.version, now.Add(policy.settlementCooling), now)
	return err
}

func fulfillmentCommandLock(ctx context.Context, tx pgx.Tx, actor Actor, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":"+actor.Subject+":"+operation+":"+key)
	return err
}

func loadFulfillmentReplay(ctx context.Context, query fulfillmentQuerier, actor Actor, operation, key, fingerprint string, output any) (bool, error) {
	var storedFingerprint string
	var payload []byte
	err := query.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM fulfillment.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation=$4 AND idempotency_key=$5`, actor.TenantID, actor.Country, actor.Subject, operation, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedFingerprint != fingerprint {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return false, fmt.Errorf("decode fulfillment replay: %w", err)
	}
	return true, nil
}

func storeFulfillmentReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, response any, now time.Time) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.idempotency_records (tenant_id,country,subject_id,operation,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.Subject, operation, key, fingerprint, payload, now)
	return mapFulfillmentError(err)
}

func insertFulfillmentAudit(ctx context.Context, tx pgx.Tx, actor Actor, action, resourceType, resourceID, reason string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO fulfillment.audit_events (id,tenant_id,country,actor_identity_id,action,resource_type,resource_id,reason,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, uuid.NewString(), actor.TenantID, actor.Country, actor.Subject, action, resourceType, resourceID, strings.TrimSpace(reason), now)
	return err
}

func insertFulfillmentAttendance(ctx context.Context, tx pgx.Tx, actor Actor, sessionID, kind string, point Point, source string, now time.Time) error {
	var latitude, longitude any
	if point != (Point{}) {
		latitude, longitude = point.Latitude, point.Longitude
	}
	_, err := tx.Exec(ctx, `INSERT INTO fulfillment.attendance_entries (id,tenant_id,country,subject_identity_id,session_id,kind,latitude,longitude,source,recorded_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, uuid.NewString(), actor.TenantID, actor.Country, actor.Subject, sessionID, kind, latitude, longitude, source, now)
	return err
}

func mapFulfillmentError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505", "40001", "40P01":
			return ErrConflict
		case "23503", "23514", "22P02":
			return ErrInvalidRequest
		}
	}
	return err
}

func fulfillmentUUID(kind, seed string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("planext4u:fulfillment:"+kind+":"+seed)).String()
}
func fulfillmentIsUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }
func postgresFulfillmentActor(actor Actor) bool {
	return validActor(actor) && fulfillmentIsUUID(actor.TenantID) && fulfillmentIsUUID(actor.Subject)
}

func allFulfillmentUUIDDocuments(documents []RiderDocument) bool {
	for _, document := range documents {
		if !fulfillmentIsUUID(document.AssetID) {
			return false
		}
	}
	return true
}

func validPostgresTaskSeed(value TaskSeed) bool {
	return validTaskSeed(value) && fulfillmentIsUUID(value.ID) && fulfillmentIsUUID(value.OrderID) && fulfillmentIsUUID(value.CustomerID) && fulfillmentIsUUID(value.CounterpartyID) && fulfillmentIsUUID(value.TerritoryID)
}

func riderAllowedActions(status RiderStatus) []string {
	switch status {
	case RiderKYCReview:
		return []string{"VIEW_REVIEW_STATUS"}
	case RiderApproved:
		return []string{"START_DUTY", "VIEW_EARNINGS"}
	default:
		return nil
	}
}

func taskAllowedActions(status string) []string {
	switch status {
	case "READY_FOR_DISPATCH":
		return []string{"OFFER"}
	case "OFFERED":
		return []string{"ACCEPT"}
	case "ASSIGNED":
		return []string{"NAVIGATE_PICKUP", "MARK_PICKED_UP"}
	case "PICKED_UP":
		return []string{"NAVIGATE_DROPOFF", "COMPLETE_DELIVERY"}
	case "REASSIGNMENT_REQUIRED":
		return []string{"REASSIGN"}
	default:
		return nil
	}
}

func decodeFulfillmentDigest(encoded string) ([]byte, error) { return hex.DecodeString(encoded) }

var _ Application = (*PostgresService)(nil)
var _ = regexp.MustCompile
