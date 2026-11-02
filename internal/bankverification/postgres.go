package bankverification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbidden       = errors.New("bank link session is not authorized")
	ErrNotFound        = errors.New("bank link not found")
	ErrInvalidRequest  = errors.New("bank link request is invalid")
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{16,128}$`)
)

type Actor struct {
	Scope
	SessionID string
}

type LinkView struct {
	AttemptID       string    `json:"attempt_id"`
	ApplicationRole string    `json:"application_role"`
	Status          string    `json:"status"`
	ExpiresAt       time.Time `json:"expires_at"`
	LaunchURL       string    `json:"launch_url,omitempty"`
	Account         *Account  `json:"account,omitempty"`
}

type PostgresService struct {
	pool   *pgxpool.Pool
	linker Linker
	clock  func() time.Time
}

func NewPostgresService(pool *pgxpool.Pool, linker Linker, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || linker == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, linker: linker, clock: clock}, nil
}

func (s *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('bankverification.link_attempts') IS NOT NULL AND to_regclass('bankverification.audit_events') IS NOT NULL AND to_regprocedure('identity.lock_bank_link_session(text,character,text,text,boolean)') IS NOT NULL`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return ErrUnavailable
	}
	return nil
}

func validActor(actor Actor, role string) bool {
	return canonicalBankUUID(actor.TenantID) && canonicalBankUUID(actor.OwnerID) && canonicalBankUUID(actor.SessionID) && countryPattern.MatchString(actor.Country) && (role == "VENDOR" || role == "RIDER")
}

