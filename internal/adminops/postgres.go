package adminops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminOperationTimeout = 10 * time.Second

type PostgresDomainExecutor struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresDomainExecutor(pool *pgxpool.Pool, clock func() time.Time) (*PostgresDomainExecutor, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresDomainExecutor{pool: pool, clock: clock}, nil
}

func (executor *PostgresDomainExecutor) Ready(ctx context.Context) error {
	var ready bool
	if err := executor.pool.QueryRow(ctx, `SELECT to_regclass('admin.domain_records') IS NOT NULL AND to_regclass('admin.domain_executions') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check admin domain executor readiness: %w", err)
	}
	if !ready {
		return errors.New("admin domain executor schema is unavailable")
	}
	return nil
}

func (executor *PostgresDomainExecutor) Execute(principal Principal, change Change) error {
	if !postgresPrincipal(principal) || principal.TenantID != change.TenantID || principal.Country != change.Country || change.Status == StatusRejected {
		return ErrForbidden
	}
	encoded, err := json.Marshal(change.Command)
	if err != nil {
		return ErrInvalidRequest
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin admin domain execution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, change.ID); err != nil {
		return fmt.Errorf("lock admin domain execution: %w", err)
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT request_fingerprint FROM admin.domain_executions WHERE change_id=$1`, change.ID).Scan(&previous)
	if err == nil {
		if previous != fingerprint {
			return ErrRevisionConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load admin domain execution: %w", err)
	}
	current := DomainRecord{TenantID: change.TenantID, Country: change.Country, Domain: change.Command.Domain, TargetID: change.Command.TargetID, Payload: map[string]any{}}
	var payload []byte
	err = tx.QueryRow(ctx, `
		SELECT state,revision,payload,last_change,updated_by::text,updated_at
		FROM admin.domain_records
		WHERE tenant_id=$1 AND country=$2 AND domain=$3 AND target_id=$4 FOR UPDATE`,
		change.TenantID, change.Country, change.Command.Domain, change.Command.TargetID).Scan(
		&current.State, &current.Revision, &payload, &current.LastChange, &current.UpdatedBy, &current.UpdatedAt)
	if err == nil {
		if json.Unmarshal(payload, &current.Payload) != nil {
			return ErrExecutionFailed
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load admin domain record: %w", err)
	}
	state, nextPayload, err := applyDomainCommand(current, change.Command)
	if err != nil {
		return err
	}
	payload, _ = json.Marshal(nextPayload)
	now := executor.clock().UTC()
	if current.Revision == 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO admin.domain_records
			(tenant_id,country,domain,target_id,state,revision,payload,last_change,updated_by,updated_at)
			VALUES ($1,$2,$3,$4,$5,1,$6::jsonb,$7,$8,$9)`, change.TenantID, change.Country, change.Command.Domain,
			change.Command.TargetID, state, payload, change.ID, principal.SubjectID, now)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE admin.domain_records SET state=$5,revision=revision+1,payload=$6::jsonb,last_change=$7,updated_by=$8,updated_at=$9
			WHERE tenant_id=$1 AND country=$2 AND domain=$3 AND target_id=$4`, change.TenantID, change.Country, change.Command.Domain,
			change.Command.TargetID, state, payload, change.ID, principal.SubjectID, now)
	}
	if err != nil {
		return fmt.Errorf("persist admin domain record: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO admin.domain_executions (change_id,request_fingerprint,executed_at) VALUES ($1,$2,$3)`, change.ID, fingerprint, now); err != nil {
		return fmt.Errorf("persist admin domain execution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit admin domain execution: %w", err)
	}
	return nil
}

func (executor *PostgresDomainExecutor) Record(ctx context.Context, tenantID, country string, domain Domain, targetID string) (DomainRecord, bool, error) {
	if !canonicalUUID(tenantID) || len(country) != 2 || capability(domain) == "" || !safeID(targetID) {
		return DomainRecord{}, false, ErrInvalidRequest
	}
	var value DomainRecord
	var payload []byte
	err := executor.pool.QueryRow(ctx, `
		SELECT state,revision,payload,last_change,updated_by::text,updated_at
		FROM admin.domain_records WHERE tenant_id=$1 AND country=$2 AND domain=$3 AND target_id=$4`, tenantID, country, domain, targetID).Scan(
		&value.State, &value.Revision, &payload, &value.LastChange, &value.UpdatedBy, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DomainRecord{}, false, nil
	}
	if err != nil {
		return DomainRecord{}, false, fmt.Errorf("load admin domain record: %w", err)
	}
	if json.Unmarshal(payload, &value.Payload) != nil {
		return DomainRecord{}, false, ErrExecutionFailed
	}
	value.TenantID, value.Country, value.Domain, value.TargetID = tenantID, country, domain, targetID
	return value, true, nil
}

type PostgresService struct {
	pool     *pgxpool.Pool
	clock    func() time.Time
	executor Executor
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time, executor Executor) (*PostgresService, error) {
	if pool == nil || clock == nil || executor == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock, executor: executor}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `SELECT to_regclass('admin.changes') IS NOT NULL AND to_regclass('admin.operation_events') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check admin operations readiness: %w", err)
	}
	if !ready {
		return errors.New("admin operations schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Submit(principal Principal, command Command) (Change, error) {
	if !postgresPrincipal(principal) || !validCommand(command) || !principal.Capabilities[capability(command.Domain)] {
		return Change{}, ErrForbidden
	}
	risk := commandRisk(command)
	if !mfaSatisfied(principal) || risk == RiskHigh && !fresh(principal, service.clock().UTC()) {
		return Change{}, ErrFreshMFARequired
	}
	encoded, _ := json.Marshal(struct {
		Tenant, Country, Actor string
		Command                Command
	}{principal.TenantID, principal.Country, principal.SubjectID, command})
	digest := sha256.Sum256(encoded)
	now := service.clock().UTC()
	status := StatusExecuted
	if risk == RiskHigh {
		status = StatusPending
	}
	value := Change{ID: "change-" + hex.EncodeToString(digest[:8]), Revision: 1, TenantID: principal.TenantID, Country: principal.Country, Command: cloneCommand(command), Risk: risk, Status: status, RequestedBy: principal.SubjectID, CreatedAt: now, UpdatedAt: now}
	if existing, found, err := service.change(context.Background(), value.ID); err != nil {
		return Change{}, err
	} else if found {
		return existing, nil
	}
	if status == StatusExecuted {
		if err := service.executor.Execute(principal, value); err != nil {
			return Change{}, ErrExecutionFailed
		}
	}
	commandPayload, _ := json.Marshal(value.Command)
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Change{}, fmt.Errorf("begin admin change persistence: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	commandTag, err := transaction.Exec(ctx, `
		INSERT INTO admin.changes (id,revision,tenant_id,country,command,risk,status,requested_by,created_at,updated_at)
		VALUES ($1,1,$2,$3,$4::jsonb,$5,$6,$7,$8,$8) ON CONFLICT (id) DO NOTHING`,
		value.ID, value.TenantID, value.Country, commandPayload, value.Risk, value.Status, value.RequestedBy, now)
	if err != nil {
		return Change{}, fmt.Errorf("persist admin change: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return service.mustChange(ctx, value.ID)
	}
	if err := appendEvent(ctx, transaction, principal, command, "SUCCEEDED", string(status), now); err != nil {
		return Change{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Change{}, fmt.Errorf("commit admin change persistence: %w", err)
	}
	return service.mustChange(ctx, value.ID)
}

func (service *PostgresService) Approve(principal Principal, changeID string, expectedRevision int64) (Change, error) {
	if !postgresPrincipal(principal) || !safeID(changeID) || expectedRevision < 1 || !mfaSatisfied(principal) || !fresh(principal, service.clock().UTC()) {
		return Change{}, ErrFreshMFARequired
	}
	value, found, err := service.change(context.Background(), changeID)
	if err != nil {
		return Change{}, err
	}
	if !found || value.TenantID != principal.TenantID || value.Country != principal.Country {
		return Change{}, ErrChangeNotFound
	}
	if !principal.Capabilities[capability(value.Command.Domain)] {
		return Change{}, ErrForbidden
	}
	if value.RequestedBy == principal.SubjectID {
		return Change{}, ErrFourEyesRequired
	}
	if value.Revision != expectedRevision {
		return Change{}, ErrRevisionConflict
	}
	if value.Status != StatusPending {
		return Change{}, ErrInvalidState
	}
	if err := service.executor.Execute(principal, value); err != nil {
		return Change{}, ErrExecutionFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	now := service.clock().UTC()
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Change{}, fmt.Errorf("begin admin change approval: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	command, err := transaction.Exec(ctx, `
		UPDATE admin.changes SET status='EXECUTED',approved_by=$2,revision=revision+1,updated_at=$3
		WHERE id=$1 AND revision=$4 AND status='PENDING_APPROVAL'`, changeID, principal.SubjectID, now, expectedRevision)
	if err != nil {
		return Change{}, fmt.Errorf("approve admin change: %w", err)
	}
	if command.RowsAffected() != 1 {
		return Change{}, ErrRevisionConflict
	}
	if err := appendEvent(ctx, transaction, principal, value.Command, "SUCCEEDED", "FOUR_EYES_APPROVED", now); err != nil {
		return Change{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Change{}, fmt.Errorf("commit admin change approval: %w", err)
	}
	return service.mustChange(ctx, changeID)
}

func (service *PostgresService) Reject(principal Principal, changeID string, expectedRevision int64, reason string) (Change, error) {
	if !postgresPrincipal(principal) || !safeID(changeID) || expectedRevision < 1 || !safeReason(reason) || !mfaSatisfied(principal) || !fresh(principal, service.clock().UTC()) {
		return Change{}, ErrFreshMFARequired
	}
	value, found, err := service.change(context.Background(), changeID)
	if err != nil {
		return Change{}, err
	}
	if !found || value.TenantID != principal.TenantID || value.Country != principal.Country {
		return Change{}, ErrChangeNotFound
	}
	if !principal.Capabilities[capability(value.Command.Domain)] {
		return Change{}, ErrForbidden
	}
	if value.RequestedBy == principal.SubjectID {
		return Change{}, ErrFourEyesRequired
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Change{}, fmt.Errorf("begin admin change rejection: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	now := service.clock().UTC()
	command, err := transaction.Exec(ctx, `
		UPDATE admin.changes SET status='REJECTED',approved_by=$2,revision=revision+1,updated_at=$3
		WHERE id=$1 AND revision=$4 AND status='PENDING_APPROVAL'`, changeID, principal.SubjectID, now, expectedRevision)
	if err != nil {
		return Change{}, fmt.Errorf("reject admin change: %w", err)
	}
	if command.RowsAffected() != 1 {
		if value.Revision != expectedRevision {
			return Change{}, ErrRevisionConflict
		}
		return Change{}, ErrInvalidState
	}
	if err := appendEvent(ctx, transaction, principal, value.Command, "SUCCEEDED", "FOUR_EYES_REJECTED", now); err != nil {
		return Change{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Change{}, fmt.Errorf("commit admin change rejection: %w", err)
	}
	return service.mustChange(ctx, changeID)
}

func (service *PostgresService) List(principal Principal) ([]Change, error) {
	if !postgresPrincipal(principal) {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `
		SELECT id,revision,tenant_id::text,country,command,risk,status,requested_by::text,approved_by::text,created_at,updated_at
		FROM admin.changes WHERE tenant_id=$1 AND country=$2 ORDER BY created_at DESC,id`, principal.TenantID, principal.Country)
	if err != nil {
		return nil, fmt.Errorf("list admin changes: %w", err)
	}
	defer rows.Close()
	result := []Change{}
	for rows.Next() {
		value, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		if principal.Capabilities[capability(value.Command.Domain)] {
			result = append(result, value)
		}
	}
	return result, rows.Err()
}

func (service *PostgresService) Audit(principal Principal) ([]AuditEvent, error) {
	if !postgresPrincipal(principal) || !principal.Capabilities[CapabilityReporting] {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `
		SELECT sequence_id,tenant_id::text,country,actor_id::text,action,target_id,outcome,reason,correlation_id,created_at
		FROM admin.operation_events WHERE tenant_id=$1 AND country=$2 ORDER BY sequence_id`, principal.TenantID, principal.Country)
	if err != nil {
		return nil, fmt.Errorf("list admin operation events: %w", err)
	}
	defer rows.Close()
	result := []AuditEvent{}
	for rows.Next() {
		var value AuditEvent
		if err := rows.Scan(&value.Sequence, &value.TenantID, &value.Country, &value.ActorID, &value.Action, &value.TargetID, &value.Outcome, &value.Reason, &value.CorrelationID, &value.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan admin operation event: %w", err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (service *PostgresService) change(parent context.Context, id string) (Change, bool, error) {
	ctx, cancel := context.WithTimeout(parent, adminOperationTimeout)
	defer cancel()
	value, err := scanChange(service.pool.QueryRow(ctx, `
		SELECT id,revision,tenant_id::text,country,command,risk,status,requested_by::text,approved_by::text,created_at,updated_at
		FROM admin.changes WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Change{}, false, nil
	}
	return value, err == nil, err
}

