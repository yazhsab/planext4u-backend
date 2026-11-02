package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const maxDeletionSweep = 100

func (repository *PostgresRepository) DeletionReady(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `SELECT to_regclass('identity.deletion_owner_requirements') IS NOT NULL
	 AND to_regclass('identity.deletion_country_policies') IS NOT NULL
	 AND to_regclass('identity.deletion_owner_receipts') IS NOT NULL
	 AND to_regclass('identity.deletion_legal_holds') IS NOT NULL
	 AND to_regclass('identity.deletion_owner_tasks') IS NOT NULL
	 AND to_regprocedure('identity.propose_deletion_policy(uuid,uuid,character,text,text,interval,bigint,text,uuid,uuid,bigint)') IS NOT NULL
	 AND to_regprocedure('identity.release_deletion_hold(bigint,uuid,character,bigint,text,uuid,uuid,bigint)') IS NOT NULL
	 AND to_regprocedure('identity.deletion_terminal_task_ack_gate_version()') IS NOT NULL
	 AND to_regprocedure('identity.guard_deletion_completed()') IS NOT NULL
	 AND EXISTS(SELECT 1 FROM pg_trigger
	            WHERE tgrelid=to_regclass('identity.account_deletion_requests')
	              AND tgname='account_deletion_completion_guard' AND tgenabled IN ('O','A'))`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return errors.New("deletion coordination schema is unavailable")
	}
	return nil
}

// PrepareDeletionTasks persists an idempotent owner work inventory for due
// requests. It never erases data, writes owner receipts or marks a request
// completed. SKIP LOCKED lets another identity process resume safely.
func (repository *PostgresRepository) PrepareDeletionTasks(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > maxDeletionSweep {
		return 0, ErrInvalidInput
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT d.id FROM identity.account_deletion_requests d
	 WHERE d.status='SCHEDULED' AND d.effective_at<=clock_timestamp()
	 ORDER BY COALESCE((SELECT min(t.checked_at) FROM identity.deletion_owner_tasks t
	                    WHERE t.request_id=d.id),'-infinity'::timestamptz),d.effective_at,d.id
	 LIMIT $1 FOR UPDATE OF d SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	requests := []string{}
	for rows.Next() {
		var requestID string
		if err = rows.Scan(&requestID); err != nil {
			rows.Close()
			return 0, err
		}
		requests = append(requests, requestID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, requestID := range requests {
		if _, err = tx.Exec(ctx, `INSERT INTO identity.deletion_owner_tasks(request_id,owner,status,updated_at)
		 SELECT $1,owner,'WAITING_POLICY',clock_timestamp() FROM identity.deletion_owner_requirements WHERE true
		 ON CONFLICT(request_id,owner) DO NOTHING`, requestID); err != nil {
			return 0, err
		}
		states, err := tx.Query(ctx, `SELECT t.owner,t.status,
		 CASE WHEN EXISTS(SELECT 1 FROM identity.deletion_legal_holds h
		                   WHERE h.request_id=t.request_id AND h.released_at IS NULL)
		        THEN 'BLOCKED_HOLD'
		      WHEN p.owner IS NULL THEN 'WAITING_POLICY'
		      WHEN clock_timestamp()<d.effective_at+p.retention_interval THEN 'WAITING_RETENTION'
		      WHEN r.outcome IN ('ERASED','NO_DATA') AND r.policy_version=p.policy_version
		           AND r.attested_at>=d.effective_at AND r.attested_at<=clock_timestamp()
		           THEN 'ACKNOWLEDGED'
		      ELSE 'READY' END AS desired
		 FROM identity.deletion_owner_tasks t
		 JOIN identity.account_deletion_requests d ON d.id=t.request_id AND d.status='SCHEDULED'
		 JOIN identity.identities i ON i.id=d.identity_id
			 LEFT JOIN identity.deletion_country_policies p ON p.tenant_id=i.tenant_id AND p.country=d.country AND p.owner=t.owner
		 LEFT JOIN identity.deletion_owner_receipts r ON r.request_id=t.request_id AND r.owner=t.owner
		 WHERE t.request_id=$1 ORDER BY t.owner FOR UPDATE OF t`, requestID)
		if err != nil {
			return 0, err
		}
		type taskState struct{ owner, current, desired string }
		tasks := []taskState{}
		for states.Next() {
			var task taskState
			if err = states.Scan(&task.owner, &task.current, &task.desired); err != nil {
				states.Close()
				return 0, err
			}
			tasks = append(tasks, task)
		}
		err = states.Err()
		states.Close()
		if err != nil {
			return 0, err
		}
		if len(tasks) == 0 {
			return 0, fmt.Errorf("deletion owner registry produced no tasks")
		}
		for _, task := range tasks {
			if task.current == task.desired {
				continue
			}
			if _, err = tx.Exec(ctx, `UPDATE identity.deletion_owner_tasks
			 SET status=$3,revision=revision+1,updated_at=clock_timestamp()
			 WHERE request_id=$1 AND owner=$2`, requestID, task.owner, task.desired); err != nil {
				return 0, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.deletion_owner_tasks
		 SET checked_at=clock_timestamp() WHERE request_id=$1`, requestID); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(requests), nil
}
