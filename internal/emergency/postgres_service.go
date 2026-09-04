package emergency

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const emergencyOperationTimeout = 15 * time.Second

type emergencyPolicy struct {
	version        string
	assignmentSLA  time.Duration
	locationMaxAge time.Duration
}

type PostgresService struct {
	pool  *pgxpool.Pool
	clock func() time.Time
	aead  cipher.AEAD
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time, encryptionKey []byte) (*PostgresService, error) {
	if pool == nil || clock == nil || len(encryptionKey) != 32 {
		return nil, ErrInvalidRequest
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock, aead: aead}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	err := service.pool.QueryRow(ctx, `SELECT to_regclass('emergency.requests') IS NOT NULL AND to_regclass('emergency.policies') IS NOT NULL AND to_regclass('emergency.idempotency_records') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check emergency schema readiness: %w", err)
	}
	if !ready {
		return errors.New("emergency schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Create(actor Actor, key string, input CreateRequest) (Request, bool, error) {
	category, priority, description := strings.ToUpper(strings.TrimSpace(input.Category)), strings.ToUpper(strings.TrimSpace(input.Priority)), strings.TrimSpace(input.Description)
	if !postgresEmergencyActor(actor) || (!customer(actor) && !hasRole(actor, "RIDER")) || !validKey(key) || !input.LocationConsent || !map[string]bool{"MEDICAL": true, "SAFETY": true, "FIRE": true, "ACCIDENT": true, "OTHER": true}[category] || !map[string]bool{"HIGH": true, "CRITICAL": true}[priority] || len(description) < 5 || len(description) > 2000 || !validLocation(input.Location) {
		return Request{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Request{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := emergencyCommandLock(ctx, tx, actor, "create", key); err != nil {
		return Request{}, false, err
	}
	fingerprint := digest(input)
	var replay Request
	if found, err := loadEmergencyReplay(ctx, tx, actor, "create", key, fingerprint, &replay); err != nil {
		return Request{}, false, err
	} else if found {
		return replay, true, nil
	}
	policy, err := loadEmergencyPolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return Request{}, false, err
	}
	now := service.Now()
	location := input.Location
	location.CapturedAt = now
	id := uuid.NewString()
	plaintext, _ := json.Marshal(location)
	ciphertext, err := service.encrypt(plaintext, id+":location")
	if err != nil {
		return Request{}, false, err
	}
	value := Request{ID: id, Revision: 1, RequesterID: actor.Subject, Category: category, Description: description, Priority: priority, Status: "OPEN", LocationConsent: true, EscalationLevel: 0, SLADeadline: now.Add(policy.assignmentSLA), CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country, lastLocation: &location, encryptedLocation: ciphertext}
	_, err = tx.Exec(ctx, `INSERT INTO emergency.requests (id,tenant_id,country,requester_identity_id,revision,category,description,priority,status,location_consent,encrypted_location,location_captured_at,escalation_level,sla_deadline,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,'OPEN',true,$8,$9,0,$10,$9,$9)`, id, actor.TenantID, actor.Country, actor.Subject, category, description, priority, ciphertext, now, value.SLADeadline)
	if err != nil {
		return Request{}, false, mapEmergencyError(err)
	}
	if err := insertEmergencyTimeline(ctx, tx, actor, id, "CREATED", map[string]any{"priority": priority, "policy_version": policy.version}, now); err != nil {
		return Request{}, false, err
	}
	presented, err := service.presentValue(ctx, tx, actor, value)
	if err != nil {
		return Request{}, false, err
	}
	if err := storeEmergencyReplay(ctx, tx, actor, "create", key, fingerprint, presented, now); err != nil {
		return Request{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, false, mapEmergencyError(err)
	}
	return presented, false, nil
}

func (service *PostgresService) Get(actor Actor, id string) (Request, error) {
	if !postgresEmergencyActor(actor) || !emergencyUUID(id) {
		return Request{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	value, err := loadEmergencyRequest(ctx, service.pool, actor.TenantID, actor.Country, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canViewEmergency(actor, value) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	return service.presentValue(ctx, service.pool, actor, value)
}

func (service *PostgresService) List(actor Actor) ([]Request, error) {
	if !postgresEmergencyActor(actor) {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	query := emergencyRequestSelect + ` WHERE tenant_id=$1 AND country=$2`
	args := []any{actor.TenantID, actor.Country}
	if !responder(actor) && !administrator(actor) {
		query += ` AND (requester_identity_id=$3 OR assigned_responder_identity_id=$3)`
		args = append(args, actor.Subject)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := service.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Request{}
	for rows.Next() {
		value, err := scanEmergencyRequest(rows)
		if err != nil {
			return nil, err
		}
		presented, err := service.presentValue(ctx, service.pool, actor, value)
		if err != nil {
			return nil, err
		}
		values = append(values, presented)
	}
	return values, rows.Err()
}

func (service *PostgresService) Accept(actor Actor, key, id string) (Request, bool, error) {
	if !postgresEmergencyActor(actor) || !responder(actor) || !validKey(key) || !emergencyUUID(id) {
		if !actor.MFAVerified {
			return Request{}, false, ErrMFARequired
		}
		return Request{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Request{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "accept:" + id
	if err := emergencyCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Request{}, false, err
	}
	fingerprint := digest(id)
	var replay Request
	if found, err := loadEmergencyReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Request{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadEmergencyRequest(ctx, tx, actor.TenantID, actor.Country, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, false, ErrNotFound
	}
	if err != nil {
		return Request{}, false, mapEmergencyError(err)
	}
	if value.Status != "OPEN" || value.AssignedResponder != "" {
		return Request{}, false, ErrConflict
	}
	now := service.Now()
	value.AssignedResponder, value.Status, value.AcceptedAt, value.UpdatedAt, value.Revision = actor.Subject, "ASSIGNED", &now, now, value.Revision+1
	_, err = tx.Exec(ctx, `UPDATE emergency.requests SET assigned_responder_identity_id=$2,status='ASSIGNED',accepted_at=$3,updated_at=$3,revision=revision+1 WHERE id=$1`, id, actor.Subject, now)
	if err != nil {
		return Request{}, false, err
	}
	if err := insertEmergencyTimeline(ctx, tx, actor, id, "ASSIGNED", map[string]any{"responder_id": actor.Subject}, now); err != nil {
		return Request{}, false, err
	}
	presented, err := service.presentValue(ctx, tx, actor, value)
	if err != nil {
		return Request{}, false, err
	}
	if err := storeEmergencyReplay(ctx, tx, actor, operation, key, fingerprint, presented, now); err != nil {
		return Request{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, false, mapEmergencyError(err)
	}
	return presented, false, nil
}

func (service *PostgresService) UpdateLocation(actor Actor, id string, input LocationRequest) (Request, error) {
	if !postgresEmergencyActor(actor) || !emergencyUUID(id) || input.Consent && !validLocation(input.Location) {
		return Request{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Request{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, err := loadEmergencyRequest(ctx, tx, actor.TenantID, actor.Country, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if actor.Subject != value.RequesterID && actor.Subject != value.AssignedResponder {
		return Request{}, ErrForbidden
	}
	now := service.Now()
	value.LocationConsent, value.UpdatedAt, value.Revision = input.Consent, now, value.Revision+1
	var ciphertext []byte
	var capturedAt any
	if input.Consent {
		location := input.Location
		location.CapturedAt = now
		plaintext, _ := json.Marshal(location)
		ciphertext, err = service.encrypt(plaintext, id+":location")
		if err != nil {
			return Request{}, err
		}
		capturedAt, value.lastLocation = now, &location
	} else {
		value.lastLocation = nil
	}
	value.encryptedLocation = ciphertext
	_, err = tx.Exec(ctx, `UPDATE emergency.requests SET location_consent=$2,encrypted_location=$3,location_captured_at=$4,updated_at=$5,revision=revision+1 WHERE id=$1`, id, input.Consent, ciphertext, capturedAt, now)
	if err != nil {
		return Request{}, err
	}
	event := "LOCATION_UPDATED"
	if !input.Consent {
		event = "LOCATION_REVOKED"
	}
	if err := insertEmergencyTimeline(ctx, tx, actor, id, event, map[string]any{"consent": input.Consent}, now); err != nil {
		return Request{}, err
	}
	presented, err := service.presentValue(ctx, tx, actor, value)
	if err != nil {
		return Request{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, mapEmergencyError(err)
	}
	return presented, nil
}

func (service *PostgresService) Transition(actor Actor, key, id string, revision int64, input TransitionRequest) (Request, bool, error) {
	status, note := strings.ToUpper(strings.TrimSpace(input.Status)), strings.TrimSpace(input.Note)
	if !postgresEmergencyActor(actor) || !responder(actor) || !validKey(key) || !emergencyUUID(id) || revision < 1 || !map[string]bool{"EN_ROUTE": true, "ON_SCENE": true, "RESOLVED": true}[status] || len(note) < 4 {
		if !actor.MFAVerified {
			return Request{}, false, ErrMFARequired
		}
		return Request{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Request{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "transition:" + id
	if err := emergencyCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Request{}, false, err
	}
	fingerprint := digest(input)
	var replay Request
	if found, err := loadEmergencyReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Request{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadEmergencyRequest(ctx, tx, actor.TenantID, actor.Country, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, false, ErrNotFound
	}
	if err != nil {
		return Request{}, false, err
	}
	if value.AssignedResponder != actor.Subject {
		return Request{}, false, ErrForbidden
	}
	if value.Revision != revision || !validTransition(value.Status, status) {
		return Request{}, false, ErrConflict
	}
	now := service.Now()
	value.Status, value.UpdatedAt, value.Revision = status, now, value.Revision+1
	var resolvedAt any
	var encryptedLocation any = value.encryptedLocation
	var locationCaptured any = value.CurrentLocation
	if status == "RESOLVED" {
		value.ResolvedAt, value.lastLocation, value.encryptedLocation = &now, nil, nil
		resolvedAt, encryptedLocation, locationCaptured = now, nil, nil
	}
	_, err = tx.Exec(ctx, `UPDATE emergency.requests SET status=$2,revision=revision+1,resolved_at=$3,encrypted_location=$4,location_captured_at=CASE WHEN $2='RESOLVED' THEN NULL ELSE location_captured_at END,updated_at=$5 WHERE id=$1`, id, status, resolvedAt, encryptedLocation, now)
	_ = locationCaptured
	if err != nil {
		return Request{}, false, err
	}
	if err := insertEmergencyTimeline(ctx, tx, actor, id, status, map[string]any{"note": note}, now); err != nil {
		return Request{}, false, err
	}
	presented, err := service.presentValue(ctx, tx, actor, value)
	if err != nil {
		return Request{}, false, err
	}
	if err := storeEmergencyReplay(ctx, tx, actor, operation, key, fingerprint, presented, now); err != nil {
		return Request{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, false, mapEmergencyError(err)
	}
	return presented, false, nil
}

func (service *PostgresService) Messages(actor Actor, id string) ([]Message, error) {
	if !postgresEmergencyActor(actor) || !emergencyUUID(id) {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	request, err := loadEmergencyRequest(ctx, service.pool, actor.TenantID, actor.Country, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canViewEmergency(actor, request) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text,request_id::text,sender_identity_id::text,body_ciphertext,delivery_status,created_at FROM emergency.communications WHERE tenant_id=$1 AND country=$2 AND request_id=$3 ORDER BY created_at`, actor.TenantID, actor.Country, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Message{}
	for rows.Next() {
		var value Message
		var ciphertext []byte
		if err := rows.Scan(&value.ID, &value.RequestID, &value.SenderID, &ciphertext, &value.Status, &value.CreatedAt); err != nil {
			return nil, err
		}
		plaintext, err := service.decrypt(ciphertext, id+":message:"+value.ID)
		if err != nil {
			return nil, err
		}
		value.Body = string(plaintext)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) SendMessage(actor Actor, key, id string, input MessageRequest) (Message, bool, error) {
	body := strings.TrimSpace(input.Body)
	if !postgresEmergencyActor(actor) || !validKey(key) || !emergencyUUID(id) || len([]rune(body)) < 1 || len([]rune(body)) > 2000 {
		return Message{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Message{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "message:" + id
	if err := emergencyCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Message{}, false, err
	}
	fingerprint := digest(input)
	var replay Message
	if found, err := loadEmergencyReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Message{}, false, err
	} else if found {
		return replay, true, nil
	}
	request, err := loadEmergencyRequest(ctx, tx, actor.TenantID, actor.Country, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, ErrNotFound
	}
	if err != nil {
		return Message{}, false, err
	}
	if request.Status == "RESOLVED" || actor.Subject != request.RequesterID && actor.Subject != request.AssignedResponder {
		return Message{}, false, ErrForbidden
	}
	now := service.Now()
	value := Message{ID: uuid.NewString(), RequestID: id, SenderID: actor.Subject, Body: body, Status: "DELIVERED", CreatedAt: now}
	ciphertext, err := service.encrypt([]byte(body), id+":message:"+value.ID)
	if err != nil {
		return Message{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO emergency.communications (id,tenant_id,country,request_id,sender_identity_id,body_ciphertext,delivery_status,created_at) VALUES ($1,$2,$3,$4,$5,$6,'DELIVERED',$7)`, value.ID, actor.TenantID, actor.Country, id, actor.Subject, ciphertext, now)
	if err != nil {
		return Message{}, false, err
	}
	if err := insertEmergencyTimeline(ctx, tx, actor, id, "MESSAGE_SENT", map[string]any{"message_id": value.ID}, now); err != nil {
		return Message{}, false, err
	}
	if err := storeEmergencyReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Message{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Message{}, false, mapEmergencyError(err)
	}
	return value, false, nil
}

func (service *PostgresService) RunEscalations(actor Actor) (int, error) {
	if !postgresEmergencyActor(actor) || !administrator(actor) {
		if !actor.MFAVerified {
			return 0, ErrMFARequired
		}
		return 0, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	policy, err := loadEmergencyPolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return 0, err
	}
	now := service.Now()
	rows, err := tx.Query(ctx, `UPDATE emergency.requests SET escalation_level=escalation_level+1,sla_deadline=$4,updated_at=$3,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND status='OPEN' AND sla_deadline<=$3 RETURNING id::text,escalation_level`, actor.TenantID, actor.Country, now, now.Add(policy.assignmentSLA))
	if err != nil {
		return 0, err
	}
	type escalation struct {
		id    string
		level int
	}
	values := []escalation{}
	for rows.Next() {
		var value escalation
		if err := rows.Scan(&value.id, &value.level); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	rows.Close()
	for _, value := range values {
		if err := insertEmergencyTimeline(ctx, tx, actor, value.id, "ESCALATED", map[string]any{"level": value.level}, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, mapEmergencyError(err)
	}
	return len(values), nil
}

func (service *PostgresService) SLA(actor Actor) (SLAReport, error) {
	if !postgresEmergencyActor(actor) || !administrator(actor) {
		if !actor.MFAVerified {
			return SLAReport{}, ErrMFARequired
		}
		return SLAReport{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), emergencyOperationTimeout)
	defer cancel()
	report := SLAReport{GeneratedAt: service.Now(), ByCategory: map[string]int{}, LocationPrecision: "aggregate_only"}
	err := service.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='OPEN'),count(*) FILTER (WHERE status NOT IN ('OPEN','RESOLVED')),count(*) FILTER (WHERE status='RESOLVED'),count(*) FILTER (WHERE escalation_level>0),coalesce(avg(extract(epoch FROM accepted_at-created_at)) FILTER (WHERE accepted_at IS NOT NULL),0)::bigint FROM emergency.requests WHERE tenant_id=$1 AND country=$2`, actor.TenantID, actor.Country).Scan(&report.Open, &report.Assigned, &report.Resolved, &report.Breached, &report.AverageAcceptSecs)
	if err != nil {
		return SLAReport{}, err
	}
	rows, err := service.pool.Query(ctx, `SELECT category,count(*) FROM emergency.requests WHERE tenant_id=$1 AND country=$2 GROUP BY category`, actor.TenantID, actor.Country)
	if err != nil {
		return SLAReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var category string
		var count int
		if err := rows.Scan(&category, &count); err != nil {
			return SLAReport{}, err
		}
		report.ByCategory[category] = count
	}
	return report, rows.Err()
}

type emergencyRow interface{ Scan(...any) error }
type emergencyQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const emergencyRequestSelect = `SELECT id::text,revision,requester_identity_id::text,category,description,priority,status,coalesce(assigned_responder_identity_id::text,''),location_consent,encrypted_location,location_captured_at,escalation_level,sla_deadline,accepted_at,resolved_at,created_at,updated_at,tenant_id::text,country FROM emergency.requests`

func loadEmergencyRequest(ctx context.Context, query emergencyQuerier, tenantID, country, id string, lock bool) (Request, error) {
	suffix := ` WHERE tenant_id=$1 AND country=$2 AND id=$3`
	if lock {
		suffix += ` FOR UPDATE`
	}
	return scanEmergencyRequest(query.QueryRow(ctx, emergencyRequestSelect+suffix, tenantID, country, id))
}

func scanEmergencyRequest(row emergencyRow) (Request, error) {
	var value Request
	var capturedAt *time.Time
	err := row.Scan(&value.ID, &value.Revision, &value.RequesterID, &value.Category, &value.Description, &value.Priority, &value.Status, &value.AssignedResponder, &value.LocationConsent, &value.encryptedLocation, &capturedAt, &value.EscalationLevel, &value.SLADeadline, &value.AcceptedAt, &value.ResolvedAt, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country)
	return value, err
}

func (service *PostgresService) presentValue(ctx context.Context, query emergencyQuerier, actor Actor, value Request) (Request, error) {
	value.AllowedActions = []string{"VIEW"}
	value.CurrentLocation = nil
	if value.LocationConsent && len(value.encryptedLocation) > 0 && canSeeEmergencyLocation(actor, value) {
		policy, err := loadEmergencyPolicy(ctx, query, value.tenantID, value.country)
		if err != nil {
			return Request{}, err
		}
		plaintext, err := service.decrypt(value.encryptedLocation, value.ID+":location")
		if err != nil {
			return Request{}, err
		}
		var location Location
		if err := json.Unmarshal(plaintext, &location); err != nil {
			return Request{}, err
		}
		if service.Now().Sub(location.CapturedAt) <= policy.locationMaxAge {
			value.CurrentLocation = &location
		}
	}
	if actor.Subject == value.RequesterID && value.Status != "RESOLVED" {
		value.AllowedActions = []string{"UPDATE_LOCATION", "REVOKE_LOCATION"}
	}
	if actor.Subject == value.AssignedResponder {
		value.AllowedActions = []string{"UPDATE_LOCATION", "EN_ROUTE", "ON_SCENE", "RESOLVE"}
	}
	if responder(actor) && value.Status == "OPEN" {
		value.AllowedActions = []string{"ACCEPT"}
	}
	value.encryptedLocation, value.lastLocation = nil, nil
	return value, nil
}

func canViewEmergency(actor Actor, value Request) bool {
	return actor.Subject == value.RequesterID || actor.Subject == value.AssignedResponder || responder(actor) || administrator(actor)
}
func canSeeEmergencyLocation(actor Actor, value Request) bool {
	return actor.Subject == value.RequesterID || actor.Subject == value.AssignedResponder || administrator(actor)
}

func loadEmergencyPolicy(ctx context.Context, query emergencyQuerier, tenantID, country string) (emergencyPolicy, error) {
	var value emergencyPolicy
	var assignment, maxAge int64
	err := query.QueryRow(ctx, `SELECT version,assignment_sla_seconds,location_max_age_seconds FROM emergency.policies WHERE tenant_id=$1 AND country=$2`, tenantID, country).Scan(&value.version, &assignment, &maxAge)
	value.assignmentSLA, value.locationMaxAge = time.Duration(assignment)*time.Second, time.Duration(maxAge)*time.Second
	return value, err
}

func insertEmergencyTimeline(ctx context.Context, tx pgx.Tx, actor Actor, requestID, event string, detail any, now time.Time) error {
	encoded, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, `INSERT INTO emergency.timeline (id,tenant_id,country,request_id,actor_identity_id,event_type,detail,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.NewString(), actor.TenantID, actor.Country, requestID, actor.Subject, event, encoded, now)
	return err
}

func emergencyCommandLock(ctx context.Context, tx pgx.Tx, actor Actor, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Subject+":"+operation+":"+key)
	return err
}

func loadEmergencyReplay(ctx context.Context, query emergencyQuerier, actor Actor, operation, key, fingerprint string, output any) (bool, error) {
	var stored string
	var payload []byte
	err := query.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM emergency.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation=$4 AND idempotency_key=$5`, actor.TenantID, actor.Country, actor.Subject, operation, key).Scan(&stored, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != fingerprint {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return false, err
	}
	return true, nil
}

func storeEmergencyReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, response any, now time.Time) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO emergency.idempotency_records (tenant_id,country,subject_id,operation,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.Subject, operation, key, fingerprint, payload, now)
	return mapEmergencyError(err)
}

func (service *PostgresService) encrypt(plaintext []byte, aad string) ([]byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.aead.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

func (service *PostgresService) decrypt(ciphertext []byte, aad string) ([]byte, error) {
	if len(ciphertext) <= service.aead.NonceSize() {
		return nil, errors.New("emergency ciphertext is invalid")
	}
	nonce := ciphertext[:service.aead.NonceSize()]
	plaintext, err := service.aead.Open(nil, nonce, ciphertext[service.aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errors.New("emergency ciphertext authentication failed")
	}
	return plaintext, nil
}

func mapEmergencyError(err error) error {
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

func emergencyUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }
func postgresEmergencyActor(actor Actor) bool {
	return validActor(actor) && emergencyUUID(actor.TenantID) && emergencyUUID(actor.Subject) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(actor.Country)
}

var _ Application = (*PostgresService)(nil)
