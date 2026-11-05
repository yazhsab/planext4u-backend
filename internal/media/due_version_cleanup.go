package media

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DueVersionCleaner is a cleanup-only boundary used by the explicit one-asset
// command. Its database pool must connect as the dedicated cleanup login and
// assume the NOLOGIN planext4u_media_cleanup role with SET ROLE. Its S3 client
// must use a separate version-cleanup identity. Provisioning remains outside
// the ordinary media runtime.
type DueVersionCleaner struct {
	pool   *pgxpool.Pool
	purger exactKeyVersionPurger
}

type exactKeyVersionPurger interface {
	purgeExactKey(context.Context, string) (versionPurgeSummary, error)
}

type DueVersionPurgeResult struct {
	AlreadyPurged   bool
	VersionsDeleted int
	MarkersDeleted  int
}

func NewDueVersionCleaner(pool *pgxpool.Pool, client *s3.Client, bucket string) (*DueVersionCleaner, error) {
	if pool == nil || client == nil {
		return nil, ErrInvalidRequest
	}
	purger, err := newVersionPurger(client, bucket)
	if err != nil {
		return nil, err
	}
	return &DueVersionCleaner{pool: pool, purger: purger}, nil
}

// PurgeDueAsset holds a single READ COMMITTED transaction from the identity
// due-request/policy/hold lock through the media purge-state and asset locks,
// cross-service reference checks, exact-key S3 zero-version proof and durable
// evidence mark. A failed S3 operation or DB commit leaves work retryable. It
// never issues a terminal account-erasure receipt.
func (cleaner *DueVersionCleaner) PurgeDueAsset(ctx context.Context, requestID, assetID string) (DueVersionPurgeResult, error) {
	var result DueVersionPurgeResult
	if cleaner == nil || cleaner.pool == nil || cleaner.purger == nil || !safeID(requestID) || uuid.Validate(assetID) != nil {
		return result, ErrInvalidRequest
	}
	tx, err := cleaner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, fmt.Errorf("begin due media version purge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// SET ROLE alone is insufficient: a migration administrator could assume
	// the cleanup role. Require the separate session login on every attempt,
	// including idempotent readback, before any owner or S3 operation.
	var login, role string
	var canLogin, superuser, createRole, createDB, replication, bypassRLS bool
	var safeCleanupRole bool
	var otherMemberships int
	if err := tx.QueryRow(ctx, `SELECT session_user,current_user,r.rolcanlogin,r.rolsuper,
	 r.rolcreaterole,r.rolcreatedb,r.rolreplication,r.rolbypassrls,
	 (SELECT count(*) FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid
	  WHERE (m.member=r.oid AND granted.rolname<>'planext4u_media_cleanup')
	     OR m.member=(SELECT oid FROM pg_roles WHERE rolname='planext4u_media_cleanup')),
	 EXISTS (SELECT 1 FROM pg_roles c WHERE c.rolname='planext4u_media_cleanup'
	  AND NOT c.rolcanlogin AND NOT c.rolsuper AND NOT c.rolcreaterole
	  AND NOT c.rolcreatedb AND NOT c.rolreplication AND NOT c.rolbypassrls)
	 FROM pg_roles r WHERE r.rolname=session_user`).Scan(&login, &role, &canLogin,
		&superuser, &createRole, &createDB, &replication, &bypassRLS, &otherMemberships, &safeCleanupRole); err != nil {
		return result, fmt.Errorf("check media cleanup identity: %w", err)
	}
	if login != "planext4u_media_cleanup_login" || role != "planext4u_media_cleanup" ||
		!canLogin || superuser || createRole || createDB || replication || bypassRLS || otherMemberships != 0 || !safeCleanupRole {
		return result, fmt.Errorf("media cleanup requires a separate database role: %w", ErrDependency)
	}
	var key, policy string
	var alreadyPurged bool
	if err := tx.QueryRow(ctx, `SELECT object_key,policy_version,already_purged
		FROM media.prepare_due_version_purge($1,$2)`, requestID, assetID).Scan(&key, &policy, &alreadyPurged); err != nil {
		return result, fmt.Errorf("prepare due media version purge: %w", err)
	}
	if !validObjectKey(key) || policy == "" {
		return result, fmt.Errorf("due media version purge returned invalid scope: %w", ErrDependency)
	}
	if alreadyPurged {
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit completed media version purge: %w", err)
		}
		result.AlreadyPurged = true
		return result, nil
	}
	summary, err := cleaner.purger.purgeExactKey(ctx, key)
	if err != nil {
		return result, fmt.Errorf("purge due media object versions: %w", err)
	}
	// purgeExactKey returns successfully only after a complete exact-key
	// listing found zero versions and zero delete markers. Do not put an
	// unverified or partially successful count into the database.
	if summary.versions < 0 || summary.deleteMarkers < 0 ||
		summary.versions > versionPurgeMaxEntries || summary.deleteMarkers > versionPurgeMaxEntries {
		return result, fmt.Errorf("media version purge summary is invalid: %w", ErrDependency)
	}
	var marked bool
	if err := tx.QueryRow(ctx, `SELECT media.finish_due_version_purge($1,$2,$3,$4,$5,$6)`,
		requestID, assetID, key, policy, summary.versions, summary.deleteMarkers).Scan(&marked); err != nil {
		return result, fmt.Errorf("record due media version purge: %w", err)
	}
	if !marked {
		return result, fmt.Errorf("media version purge mark was refused: %w", ErrDependency)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit due media version purge: %w", err)
	}
	result.VersionsDeleted, result.MarkersDeleted = summary.versions, summary.deleteMarkers
	return result, nil
}

var _ exactKeyVersionPurger = (*versionPurger)(nil)
