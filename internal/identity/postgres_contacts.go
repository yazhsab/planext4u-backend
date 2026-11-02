package identity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) ContactReady(ctx context.Context) error {
	var ready bool
	err := repository.pool.QueryRow(ctx, `SELECT to_regclass('identity.contact_snapshots') IS NOT NULL AND to_regclass('identity.contact_hashes') IS NOT NULL AND to_regprocedure('configuration.contact_matching_policy(uuid,character)') IS NOT NULL`).Scan(&ready)
	if err != nil || !ready {
		return fmt.Errorf("contact matching schema is unavailable")
	}
	return nil
}

func contactPolicy(ctx context.Context, tx pgx.Tx, trusted TrustedIdentity) (string, error) {
	// Configuration publication takes the exclusive counterpart for every
	// published policy, so a policy cannot change before this transaction commits.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, "delivery-policy:"+trusted.TenantID+":"+trusted.Country); err != nil {
		return "", err
	}
	var policy *string
	if err := tx.QueryRow(ctx, `SELECT configuration.contact_matching_policy($1::uuid,$2::char(2))`, trusted.TenantID, trusted.Country).Scan(&policy); err != nil {
		return "", err
	}
	if policy == nil || !validSafeIdentifier(*policy, 64) {
		return "", ErrContactConsent
	}
	return *policy, nil
}

func lockContactConsent(ctx context.Context, tx pgx.Tx, ids []string) error {
	sort.Strings(ids)
	previous := ""
	for _, id := range ids {
		if id == previous {
			continue
		}
		previous = id
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, "consent:"+id); err != nil {
			return err
		}
	}
	return nil
}

