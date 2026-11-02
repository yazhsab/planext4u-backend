// Package workflow provides a committed write-ahead journal. Domain services
// own the payload and all business decisions; effects use stable idempotency keys.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("workflow request conflict")

type Store struct {
	Pool   *pgxpool.Pool
	Schema string
}
type Record struct {
	ID, TenantID, Country, CustomerID, Key, Fingerprint string
	Payload                                             json.RawMessage
	Done                                                bool
}
type Session struct {
	conn  *pgxpool.Conn
	table string
}

var poolSlots sync.Map

func (s Store) Locked(ctx context.Context, id string, run func(*Session) error) error {
	if s.Pool == nil || (s.Schema != "commerce" && s.Schema != "food") {
		return errors.New("invalid workflow store")
	}
	return WithCapacity(ctx, s.Pool, func() error { return s.locked(ctx, id, run) })
}

// WithCapacity reserves connections for nested owner operations.
func WithCapacity(ctx context.Context, pool *pgxpool.Pool, run func() error) error {
	// A workflow holds a session lock while owners use the same pool. Reserve
	// at least half the connections for those owner transactions; otherwise
	// concurrent lock waiters can exhaust the pool and deadlock the lock holder.
	capacity := int(pool.Config().MaxConns) / 2
	if capacity < 1 {
		return errors.New("workflow journal needs at least two database connections")
	}
	stored, _ := poolSlots.LoadOrStore(pool, make(chan struct{}, capacity))
	slots := stored.(chan struct{})
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-slots }()
	return run()
}

func (s Store) locked(ctx context.Context, id string, run func(*Session) error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	lock := s.Schema + ":checkout-workflow:" + id
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lock); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Never return a session carrying an advisory lock to the pool.
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lock); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	return run(&Session{conn: conn, table: pgx.Identifier{s.Schema, "checkout_workflows"}.Sanitize()})
}

func (s *Session) Load(ctx context.Context, id string) (Record, bool, error) {
	var r Record
	err := s.conn.QueryRow(ctx, `SELECT id,tenant_id::text,country,customer_id::text,idempotency_key,fingerprint,payload,done FROM `+s.table+` WHERE id=$1`, id).Scan(&r.ID, &r.TenantID, &r.Country, &r.CustomerID, &r.Key, &r.Fingerprint, &r.Payload, &r.Done)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// Insert must finish before the first effect. The unique request key and ID
// claim both retries and alternative keys for the same source cart/quote.
func (s *Session) Insert(ctx context.Context, r Record) error {
	tag, err := s.conn.Exec(ctx, `INSERT INTO `+s.table+` (id,tenant_id,country,customer_id,idempotency_key,fingerprint,payload,done,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,false,now()) ON CONFLICT DO NOTHING`, r.ID, r.TenantID, r.Country, r.CustomerID, r.Key, r.Fingerprint, []byte(r.Payload))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Session) Save(ctx context.Context, r Record, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tag, err := s.conn.Exec(ctx, `UPDATE `+s.table+` SET payload=$2,done=$3,updated_at=now() WHERE id=$1`, r.ID, payload, r.Done)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("workflow disappeared: %s", r.ID)
	}
	return nil
}
func (s Store) Pending(ctx context.Context, limit int) ([]Record, error) {
	if s.Pool == nil || (s.Schema != "commerce" && s.Schema != "food") || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid workflow scan")
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,tenant_id::text,country,customer_id::text,idempotency_key,fingerprint,payload,done FROM `+pgx.Identifier{s.Schema, "checkout_workflows"}.Sanitize()+` WHERE done=false ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Country, &r.CustomerID, &r.Key, &r.Fingerprint, &r.Payload, &r.Done); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