func (service *PostgresService) mustChange(ctx context.Context, id string) (Change, error) {
	value, found, err := service.change(ctx, id)
	if err != nil {
		return Change{}, err
	}
	if !found {
		return Change{}, ErrChangeNotFound
	}
	return value, nil
}

type changeScanner interface{ Scan(...any) error }

func scanChange(row changeScanner) (Change, error) {
	var value Change
	var commandPayload []byte
	var approved *string
	if err := row.Scan(&value.ID, &value.Revision, &value.TenantID, &value.Country, &commandPayload, &value.Risk, &value.Status, &value.RequestedBy, &approved, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return Change{}, err
	}
	if approved != nil {
		value.ApprovedBy = *approved
	}
	if json.Unmarshal(commandPayload, &value.Command) != nil || !validCommand(value.Command) {
		return Change{}, ErrInvalidRequest
	}
	return value, nil
}

type eventExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func appendEvent(ctx context.Context, executor eventExecer, principal Principal, command Command, outcome, reason string, now time.Time) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO admin.operation_events (tenant_id,country,actor_id,action,target_id,outcome,reason,correlation_id,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, principal.TenantID, principal.Country, principal.SubjectID,
		string(command.Domain)+"."+command.Action, command.TargetID, outcome, reason, command.CorrelationID, now)
	if err != nil {
		return fmt.Errorf("append admin operation event: %w", err)
	}
	return nil
}

func postgresPrincipal(value Principal) bool {
	return validPrincipal(value) && canonicalUUID(value.TenantID) && canonicalUUID(value.SubjectID)
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