func contactAllowed(ctx context.Context, tx pgx.Tx, id, country, policy string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT granted AND policy_version=$3 FROM identity.consent_evidence WHERE identity_id=$1 AND country=$2 AND purpose='CONTACT_MATCHING' ORDER BY version DESC LIMIT 1),false)`, id, country, policy).Scan(&allowed)
	return allowed, err
}

// All contact operations lock consent before accounts, matching withdrawal and
// deactivation. Session authority is rechecked after any external proof lookup.
func lockContactOwner(ctx context.Context, tx pgx.Tx, trusted TrustedIdentity, now time.Time) (string, error) {
	var subject, provider string
	var disabled *time.Time
	err := tx.QueryRow(ctx, `SELECT provider_subject,provider,disabled_at FROM identity.identities WHERE id=$1 AND tenant_id=$2 FOR SHARE`, trusted.Subject, trusted.TenantID).Scan(&subject, &provider, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if disabled != nil || provider != "firebase" {
		return "", ErrForbidden
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.account_restrictions WHERE identity_id=$1 AND source<>'ACTIVE')`, trusted.Subject).Scan(&allowed)
	if err != nil {
		return "", err
	}
	if allowed {
		return "", ErrForbidden
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM identity.sessions WHERE id=$1 AND identity_id=$2 AND tenant_id=$3 AND country=$4 AND revoked_at IS NULL AND refresh_expires_at>$5 FOR SHARE`, trusted.Session, trusted.Subject, trusted.TenantID, trusted.Country, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSessionInvalid
	}
	if err != nil {
		return "", err
	}
	return subject, nil
}

func contactStateTx(ctx context.Context, tx pgx.Tx, trusted TrustedIdentity, settings contactMatchingSettings, policy string, now time.Time) (ContactMatchingState, error) {
	result := ContactMatchingState{Status: "EMPTY", PolicyVersion: policy, MaxContacts: settings.maxContacts}
	var keyID, storedPolicy *string
	err := tx.QueryRow(ctx, `SELECT revision,contact_count,key_id,policy_version,expires_at FROM identity.contact_snapshots WHERE identity_id=$1 AND country=$2`, trusted.Subject, trusted.Country).Scan(&result.Revision, &result.ContactCount, &keyID, &storedPolicy, &result.ExpiresAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ContactMatchingState{}, err
	}
	allowed, err := contactAllowed(ctx, tx, trusted.Subject, trusted.Country, policy)
	if err != nil {
		return ContactMatchingState{}, err
	}
	if !allowed {
		result.Status = "CONSENT_REQUIRED"
		result.ContactCount = 0
		result.ExpiresAt = nil
		return result, nil
	}
	if keyID == nil {
		return result, nil
	}
	if *keyID != settings.keyID || storedPolicy == nil || *storedPolicy != policy || result.ExpiresAt == nil || !now.Before(*result.ExpiresAt) {
		result.Status = "SYNC_REQUIRED"
		result.ContactCount = 0
		result.ExpiresAt = nil
		return result, nil
	}
	result.Status = "READY"
	return result, nil
}

func (repository *PostgresRepository) ContactState(ctx context.Context, trusted TrustedIdentity, settings contactMatchingSettings, now time.Time) (ContactMatchingState, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return ContactMatchingState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	policy, err := contactPolicy(ctx, tx, trusted)
	if errors.Is(err, ErrContactConsent) {
		return ContactMatchingState{Status: "CONSENT_REQUIRED", MaxContacts: settings.maxContacts}, nil
	}
	if err != nil {
		return ContactMatchingState{}, err
	}
	if err = lockContactConsent(ctx, tx, []string{trusted.Subject}); err != nil {
		return ContactMatchingState{}, err
	}
	if _, err = lockContactOwner(ctx, tx, trusted, now); err != nil {
		return ContactMatchingState{}, err
	}
	result, err := contactStateTx(ctx, tx, trusted, settings, policy, now)
	if err != nil {
		return ContactMatchingState{}, err
	}
	return result, tx.Commit(ctx)
}

func contactGraphLock(ctx context.Context, tx pgx.Tx, trusted TrustedIdentity, shared bool) error {
	statement := `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`
	if shared {
		statement = `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`
	}
	_, err := tx.Exec(ctx, statement, "contact-graph:"+trusted.TenantID+":"+trusted.Country)
	return err
}

func (repository *PostgresRepository) PutContactSnapshot(ctx context.Context, trusted TrustedIdentity, proof ProviderAccount, input contactSnapshot, settings contactMatchingSettings, now time.Time) (ContactMatchingState, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return ContactMatchingState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	policy, err := contactPolicy(ctx, tx, trusted)
	if err != nil {
		return ContactMatchingState{}, err
	}
	if err = contactGraphLock(ctx, tx, trusted, false); err != nil {
		return ContactMatchingState{}, err
	}
	if err = lockContactConsent(ctx, tx, []string{trusted.Subject}); err != nil {
		return ContactMatchingState{}, err
	}
	uid, err := lockContactOwner(ctx, tx, trusted, now)
	if err != nil {
		return ContactMatchingState{}, err
	}
	if uid != proof.Subject {
		return ContactMatchingState{}, ErrForbidden
	}
	allowed, err := contactAllowed(ctx, tx, trusted.Subject, trusted.Country, policy)
	if err != nil {
		return ContactMatchingState{}, err
	}
	if !allowed || input.policyVersion != policy {
		return ContactMatchingState{}, ErrContactConsent
	}
	var revision int64
	var requestHash *string
	err = tx.QueryRow(ctx, `SELECT revision,request_hash FROM identity.contact_snapshots WHERE identity_id=$1 AND country=$2 FOR UPDATE`, trusted.Subject, trusted.Country).Scan(&revision, &requestHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ContactMatchingState{}, err
	}
	if revision != input.expectedRevision {
		if requestHash != nil && *requestHash == input.requestHash {
			state, readErr := contactStateTx(ctx, tx, trusted, settings, policy, now)
			if readErr != nil {
				return ContactMatchingState{}, readErr
			}
			return state, tx.Commit(ctx)
		}
		return ContactMatchingState{}, ErrContactConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM identity.contact_hashes WHERE identity_id=$1 AND country=$2`, trusted.Subject, trusted.Country); err != nil {
		return ContactMatchingState{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.contact_snapshots(identity_id,country,revision,key_id,phone_hash,policy_version,request_hash,contact_count,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(identity_id,country) DO UPDATE SET revision=excluded.revision,key_id=excluded.key_id,phone_hash=excluded.phone_hash,policy_version=excluded.policy_version,request_hash=excluded.request_hash,contact_count=excluded.contact_count,expires_at=excluded.expires_at`, trusted.Subject, trusted.Country, revision+1, input.keyID, input.phoneHash, input.policyVersion, input.requestHash, len(input.contactHashes), input.expiresAt)
	if err != nil {
		return ContactMatchingState{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.contact_hashes(identity_id,country,phone_hash) SELECT $1,$2,unnest($3::text[])`, trusted.Subject, trusted.Country, input.contactHashes); err != nil {
		return ContactMatchingState{}, err
	}
	if err = insertSecurityEvent(ctx, tx, "CONTACT_SNAPSHOT_SYNCED", trusted.Subject, trusted.Session, "", "CONTACT_MATCHING", "SUCCEEDED", now); err != nil {
		return ContactMatchingState{}, err
	}
	state, err := contactStateTx(ctx, tx, trusted, settings, policy, now)
	if err != nil {
		return ContactMatchingState{}, err
	}
	return state, tx.Commit(ctx)
}

type contactCandidate struct{ id, uid, name, phoneHash string }

func (repository *PostgresRepository) ContactMatches(ctx context.Context, trusted TrustedIdentity, settings contactMatchingSettings, revision int64, cursor string, limit int, directory ContactPhoneDirectory, now time.Time) (ContactMatches, error) {
	result := ContactMatches{SnapshotRevision: revision, Items: []ContactMatch{}}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	policy, err := contactPolicy(ctx, tx, trusted)
	if err != nil {
		return result, err
	}
	if err = contactGraphLock(ctx, tx, trusted, true); err != nil {
		return result, err
	}
	// Read a bounded candidate page. Eligibility is rechecked after taking the
	// same consent locks as withdrawal, before any provider lookup or disclosure.
	rows, err := tx.Query(ctx, `SELECT i.id,i.provider_subject,p.display_name,s.phone_hash FROM identity.contact_snapshots s JOIN identity.identities i ON i.id=s.identity_id JOIN identity.profiles p ON p.identity_id=i.id JOIN identity.contact_snapshots own ON own.identity_id=$1 AND own.country=s.country WHERE i.tenant_id=$2 AND i.provider='firebase' AND i.disabled_at IS NULL AND s.country=$3 AND s.identity_id<>$1 AND s.identity_id>$4 AND s.key_id=$5 AND own.key_id=$5 AND s.policy_version=$6 AND own.policy_version=$6 AND s.expires_at>$7 AND own.expires_at>$7 AND own.revision=$8 AND EXISTS(SELECT 1 FROM identity.contact_hashes h WHERE h.identity_id=$1 AND h.country=$3 AND h.phone_hash=s.phone_hash) AND EXISTS(SELECT 1 FROM identity.contact_hashes h WHERE h.identity_id=s.identity_id AND h.country=$3 AND h.phone_hash=own.phone_hash) ORDER BY i.id LIMIT $9`, trusted.Subject, trusted.TenantID, trusted.Country, cursor, settings.keyID, policy, now, revision, limit+1)
	if err != nil {
		return result, err
	}
	candidates := []contactCandidate{}
	for rows.Next() {
		var c contactCandidate
		if err = rows.Scan(&c.id, &c.uid, &c.name, &c.phoneHash); err != nil {
			rows.Close()
			return result, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(candidates) > limit {
		result.NextCursor = candidates[limit-1].id
		candidates = candidates[:limit]
	}
	ids := []string{trusted.Subject}
	for _, c := range candidates {
		ids = append(ids, c.id)
	}
	if err = lockContactConsent(ctx, tx, ids); err != nil {
		return result, err
	}
	// Shared account locks prevent disablement/rebinding through commit.
	locked, err := tx.Query(ctx, `SELECT id FROM identity.identities WHERE id=ANY($1::text[]) ORDER BY id FOR SHARE`, ids)
	if err != nil {
		return result, err
	}
	for locked.Next() {
		var id string
		if err = locked.Scan(&id); err != nil {
			locked.Close()
			return result, err
		}
	}
	err = locked.Err()
	locked.Close()
	if err != nil {
		return result, err
	}
	uid, err := lockContactOwner(ctx, tx, trusted, now)
	if err != nil {
		return result, err
	}
	state, err := contactStateTx(ctx, tx, trusted, settings, policy, now)
	if err != nil {
		return result, err
	}
	if state.Status == "CONSENT_REQUIRED" {
		return result, ErrContactConsent
	}
	if state.Status != "READY" || state.Revision != revision {
		return result, ErrContactConflict
	}
	var selfHash string
	if err = tx.QueryRow(ctx, `SELECT phone_hash FROM identity.contact_snapshots WHERE identity_id=$1 AND country=$2`, trusted.Subject, trusted.Country).Scan(&selfHash); err != nil {
		return result, err
	}
	eligible := []contactCandidate{}
	uids := []string{uid}
	for _, c := range candidates {
		allowed, e := contactAllowed(ctx, tx, c.id, trusted.Country, policy)
		if e != nil {
			return result, e
		}
		if !allowed {
			continue
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.identities i JOIN identity.contact_snapshots s ON s.identity_id=i.id WHERE i.id=$1 AND i.disabled_at IS NULL AND s.country=$2 AND s.phone_hash=$3 AND s.expires_at>$4 AND NOT EXISTS(SELECT 1 FROM identity.account_restrictions r WHERE r.identity_id=i.id AND r.source<>'ACTIVE'))`, c.id, trusted.Country, c.phoneHash, now).Scan(&active); e != nil {
			return result, e
		}
		if active {
			eligible = append(eligible, c)
			uids = append(uids, c.uid)
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	phones, err := directory.ContactPhones(bounded, uids)
	if err != nil {
		return result, ErrProviderUnavailable
	}
	if !contactPhonePattern.MatchString(phones[uid]) || settings.phoneHash(trusted.TenantID, phones[uid]) != selfHash {
		return result, ErrContactConflict
	}
	for _, c := range eligible {
		phone := phones[c.uid]
		if contactPhonePattern.MatchString(phone) && settings.phoneHash(trusted.TenantID, phone) == c.phoneHash {
			result.Items = append(result.Items, ContactMatch{IdentityID: c.id, DisplayName: c.name})
		}
	}
	if err = insertSecurityEvent(ctx, tx, "CONTACT_MATCHES_READ", trusted.Subject, trusted.Session, "", "CONTACT_MATCHING", "SUCCEEDED", now); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

// Expired graph material is erased in bounded batches; revision tombstones and
// append-only security events remain. A failed audit rolls the erasure back.
func (repository *PostgresRepository) ExpireContacts(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, ErrInvalidInput
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock one tenant/country graph before touching its rows. Readers hold the
	// shared counterpart through provider verification and response commit.
	var tenant, country string
	err = tx.QueryRow(ctx, `SELECT i.tenant_id,s.country FROM identity.contact_snapshots s JOIN identity.identities i ON i.id=s.identity_id WHERE s.expires_at<=$1 ORDER BY s.expires_at,s.identity_id LIMIT 1`, now).Scan(&tenant, &country)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err = contactGraphLock(ctx, tx, TrustedIdentity{TenantID: tenant, Country: country}, false); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT s.identity_id,s.country FROM identity.contact_snapshots s JOIN identity.identities i ON i.id=s.identity_id WHERE s.expires_at<=$1 AND i.tenant_id=$2 AND s.country=$3 ORDER BY s.expires_at,s.identity_id LIMIT $4 FOR UPDATE OF s SKIP LOCKED`, now, tenant, country, limit)
	if err != nil {
		return 0, err
	}
	type expired struct{ id, country string }
	items := []expired{}
	for rows.Next() {
		var item expired
		if err = rows.Scan(&item.id, &item.country); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if _, err = tx.Exec(ctx, `SELECT identity.clear_contact_snapshot($1,$2::char(2))`, item.id, item.country); err != nil {
			return 0, err
		}
		if err = insertSecurityEvent(ctx, tx, "CONTACT_SNAPSHOT_EXPIRED", item.id, "", "", "CONTACT_MATCHING", "SUCCEEDED", now); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit(ctx)
}
