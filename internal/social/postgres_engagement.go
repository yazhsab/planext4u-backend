package social

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (service *PostgresService) CreateMedia(actor Actor, key string, request CreateMediaRequest) (MediaJob, bool, error) {
	kind := strings.ToUpper(strings.TrimSpace(request.Kind))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(request.AssetID) || !map[string]bool{"IMAGE": true, "VIDEO": true, "VOICE": true}[kind] {
		return MediaJob{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MediaJob{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := socialCommandLock(ctx, tx, actor, "social-media", key); err != nil {
		return MediaJob{}, false, err
	}
	fingerprint := digest(request)
	var replay MediaJob
	if found, err := loadSocialReplay(ctx, tx, actor, "social-media", key, fingerprint, &replay); err != nil {
		return MediaJob{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return MediaJob{}, false, err
	}
	policy, err := loadSocialPolicy(ctx, tx, actor)
	if err != nil {
		return MediaJob{}, false, err
	}
	now := service.Now()
	value := MediaJob{ID: uuid.NewString(), OwnerID: actor.Subject, AssetID: request.AssetID, Kind: kind, State: MediaQuarantined, ScanStatus: "PENDING", BlurStatus: "PENDING", TranscodeStatus: "PENDING", RetentionUntil: now.Add(policy.mediaRetention), CreatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	if kind == "VOICE" {
		value.BlurStatus = "NOT_APPLICABLE"
	}
	_, err = tx.Exec(ctx, `INSERT INTO social.media_jobs (id,tenant_id,country,owner_identity_id,media_asset_id,kind,state,scan_status,blur_status,transcode_status,retention_until,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,'QUARANTINED',$7,$8,$9,$10,$11,$11)`, value.ID, actor.TenantID, actor.Country, actor.Subject, value.AssetID, kind, value.ScanStatus, value.BlurStatus, value.TranscodeStatus, value.RetentionUntil, now)
	if err != nil {
		return MediaJob{}, false, mapSocialError(err)
	}
	if err := storeSocialReplay(ctx, tx, actor, "social-media", key, fingerprint, value, now); err != nil {
		return MediaJob{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MediaJob{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ProcessMedia(actor Actor, key, mediaID string, request ProcessMediaRequest) (MediaJob, bool, error) {
	if !actor.MFAVerified {
		return MediaJob{}, false, ErrMFARequired
	}
	if !postgresSocialActor(actor) || !validModerator(actor) || !validKey(key) || !socialUUID(mediaID) {
		return MediaJob{}, false, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MediaJob{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "process-media:" + mediaID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return MediaJob{}, false, err
	}
	fingerprint := digest(request)
	var replay MediaJob
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return MediaJob{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadSocialMedia(ctx, tx, actor, mediaID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaJob{}, false, ErrNotFound
	}
	if err != nil {
		return MediaJob{}, false, err
	}
	if value.State != MediaQuarantined {
		return MediaJob{}, false, ErrConflict
	}
	value.ModeratedBy = actor.Subject
	if request.Clean {
		value.State, value.ScanStatus, value.BlurStatus, value.TranscodeStatus = MediaReady, "CLEAN", "COMPLETE", "COMPLETE"
		if value.Kind == "VOICE" {
			value.BlurStatus = "NOT_APPLICABLE"
		}
	} else {
		value.State, value.ScanStatus, value.BlurStatus, value.TranscodeStatus = MediaRejected, "REJECTED", "NOT_RUN", "NOT_RUN"
	}
	_, err = tx.Exec(ctx, `UPDATE social.media_jobs SET state=$2,scan_status=$3,blur_status=$4,transcode_status=$5,moderated_by_identity_id=$6,updated_at=$7 WHERE id=$1`, mediaID, value.State, value.ScanStatus, value.BlurStatus, value.TranscodeStatus, actor.Subject, service.Now())
	if err != nil {
		return MediaJob{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return MediaJob{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MediaJob{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) AppealMedia(actor Actor, key, mediaID string) (MediaJob, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(mediaID) {
		return MediaJob{}, false, ErrInvalidRequest
	}
	return service.mutateMedia(actor, key, mediaID, "appeal-media", digest(mediaID), func(value *MediaJob) error {
		if value.OwnerID != actor.Subject {
			return ErrNotFound
		}
		if value.State != MediaRejected || value.AppealStatus != "" {
			return ErrConflict
		}
		value.AppealStatus = "PENDING"
		return nil
	})
}

func (service *PostgresService) DecideMediaAppeal(actor Actor, key, mediaID string, request AppealDecisionRequest) (MediaJob, bool, error) {
	if !actor.MFAVerified {
		return MediaJob{}, false, ErrMFARequired
	}
	note := strings.TrimSpace(request.Note)
	if !postgresSocialActor(actor) || !validModerator(actor) || !validKey(key) || !socialUUID(mediaID) || len([]rune(note)) < 8 || len([]rune(note)) > 500 {
		return MediaJob{}, false, ErrInvalidRequest
	}
	return service.mutateMedia(actor, key, mediaID, "decide-media-appeal", digest(request), func(value *MediaJob) error {
		if value.AppealStatus != "PENDING" || value.ModeratedBy == actor.Subject {
			return ErrForbidden
		}
		value.AppealStatus = "REJECTED"
		if request.Approve {
			value.AppealStatus, value.State, value.ScanStatus, value.BlurStatus, value.TranscodeStatus = "APPROVED", MediaReady, "CLEAN", "COMPLETE", "COMPLETE"
			if value.Kind == "VOICE" {
				value.BlurStatus = "NOT_APPLICABLE"
			}
		}
		return nil
	})
}

func (service *PostgresService) mutateMedia(actor Actor, key, mediaID, operation, fingerprint string, mutation func(*MediaJob) error) (MediaJob, bool, error) {
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MediaJob{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope := operation + ":" + mediaID
	if err := socialCommandLock(ctx, tx, actor, scope, key); err != nil {
		return MediaJob{}, false, err
	}
	var replay MediaJob
	if found, err := loadSocialReplay(ctx, tx, actor, scope, key, fingerprint, &replay); err != nil {
		return MediaJob{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadSocialMedia(ctx, tx, actor, mediaID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaJob{}, false, ErrNotFound
	}
	if err != nil {
		return MediaJob{}, false, err
	}
	if err := mutation(&value); err != nil {
		return MediaJob{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE social.media_jobs SET state=$2,scan_status=$3,blur_status=$4,transcode_status=$5,appeal_status=NULLIF($6,''),updated_at=$7 WHERE id=$1`, mediaID, value.State, value.ScanStatus, value.BlurStatus, value.TranscodeStatus, value.AppealStatus, service.Now())
	if err != nil {
		return MediaJob{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, scope, key, fingerprint, value, service.Now()); err != nil {
		return MediaJob{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MediaJob{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) CreateEphemeral(actor Actor, key string, request CreateEphemeralRequest) (EphemeralContent, bool, error) {
	kind, caption := strings.ToUpper(strings.TrimSpace(request.Kind)), strings.TrimSpace(request.Caption)
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(request.MediaJobID) || !map[string]bool{"STORY": true, "REEL": true}[kind] || len([]rune(caption)) > 1000 {
		return EphemeralContent{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EphemeralContent{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := socialCommandLock(ctx, tx, actor, "ephemeral", key); err != nil {
		return EphemeralContent{}, false, err
	}
	fingerprint := digest(request)
	var replay EphemeralContent
	if found, err := loadSocialReplay(ctx, tx, actor, "ephemeral", key, fingerprint, &replay); err != nil {
		return EphemeralContent{}, false, err
	} else if found {
		return replay, true, nil
	}
	media, err := loadSocialMedia(ctx, tx, actor, request.MediaJobID, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (media.OwnerID != actor.Subject || media.State != MediaReady) {
		return EphemeralContent{}, false, ErrForbidden
	}
	if err != nil {
		return EphemeralContent{}, false, err
	}
	policy, err := loadSocialPolicy(ctx, tx, actor)
	if err != nil {
		return EphemeralContent{}, false, err
	}
	ttl := policy.storyTTL
	if kind == "REEL" {
		ttl = policy.reelTTL
	}
	id, now := uuid.NewString(), service.Now()
	_, err = tx.Exec(ctx, `INSERT INTO social.ephemeral_content (id,tenant_id,country,author_identity_id,media_job_id,kind,caption,status,highlighted,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'PUBLISHED',false,$8,$9)`, id, actor.TenantID, actor.Country, actor.Subject, request.MediaJobID, kind, caption, now.Add(ttl), now)
	if err != nil {
		return EphemeralContent{}, false, mapSocialError(err)
	}
	value, err := loadSocialEphemeral(ctx, tx, actor, id)
	if err != nil {
		return EphemeralContent{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, "ephemeral", key, fingerprint, value, now); err != nil {
		return EphemeralContent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EphemeralContent{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Ephemeral(actor Actor) ([]EphemeralContent, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) {
		return nil, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	_, _ = service.pool.Exec(ctx, `UPDATE social.ephemeral_content SET status='EXPIRED' WHERE tenant_id=$1 AND country=$2 AND status='PUBLISHED' AND highlighted=false AND expires_at<=$3`, actor.TenantID, actor.Country, service.Now())
	rows, err := service.pool.Query(ctx, socialEphemeralSelect+` WHERE e.tenant_id=$1 AND e.country=$2 AND e.status='PUBLISHED' AND (e.highlighted OR e.expires_at>$3) AND NOT EXISTS(SELECT 1 FROM social.relationship_controls r WHERE r.tenant_id=e.tenant_id AND r.country=e.country AND r.control='BLOCK' AND ((r.actor_identity_id=$4 AND r.target_identity_id=e.author_identity_id) OR (r.actor_identity_id=e.author_identity_id AND r.target_identity_id=$4))) AND (e.author_identity_id=$4 OR NOT p.private OR EXISTS(SELECT 1 FROM social.follows f WHERE f.tenant_id=e.tenant_id AND f.country=e.country AND f.follower_identity_id=$4 AND f.following_identity_id=e.author_identity_id AND f.status='ACCEPTED')) ORDER BY e.created_at DESC,e.id`, actor.TenantID, actor.Country, service.Now(), actor.Subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []EphemeralContent{}
	for rows.Next() {
		value, scanErr := scanSocialEphemeral(rows, actor.Subject)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) SetHighlight(actor Actor, key, contentID string, active bool) (EphemeralContent, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(contentID) {
		return EphemeralContent{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EphemeralContent{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "highlight:" + contentID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return EphemeralContent{}, false, err
	}
	fingerprint := digest(active)
	var replay EphemeralContent
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return EphemeralContent{}, false, err
	} else if found {
		return replay, true, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE social.ephemeral_content SET highlighted=$2 WHERE id=$1 AND tenant_id=$3 AND country=$4 AND author_identity_id=$5 AND status='PUBLISHED'`, contentID, active, actor.TenantID, actor.Country, actor.Subject)
	if err != nil {
		return EphemeralContent{}, false, err
	}
	if tag.RowsAffected() != 1 {
		return EphemeralContent{}, false, ErrNotFound
	}
	value, err := loadSocialEphemeral(ctx, tx, actor, contentID)
	if err != nil {
		return EphemeralContent{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return EphemeralContent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EphemeralContent{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) PurgeExpired(actor Actor) (int, error) {
	if !postgresSocialActor(actor) || !validModerator(actor) {
		return 0, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `UPDATE social.ephemeral_content SET status='TOMBSTONED' WHERE tenant_id=$1 AND country=$2 AND highlighted=false AND expires_at<=$3 AND status<>'TOMBSTONED' RETURNING media_job_id`, actor.TenantID, actor.Country, service.Now())
	if err != nil {
		return 0, err
	}
	mediaIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		mediaIDs = append(mediaIDs, id)
	}
	rows.Close()
	if len(mediaIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE social.media_jobs SET state='TOMBSTONED',updated_at=$2 WHERE id=ANY($1::uuid[])`, mediaIDs, service.Now()); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(mediaIDs), nil
}

func (service *PostgresService) CreateCollection(actor Actor, key, name string) (Collection, bool, error) {
	name = strings.TrimSpace(name)
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || len([]rune(name)) < 2 || len([]rune(name)) > 80 {
		return Collection{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Collection{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := socialCommandLock(ctx, tx, actor, "collection", key); err != nil {
		return Collection{}, false, err
	}
	fingerprint := digest(name)
	var replay Collection
	if found, err := loadSocialReplay(ctx, tx, actor, "collection", key, fingerprint, &replay); err != nil {
		return Collection{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Collection{}, false, err
	}
	id, now := uuid.NewString(), service.Now()
	if _, err := tx.Exec(ctx, `INSERT INTO social.collections (id,tenant_id,country,owner_identity_id,name,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$6)`, id, actor.TenantID, actor.Country, actor.Subject, name, now); err != nil {
		return Collection{}, false, mapSocialError(err)
	}
	value := Collection{ID: id, Name: name, PostIDs: []string{}, CreatedAt: now, UpdatedAt: now, ownerID: actor.Subject, tenantID: actor.TenantID, country: actor.Country}
	if err := storeSocialReplay(ctx, tx, actor, "collection", key, fingerprint, value, now); err != nil {
		return Collection{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Collection{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) SetCollectionPost(actor Actor, key, collectionID, postID string, active bool) (Collection, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(collectionID) || !socialUUID(postID) {
		return Collection{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Collection{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "collection-post:" + collectionID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Collection{}, false, err
	}
	fingerprint := digest(CollectionPostRequest{PostID: postID, Active: active})
	var replay Collection
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Collection{}, false, err
	} else if found {
		return replay, true, nil
	}
	var ownerID string
	err = tx.QueryRow(ctx, `SELECT owner_identity_id::text FROM social.collections WHERE id=$1 AND tenant_id=$2 AND country=$3 FOR UPDATE`, collectionID, actor.TenantID, actor.Country).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && ownerID != actor.Subject {
		return Collection{}, false, ErrNotFound
	}
	if err != nil {
		return Collection{}, false, err
	}
	if _, err := loadSocialPost(ctx, tx, actor, postID); errors.Is(err, pgx.ErrNoRows) {
		return Collection{}, false, ErrNotFound
	} else if err != nil {
		return Collection{}, false, err
	}
	if active {
		_, err = tx.Exec(ctx, `INSERT INTO social.collection_posts (collection_id,post_id,created_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, collectionID, postID, service.Now())
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM social.collection_posts WHERE collection_id=$1 AND post_id=$2`, collectionID, postID)
	}
	if err != nil {
		return Collection{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE social.collections SET updated_at=$2 WHERE id=$1`, collectionID, service.Now()); err != nil {
		return Collection{}, false, err
	}
	value, err := loadSocialCollection(ctx, tx, actor, collectionID, false)
	if err != nil {
		return Collection{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Collection{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Collection{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) OpenConversation(actor Actor, key, profileID string) (Conversation, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(profileID) || profileID == actor.Subject {
		return Conversation{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Conversation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "conversation:" + profileID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Conversation{}, false, err
	}
	fingerprint := digest(profileID)
	var replay Conversation
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Conversation{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Conversation{}, false, err
	}
	if _, err := loadSocialProfile(ctx, tx, actor, profileID); errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, false, ErrNotFound
	} else if err != nil {
		return Conversation{}, false, err
	}
	value, err := findSocialConversation(ctx, tx, actor, profileID)
	if err == nil {
		if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
			return Conversation{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Conversation{}, false, mapSocialError(err)
		}
		return value, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, false, err
	}
	var mutual bool
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM social.follows WHERE tenant_id=$1 AND country=$2 AND status='ACCEPTED' AND ((follower_identity_id=$3 AND following_identity_id=$4) OR (follower_identity_id=$4 AND following_identity_id=$3)))=2`, actor.TenantID, actor.Country, actor.Subject, profileID).Scan(&mutual); err != nil {
		return Conversation{}, false, err
	}
	status := "REQUESTED"
	if mutual {
		status = "ACCEPTED"
	}
	id, now := uuid.NewString(), service.Now()
	_, err = tx.Exec(ctx, `INSERT INTO social.conversations (id,tenant_id,country,participant_ids,requested_by_identity_id,status,created_at,updated_at) VALUES ($1,$2,$3,ARRAY[$4::uuid,$5::uuid],$4,$6,$7,$7)`, id, actor.TenantID, actor.Country, actor.Subject, profileID, status, now)
	if err != nil {
		return Conversation{}, false, mapSocialError(err)
	}
	value, err = loadSocialConversation(ctx, tx, actor, id, false)
	if err != nil {
		return Conversation{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Conversation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Conversation{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Conversations(actor Actor) ([]Conversation, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) {
		return nil, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	rows, err := service.pool.Query(ctx, socialConversationSelect+` WHERE tenant_id=$1 AND country=$2 AND participant_ids @> ARRAY[$3::uuid] ORDER BY updated_at DESC,id`, actor.TenantID, actor.Country, actor.Subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Conversation{}
	for rows.Next() {
		value, scanErr := scanSocialConversation(rows, actor.Subject)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) AcceptConversation(actor Actor, key, conversationID string) (Conversation, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(conversationID) {
		return Conversation{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Conversation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "accept-conversation:" + conversationID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Conversation{}, false, err
	}
	fingerprint := digest(conversationID)
	var replay Conversation
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Conversation{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadSocialConversation(ctx, tx, actor, conversationID, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (value.Status != "REQUESTED" || value.RequestedBy == actor.Subject) {
		return Conversation{}, false, ErrNotFound
	}
	if err != nil {
		return Conversation{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE social.conversations SET status='ACCEPTED',updated_at=$2 WHERE id=$1`, conversationID, service.Now()); err != nil {
		return Conversation{}, false, err
	}
	value, err = loadSocialConversation(ctx, tx, actor, conversationID, false)
	if err != nil {
		return Conversation{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Conversation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Conversation{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) SendMessage(actor Actor, key, conversationID string, request SendMessageRequest) (DirectMessage, bool, error) {
	body := strings.TrimSpace(request.Body)
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(conversationID) || len([]rune(body)) > 4000 || body == "" && request.VoiceMediaID == "" || request.VoiceMediaID != "" && !socialUUID(request.VoiceMediaID) {
		return DirectMessage{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DirectMessage{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "message:" + conversationID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return DirectMessage{}, false, err
	}
	fingerprint := digest(request)
	var replay DirectMessage
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return DirectMessage{}, false, err
	} else if found {
		return replay, true, nil
	}
	conversation, err := loadSocialConversation(ctx, tx, actor, conversationID, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && conversation.Status != "ACCEPTED" {
		return DirectMessage{}, false, ErrForbidden
	}
	if err != nil {
		return DirectMessage{}, false, err
	}
	if request.VoiceMediaID != "" {
		media, err := loadSocialMedia(ctx, tx, actor, request.VoiceMediaID, false)
		if err != nil || media.OwnerID != actor.Subject || media.Kind != "VOICE" || media.State != MediaReady {
			return DirectMessage{}, false, ErrForbidden
		}
	}
	id, now := uuid.NewString(), service.Now()
	_, err = tx.Exec(ctx, `INSERT INTO social.direct_messages (id,tenant_id,country,conversation_id,sender_identity_id,body,voice_media_job_id,status,created_at) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,'DELIVERED',$8)`, id, actor.TenantID, actor.Country, conversationID, actor.Subject, body, request.VoiceMediaID, now)
	if err != nil {
		return DirectMessage{}, false, mapSocialError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE social.conversations SET updated_at=$2 WHERE id=$1`, conversationID, now); err != nil {
		return DirectMessage{}, false, err
	}
	value := DirectMessage{ID: id, ConversationID: conversationID, SenderID: actor.Subject, Body: body, VoiceMediaID: request.VoiceMediaID, Status: "DELIVERED", CreatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return DirectMessage{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DirectMessage{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Messages(actor Actor, conversationID string) ([]DirectMessage, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(conversationID) {
		return nil, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	conversation, err := loadSocialConversation(ctx, service.pool, actor, conversationID, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && conversation.Status != "ACCEPTED" {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text,conversation_id::text,sender_identity_id::text,body,COALESCE(voice_media_job_id::text,''),status,created_at,tenant_id::text,country FROM social.direct_messages WHERE tenant_id=$1 AND country=$2 AND conversation_id=$3 ORDER BY created_at,id`, actor.TenantID, actor.Country, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []DirectMessage{}
	for rows.Next() {
		var value DirectMessage
		if err := rows.Scan(&value.ID, &value.ConversationID, &value.SenderID, &value.Body, &value.VoiceMediaID, &value.Status, &value.CreatedAt, &value.tenantID, &value.country); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) SetPresence(actor Actor, request PresenceRequest) (Presence, error) {
	state := strings.ToUpper(strings.TrimSpace(request.State))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !map[string]bool{"ONLINE": true, "AWAY": true, "OFFLINE": true}[state] {
		return Presence{}, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Presence{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Presence{}, err
	}
	policy, err := loadSocialPolicy(ctx, tx, actor)
	if err != nil {
		return Presence{}, err
	}
	ttl := policy.presenceTTL
	if state == "OFFLINE" {
		ttl = time.Second
	}
	value := Presence{ProfileID: actor.Subject, State: state, ExpiresAt: service.Now().Add(ttl)}
	_, err = tx.Exec(ctx, `INSERT INTO social.presence (tenant_id,country,profile_identity_id,state,expires_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,country,profile_identity_id) DO UPDATE SET state=excluded.state,expires_at=excluded.expires_at,updated_at=excluded.updated_at`, actor.TenantID, actor.Country, actor.Subject, state, value.ExpiresAt, service.Now())
	if err != nil {
		return Presence{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Presence{}, err
	}
	return value, nil
}

func (service *PostgresService) Presence(actor Actor, profileID string) (Presence, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(profileID) {
		return Presence{}, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	if _, err := loadSocialProfile(ctx, service.pool, actor, profileID); errors.Is(err, pgx.ErrNoRows) {
		return Presence{}, ErrForbidden
	} else if err != nil {
		return Presence{}, err
	}
	var value Presence
	err := service.pool.QueryRow(ctx, `SELECT profile_identity_id::text,state,expires_at FROM social.presence WHERE tenant_id=$1 AND country=$2 AND profile_identity_id=$3 AND expires_at>$4`, actor.TenantID, actor.Country, profileID, service.Now()).Scan(&value.ProfileID, &value.State, &value.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Presence{ProfileID: profileID, State: "OFFLINE", ExpiresAt: service.Now()}, nil
	}
	return value, err
}

func (service *PostgresService) CreateCall(actor Actor, key, conversationID, kind string) (CallSession, bool, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(conversationID) || !map[string]bool{"AUDIO": true, "VIDEO": true}[kind] {
		return CallSession{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CallSession{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "call:" + conversationID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return CallSession{}, false, err
	}
	fingerprint := digest(kind)
	var replay CallSession
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return CallSession{}, false, err
	} else if found {
		return replay, true, nil
	}
	conversation, err := loadSocialConversation(ctx, tx, actor, conversationID, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && conversation.Status != "ACCEPTED" {
		return CallSession{}, false, ErrForbidden
	}
	if err != nil {
		return CallSession{}, false, err
	}
	policy, err := loadSocialPolicy(ctx, tx, actor)
	if err != nil {
		return CallSession{}, false, err
	}
	id, now := uuid.NewString(), service.Now()
	value := CallSession{ID: id, ConversationID: conversationID, InitiatorID: actor.Subject, Kind: kind, Status: "RINGING", ExpiresAt: now.Add(policy.callTTL), CreatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO social.call_sessions (id,tenant_id,country,conversation_id,initiator_identity_id,kind,status,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,'RINGING',$7,$8)`, id, actor.TenantID, actor.Country, conversationID, actor.Subject, kind, value.ExpiresAt, now)
	if err != nil {
		return CallSession{}, false, mapSocialError(err)
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return CallSession{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CallSession{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) SignalCall(actor Actor, callID string, request SignalRequest) (CallSession, error) {
	typeName, payload := strings.ToUpper(strings.TrimSpace(request.Type)), strings.TrimSpace(request.Payload)
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(callID) || !map[string]bool{"OFFER": true, "ANSWER": true, "ICE": true, "END": true}[typeName] || len(payload) > 16*1024 || strings.ContainsAny(payload, "\r\n") {
		return CallSession{}, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CallSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, err := loadSocialCall(ctx, tx, actor, callID, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!service.Now().Before(value.ExpiresAt) || value.Status == "ENDED") {
		return CallSession{}, ErrForbidden
	}
	if err != nil {
		return CallSession{}, err
	}
	sum := sha256.Sum256([]byte(payload))
	if _, err := tx.Exec(ctx, `INSERT INTO social.call_signals (tenant_id,country,call_id,sender_identity_id,signal_type,payload_digest,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, actor.TenantID, actor.Country, callID, actor.Subject, typeName, hex.EncodeToString(sum[:]), service.Now()); err != nil {
		return CallSession{}, err
	}
	status := value.Status
	if typeName == "ANSWER" {
		status = "CONNECTED"
	} else if typeName == "END" {
		status = "ENDED"
	}
	if err := tx.QueryRow(ctx, `UPDATE social.call_sessions SET signal_count=signal_count+1,status=$2 WHERE id=$1 RETURNING signal_count,status`, callID, status).Scan(&value.SignalCount, &value.Status); err != nil {
		return CallSession{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CallSession{}, mapSocialError(err)
	}
	return value, nil
}

const socialMediaSelect = `SELECT id::text,owner_identity_id::text,media_asset_id::text,kind,state,scan_status,blur_status,transcode_status,COALESCE(moderated_by_identity_id::text,''),COALESCE(appeal_status,''),retention_until,created_at,tenant_id::text,country FROM social.media_jobs`

func loadSocialMedia(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string, lock bool) (MediaJob, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var value MediaJob
	err := querier.QueryRow(ctx, socialMediaSelect+` WHERE id=$1 AND tenant_id=$2 AND country=$3`+suffix, id, actor.TenantID, actor.Country).Scan(&value.ID, &value.OwnerID, &value.AssetID, &value.Kind, &value.State, &value.ScanStatus, &value.BlurStatus, &value.TranscodeStatus, &value.ModeratedBy, &value.AppealStatus, &value.RetentionUntil, &value.CreatedAt, &value.tenantID, &value.country)
	return value, err
}

const socialEphemeralSelect = `SELECT e.id::text,e.kind,e.media_job_id::text,e.caption,e.status,e.highlighted,e.expires_at,e.created_at,p.identity_id::text,p.handle,p.display_name,p.bio,COALESCE(p.avatar_asset_id::text,''),p.private,p.verified,p.follower_count,p.following_count,e.tenant_id::text,e.country FROM social.ephemeral_content e JOIN social.profiles p ON p.identity_id=e.author_identity_id`

func loadSocialEphemeral(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string) (EphemeralContent, error) {
	return scanSocialEphemeral(querier.QueryRow(ctx, socialEphemeralSelect+` WHERE e.id=$1 AND e.tenant_id=$2 AND e.country=$3`, id, actor.TenantID, actor.Country), actor.Subject)
}

func scanSocialEphemeral(row interface{ Scan(...any) error }, viewer string) (EphemeralContent, error) {
	var value EphemeralContent
	err := row.Scan(&value.ID, &value.Kind, &value.MediaJobID, &value.Caption, &value.Status, &value.Highlighted, &value.ExpiresAt, &value.CreatedAt, &value.Author.ID, &value.Author.Handle, &value.Author.DisplayName, &value.Author.Bio, &value.Author.AvatarAssetID, &value.Author.Private, &value.Author.Verified, &value.Author.FollowerCount, &value.Author.FollowingCount, &value.tenantID, &value.country)
	if err != nil {
		return EphemeralContent{}, err
	}
	value.Author.tenantID, value.Author.country = value.tenantID, value.country
	value.AllowedActions = []string{"REPORT"}
	if value.Author.ID == viewer {
		value.AllowedActions = []string{"HIGHLIGHT", "DELETE"}
	}
	return value, nil
}

func loadSocialCollection(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string, lock bool) (Collection, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF c"
	}
	var value Collection
	err := querier.QueryRow(ctx, `SELECT c.id::text,c.name,COALESCE(array_agg(cp.post_id::text ORDER BY cp.created_at) FILTER (WHERE cp.post_id IS NOT NULL),'{}'),c.created_at,c.updated_at,c.owner_identity_id::text,c.tenant_id::text,c.country FROM social.collections c LEFT JOIN social.collection_posts cp ON cp.collection_id=c.id WHERE c.id=$1 AND c.tenant_id=$2 AND c.country=$3 GROUP BY c.id`+suffix, id, actor.TenantID, actor.Country).Scan(&value.ID, &value.Name, &value.PostIDs, &value.CreatedAt, &value.UpdatedAt, &value.ownerID, &value.tenantID, &value.country)
	return value, err
}

const socialConversationSelect = `SELECT id::text,participant_ids::text[],status,requested_by_identity_id::text,created_at,updated_at,tenant_id::text,country FROM social.conversations`

func loadSocialConversation(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string, lock bool) (Conversation, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanSocialConversation(querier.QueryRow(ctx, socialConversationSelect+` WHERE id=$1 AND tenant_id=$2 AND country=$3 AND participant_ids @> ARRAY[$4::uuid]`+suffix, id, actor.TenantID, actor.Country, actor.Subject), actor.Subject)
}

func findSocialConversation(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, other string) (Conversation, error) {
	return scanSocialConversation(querier.QueryRow(ctx, socialConversationSelect+` WHERE tenant_id=$1 AND country=$2 AND participant_ids @> ARRAY[$3::uuid,$4::uuid]`, actor.TenantID, actor.Country, actor.Subject, other), actor.Subject)
}

func scanSocialConversation(row interface{ Scan(...any) error }, viewer string) (Conversation, error) {
	var value Conversation
	if err := row.Scan(&value.ID, &value.ParticipantIDs, &value.Status, &value.RequestedBy, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country); err != nil {
		return Conversation{}, err
	}
	return presentConversation(viewer, value), nil
}

const socialCallSelect = `SELECT c.id::text,c.conversation_id::text,c.initiator_identity_id::text,c.kind,c.status,c.signal_count,c.expires_at,c.created_at,c.tenant_id::text,c.country FROM social.call_sessions c JOIN social.conversations v ON v.id=c.conversation_id`

func loadSocialCall(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string, lock bool) (CallSession, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF c"
	}
	var value CallSession
	err := querier.QueryRow(ctx, socialCallSelect+` WHERE c.id=$1 AND c.tenant_id=$2 AND c.country=$3 AND v.participant_ids @> ARRAY[$4::uuid]`+suffix, id, actor.TenantID, actor.Country, actor.Subject).Scan(&value.ID, &value.ConversationID, &value.InitiatorID, &value.Kind, &value.Status, &value.SignalCount, &value.ExpiresAt, &value.CreatedAt, &value.tenantID, &value.country)
	return value, err
}
