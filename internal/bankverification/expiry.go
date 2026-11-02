package bankverification

import (
	"context"
	"time"
)

// Expire removes transient provider references and masked proof once the hosted
// attempt expires. It never changes a bank binding or a financial record owned
// by supply/fulfillment. The owning audit remains append-only. Bounded row
// leases allow multiple recovery workers without blocking an active attempt.
func (s *PostgresService) Expire(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `WITH leased AS (
 SELECT id FROM bankverification.link_attempts
 WHERE expires_at<=clock_timestamp() AND (status<>'EXPIRED' OR account IS NOT NULL OR provider_session_id IS NOT NULL OR verification_id IS NOT NULL OR verified_at IS NOT NULL)
 ORDER BY expires_at,id LIMIT $1 FOR UPDATE SKIP LOCKED
) UPDATE bankverification.link_attempts a SET status='EXPIRED',account=NULL,provider_session_id=NULL,verification_id=NULL,verified_at=NULL,updated_at=clock_timestamp(),revision=revision+1
 FROM leased WHERE a.id=leased.id RETURNING a.id::text,a.tenant_id::text,a.country,a.owner_id::text`, limit)
	if err != nil {
		return 0, err
	}
	type expired struct {
		actor Actor
		id    string
	}
	var values []expired
	for rows.Next() {
		var value expired
		if err = rows.Scan(&value.id, &value.actor.TenantID, &value.actor.Country, &value.actor.OwnerID); err != nil {
			rows.Close()
			return 0, err
		}
		value.actor.SessionID = "SYSTEM_EXPIRY"
		values = append(values, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, value := range values {
		if err = audit(ctx, tx, value.actor, value.id, "EXPIRED", "EXPIRED", s.clock().UTC()); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(values), nil
}