// authorize keeps source account/session shared locks through the owning
// transaction. A role header, user-supplied owner, or cached JWT cannot replace
// this live check. SQL uses the database clock for freshness/expiry decisions.
func authorize(ctx context.Context, tx pgx.Tx, actor Actor) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT identity.lock_bank_link_session($1,$2,$3,$4,true)`, actor.TenantID, actor.Country, actor.OwnerID, actor.SessionID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func ownerLock(ctx context.Context, tx pgx.Tx, actor Actor, role string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "bank-link:"+actor.TenantID+":"+actor.Country+":"+actor.OwnerID+":"+role)
	return err
}

// Start commits the idempotent attempt before any provider side effect. The
// second transaction serializes bounded provider calls on that attempt. If a
// response/commit is lost, the same durable attempt reaches provider Create
// again with the same idempotency key; no new account/transfer is inferred.
func (s *PostgresService) Start(ctx context.Context, actor Actor, role, key string) (LinkView, error) {
	if !validActor(actor, role) {
		return LinkView{}, ErrForbidden
	}
	if !idempotencyPattern.MatchString(key) {
		return LinkView{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	id, err := s.prepare(ctx, actor, role, key)
	if err != nil {
		return LinkView{}, err
	}
	return s.exchange(ctx, actor, role, id, false)
}

func (s *PostgresService) prepare(ctx context.Context, actor Actor, role, key string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = authorize(ctx, tx, actor); err != nil {
		return "", err
	}
	if err = ownerLock(ctx, tx, actor, role); err != nil {
		return "", err
	}
	now := s.clock().UTC()
	// Expiry is a durable state change, with its mandatory audit in this commit.
	rows, err := tx.Query(ctx, `UPDATE bankverification.link_attempts SET status='EXPIRED',account=NULL,verification_id=NULL,verified_at=NULL,updated_at=$5,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND owner_id=$3 AND application_role=$4 AND status IN ('CREATING','AWAITING_USER','VERIFYING','VERIFIED') AND expires_at<=$5 RETURNING id::text`, actor.TenantID, actor.Country, actor.OwnerID, role, now)
	if err != nil {
		return "", err
	}
	var expired []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		expired = append(expired, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, id := range expired {
		if err = audit(ctx, tx, actor, id, "EXPIRED", "EXPIRED", now); err != nil {
			return "", err
		}
	}
	digest := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(digest[:])
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM bankverification.link_attempts WHERE tenant_id=$1 AND country=$2 AND owner_id=$3 AND application_role=$4 AND (idempotency_hash=$5 OR status IN ('CREATING','AWAITING_USER','VERIFYING','VERIFIED')) ORDER BY (idempotency_hash=$5) DESC LIMIT 1`, actor.TenantID, actor.Country, actor.OwnerID, role, hash).Scan(&id)
	action := "RESUMED"
	if errors.Is(err, pgx.ErrNoRows) {
		id, action = uuid.NewString(), "CREATED"
		_, err = tx.Exec(ctx, `INSERT INTO bankverification.link_attempts(id,tenant_id,country,owner_id,application_role,idempotency_hash,status,created_at,updated_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,'CREATING',$7,$7,$8)`, id, actor.TenantID, actor.Country, actor.OwnerID, role, hash, now, now.Add(30*time.Minute))
	}
	if err != nil {
		return "", err
	}
	if err = audit(ctx, tx, actor, id, action, "ATTEMPT", now); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *PostgresService) Current(ctx context.Context, actor Actor, role string) (LinkView, error) {
	if !validActor(actor, role) {
		return LinkView{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LinkView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = authorize(ctx, tx, actor); err != nil {
		return LinkView{}, err
	}
	var view LinkView
	var account []byte
	err = tx.QueryRow(ctx, `SELECT id::text,application_role,status,expires_at,account FROM bankverification.link_attempts WHERE tenant_id=$1 AND country=$2 AND owner_id=$3 AND application_role=$4 ORDER BY created_at DESC,id DESC LIMIT 1`, actor.TenantID, actor.Country, actor.OwnerID, role).Scan(&view.AttemptID, &view.ApplicationRole, &view.Status, &view.ExpiresAt, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkView{}, ErrNotFound
	}
	if err != nil {
		return LinkView{}, err
	}
	if !view.ExpiresAt.After(s.clock()) {
		view.Status = "EXPIRED"
	} else if len(account) > 0 {
		if err = json.Unmarshal(account, &view.Account); err != nil {
			return LinkView{}, ErrUnavailable
		}
	}
	return view, tx.Commit(ctx)
}

func (s *PostgresService) Resume(ctx context.Context, actor Actor, role, id string) (LinkView, error) {
	return s.command(ctx, actor, role, id, false)
}
func (s *PostgresService) Cancel(ctx context.Context, actor Actor, role, id string) (LinkView, error) {
	return s.command(ctx, actor, role, id, true)
}
func (s *PostgresService) command(ctx context.Context, actor Actor, role, id string, cancelLink bool) (LinkView, error) {
	if !validActor(actor, role) {
		return LinkView{}, ErrForbidden
	}
	if !canonicalBankUUID(id) {
		return LinkView{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return s.exchange(ctx, actor, role, id, cancelLink)
}

func (s *PostgresService) exchange(ctx context.Context, actor Actor, role, id string, cancelLink bool) (LinkView, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return LinkView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = authorize(ctx, tx, actor); err != nil {
		return LinkView{}, err
	}
	var view LinkView
	var providerSession *string
	var account []byte
	err = tx.QueryRow(ctx, `SELECT id::text,application_role,status,expires_at,provider_session_id,account FROM bankverification.link_attempts WHERE id=$1 AND tenant_id=$2 AND country=$3 AND owner_id=$4 AND application_role=$5 FOR UPDATE`, id, actor.TenantID, actor.Country, actor.OwnerID, role).Scan(&view.AttemptID, &view.ApplicationRole, &view.Status, &view.ExpiresAt, &providerSession, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkView{}, ErrNotFound
	}
	if err != nil {
		return LinkView{}, err
	}
	now := s.clock().UTC()
	action := "RESOLVED"
	var result LinkResult
	if !view.ExpiresAt.After(now) {
		view.Status, action = "EXPIRED", "EXPIRED"
	} else if view.Status == "CANCELLED" || view.Status == "DECLINED" || view.Status == "EXPIRED" || view.Status == "VERIFIED" {
		if len(account) > 0 {
			if err = json.Unmarshal(account, &view.Account); err != nil {
				return LinkView{}, ErrUnavailable
			}
		}
		return view, tx.Commit(ctx)
	} else {
		input := LinkRequest{Scope: actor.Scope, ApplicationRole: role, AttemptID: id}
		if providerSession != nil {
			input.ProviderSession = *providerSession
		}
		if cancelLink {
			// The provider resolves an interrupted attempt by its stable ID.
			// Cancelling must never initiate a new paid verification.
			result, err = s.linker.Cancel(ctx, input)
		} else if providerSession == nil {
			result, err = s.linker.Create(ctx, input)
		} else {
			result, err = s.linker.Resolve(ctx, input)
		}
		if err != nil {
			return LinkView{}, err
		}
		view.Status, view.LaunchURL, view.Account = result.Status, result.LaunchURL, result.Account
		if result.ExpiresAt.Before(view.ExpiresAt) {
			view.ExpiresAt = result.ExpiresAt
		}
		// A retained masked result must not outlive the independent resolver's
		// freshness window. Start can then create a fresh journey after expiry.
		if result.Status == "VERIFIED" && result.VerifiedAt.Add(15*time.Minute).Before(view.ExpiresAt) {
			view.ExpiresAt = result.VerifiedAt.Add(15 * time.Minute)
		}
		if cancelLink {
			action = "CANCELLED"
		}
	}
	var masked []byte
	var verificationID *string
	var verifiedAt *time.Time
	if view.Account != nil {
		masked, _ = json.Marshal(view.Account)
		verificationID, verifiedAt = &result.VerificationID, &result.VerifiedAt
	}
	if result.ProviderSession != "" {
		providerSession = &result.ProviderSession
	}
	// Locks prevent revocation/deactivation from committing meanwhile; the
	// second check also catches wall-clock freshness/expiry crossed in network I/O.
	if err = authorize(ctx, tx, actor); err != nil {
		return LinkView{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE bankverification.link_attempts SET status=$2,provider_session_id=$3,account=$4,verification_id=$5,verified_at=$6,updated_at=$7,expires_at=$8,revision=revision+1 WHERE id=$1`, id, view.Status, providerSession, masked, verificationID, verifiedAt, now, view.ExpiresAt)
	if err != nil {
		return LinkView{}, err
	}
	if err = audit(ctx, tx, actor, id, action, view.Status, now); err != nil {
		return LinkView{}, err
	}
	return view, tx.Commit(ctx)
}

func audit(ctx context.Context, tx pgx.Tx, actor Actor, id, action, status string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO bankverification.audit_events(tenant_id,country,owner_id,session_id,attempt_id,action,status,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.OwnerID, actor.SessionID, id, action, status, now)
	return err
}
