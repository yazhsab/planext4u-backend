package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

const socialOperationTimeout = 20 * time.Second

type EngagementRewarder interface {
	RewardEngagement(wallet.Scope, string, string, string, int64, time.Time) (wallet.LedgerEntry, bool, error)
}

type socialPolicy struct {
	version, rankingModel       string
	reviewTerms                 []string
	storyTTL, reelTTL           time.Duration
	mediaRetention, presenceTTL time.Duration
	callTTL, rewardExpiry       time.Duration
	likePoints, followPoints    int64
	sharePoints                 int64
}

type PostgresService struct {
	pool     *pgxpool.Pool
	clock    func() time.Time
	rewarder EngagementRewarder
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time, rewarder EngagementRewarder) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock, rewarder: rewarder}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	err := service.pool.QueryRow(ctx, `SELECT to_regclass('social.profiles') IS NOT NULL AND to_regclass('social.posts') IS NOT NULL AND to_regclass('social.policies') IS NOT NULL AND to_regclass('social.presence') IS NOT NULL AND to_regclass('social.reward_events') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check social schema readiness: %w", err)
	}
	if !ready {
		return errors.New("social schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Feed(actor Actor, cursor string, limit int) (FeedPage, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) {
		return FeedPage{}, ErrForbidden
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return FeedPage{}, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	policy, err := loadSocialPolicy(ctx, service.pool, actor)
	if err != nil {
		return FeedPage{}, err
	}
	offset, err := decodePostgresCursor(cursor, policy.rankingModel)
	if err != nil {
		return FeedPage{}, err
	}
	rows, err := service.pool.Query(ctx, socialPostSelect+socialVisibleClause+` ORDER BY (p.like_count*10+p.comment_count*15+CASE WHEN p.sponsored THEN 5 ELSE 0 END) DESC,p.created_at DESC,p.id LIMIT $4 OFFSET $5`, actor.TenantID, actor.Country, actor.Subject, limit, offset)
	if err != nil {
		return FeedPage{}, fmt.Errorf("query social feed: %w", err)
	}
	defer rows.Close()
	items := []Post{}
	for rows.Next() {
		value, scanErr := scanSocialPost(rows)
		if scanErr != nil {
			return FeedPage{}, scanErr
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return FeedPage{}, fmt.Errorf("iterate social feed: %w", err)
	}
	page := FeedPage{Items: items, RankingVersion: policy.rankingModel}
	if len(items) == limit {
		page.NextCursor = encodePostgresCursor(policy.rankingModel, offset+limit)
	}
	return page, nil
}

func (service *PostgresService) Profile(actor Actor, profileID string) (Profile, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(profileID) {
		return Profile{}, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	value, err := loadSocialProfile(ctx, service.pool, actor, profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return value, err
}

func (service *PostgresService) ProfileByHandle(actor Actor, handle string) (Profile, error) {
	handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !handlePattern.MatchString(handle) {
		return Profile{}, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	var profileID string
	err := service.pool.QueryRow(ctx, `SELECT identity_id::text FROM social.profiles WHERE tenant_id=$1 AND country=$2 AND lower(handle)=$3`, actor.TenantID, actor.Country, handle).Scan(&profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	value, err := loadSocialProfile(ctx, service.pool, actor, profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return value, err
}

func (service *PostgresService) ProfileContent(actor Actor, profileID, kind string) ([]any, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(profileID) || !map[string]bool{"POSTS": true, "REELS": true, "TAGGED": true, "SAVED": true}[kind] {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	profile, err := loadSocialProfile(ctx, service.pool, actor, profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if profile.Private && profile.ID != actor.Subject && profile.Relationship != "ACCEPTED" || kind == "SAVED" && profile.ID != actor.Subject {
		return nil, ErrNotFound
	}
	if kind == "REELS" {
		values, err := service.Ephemeral(actor)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(values))
		for _, value := range values {
			if value.Author.ID == profileID && value.Kind == "REEL" {
				items = append(items, value)
			}
		}
		return items, nil
	}
	query := socialPostSelect + socialVisibleClause
	arguments := []any{actor.TenantID, actor.Country, actor.Subject}
	switch kind {
	case "POSTS":
		query += ` AND p.author_identity_id=$4`
		arguments = append(arguments, profileID)
	case "TAGGED":
		query += ` AND ($4=ANY(p.mentions) OR $5=ANY(p.mentions))`
		arguments = append(arguments, profileID, profile.Handle)
	case "SAVED":
		query += ` AND EXISTS(SELECT 1 FROM social.post_engagement saved WHERE saved.tenant_id=p.tenant_id AND saved.country=p.country AND saved.post_id=p.id AND saved.actor_identity_id=$3 AND saved.kind='SAVE')`
	}
	query += ` ORDER BY p.created_at DESC,p.id`
	rows, err := service.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		value, scanErr := scanSocialPost(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

func (service *PostgresService) Post(actor Actor, postID string) (Post, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(postID) {
		return Post{}, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	value, err := loadSocialPost(ctx, service.pool, actor, postID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Post{}, ErrNotFound
	}
	return value, err
}

func (service *PostgresService) CreatePost(actor Actor, key string, request CreatePostRequest) (Post, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !validPostRequest(request) || !allSocialUUIDs(request.MediaAssetIDs) || request.ProductStickerID != "" && !socialUUID(request.ProductStickerID) {
		return Post{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Post{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := socialCommandLock(ctx, tx, actor, "create-post", key); err != nil {
		return Post{}, false, err
	}
	fingerprint := digest(request)
	var replay Post
	if found, err := loadSocialReplay(ctx, tx, actor, "create-post", key, fingerprint, &replay); err != nil {
		return Post{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Post{}, false, err
	}
	policy, err := loadSocialPolicy(ctx, tx, actor)
	if err != nil {
		return Post{}, false, err
	}
	now, status, reason := service.Now(), PostPublished, ""
	body := strings.TrimSpace(request.Body)
	for _, term := range policy.reviewTerms {
		if cleaned := strings.TrimSpace(term); cleaned != "" && strings.Contains(strings.ToLower(body), strings.ToLower(cleaned)) {
			status, reason = PostPendingReview, "AUTOMATED_POLICY_REVIEW"
			break
		}
	}
	id := uuid.NewString()
	mediaAssetIDs := append([]string{}, request.MediaAssetIDs...)
	_, err = tx.Exec(ctx, `INSERT INTO social.posts (id,tenant_id,country,author_identity_id,revision,body,media_asset_ids,hashtags,mentions,product_sticker_id,status,moderation_reason,ranking_version,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,NULLIF($9,'')::uuid,$10,NULLIF($11,''),$12,$13,$13)`, id, actor.TenantID, actor.Country, actor.Subject, body, mediaAssetIDs, extract(body, hashtagPattern), extract(body, mentionPattern), request.ProductStickerID, status, reason, policy.rankingModel, now)
	if err != nil {
		return Post{}, false, mapSocialError(err)
	}
	value, err := loadSocialPost(ctx, tx, actor, id)
	if err != nil {
		return Post{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, "create-post", key, fingerprint, value, now); err != nil {
		return Post{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Post{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) SetLike(actor Actor, key, postID string, revision int64, active bool) (Post, bool, error) {
	return service.setPostEngagement(actor, key, postID, revision, "LIKE", active)
}

func (service *PostgresService) SetSave(actor Actor, key, postID string, revision int64, active bool) (Post, bool, error) {
	return service.setPostEngagement(actor, key, postID, revision, "SAVE", active)
}

func (service *PostgresService) SharePost(actor Actor, key, postID string, request ShareRequest) (Post, bool, error) {
	channel := strings.ToUpper(strings.TrimSpace(request.Channel))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(postID) || !validShareChannel(channel) {
		return Post{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Post{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "share:" + postID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Post{}, false, err
	}
	normalized := ShareRequest{Channel: channel}
	fingerprint := digest(normalized)
	var replay Post
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Post{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Post{}, false, err
	}
	var lockedPostID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM social.posts WHERE id=$1 AND tenant_id=$2 AND country=$3 AND status='PUBLISHED' FOR UPDATE`, postID, actor.TenantID, actor.Country).Scan(&lockedPostID); errors.Is(err, pgx.ErrNoRows) {
		return Post{}, false, ErrNotFound
	} else if err != nil {
		return Post{}, false, err
	}
	if _, err := loadSocialPost(ctx, tx, actor, postID); errors.Is(err, pgx.ErrNoRows) {
		return Post{}, false, ErrNotFound
	} else if err != nil {
		return Post{}, false, err
	}
	now := service.Now()
	tag, err := tx.Exec(ctx, `INSERT INTO social.post_shares (id,tenant_id,country,post_id,actor_identity_id,channel,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (tenant_id,country,post_id,actor_identity_id,channel) DO NOTHING`, uuid.NewString(), actor.TenantID, actor.Country, postID, actor.Subject, channel, now)
	if err != nil {
		return Post{}, false, mapSocialError(err)
	}
	replayed := tag.RowsAffected() == 0
	if !replayed {
		if _, err := tx.Exec(ctx, `UPDATE social.posts SET revision=revision+1,share_count=share_count+1,updated_at=$2 WHERE id=$1`, postID, now); err != nil {
			return Post{}, false, err
		}
		policy, policyErr := loadSocialPolicy(ctx, tx, actor)
		if policyErr != nil {
			return Post{}, false, policyErr
		}
		if err := queueSocialReward(ctx, tx, actor, "social-share:"+postID+":"+actor.Subject+":"+channel, policy.sharePoints, policy.rewardExpiry, now); err != nil {
			return Post{}, false, err
		}
	}
	value, err := loadSocialPost(ctx, tx, actor, postID)
	if err != nil {
		return Post{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Post{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Post{}, false, mapSocialError(err)
	}
	return value, replayed, nil
}

func (service *PostgresService) setPostEngagement(actor Actor, key, postID string, revision int64, kind string, active bool) (Post, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(postID) || revision < 1 {
		return Post{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Post{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := strings.ToLower(kind) + ":" + postID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Post{}, false, err
	}
	fingerprint := digest(EngagementRequest{Active: active})
	var replay Post
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Post{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Post{}, false, err
	}
	var currentRevision int64
	var status string
	if err := tx.QueryRow(ctx, `SELECT revision,status FROM social.posts WHERE id=$1 AND tenant_id=$2 AND country=$3 FOR UPDATE`, postID, actor.TenantID, actor.Country).Scan(&currentRevision, &status); errors.Is(err, pgx.ErrNoRows) || status != string(PostPublished) {
		return Post{}, false, ErrNotFound
	} else if err != nil {
		return Post{}, false, err
	}
	if _, err := loadSocialPost(ctx, tx, actor, postID); errors.Is(err, pgx.ErrNoRows) {
		return Post{}, false, ErrNotFound
	} else if err != nil {
		return Post{}, false, err
	}
	if currentRevision != revision {
		return Post{}, false, ErrConflict
	}
	var changed bool
	if active {
		tag, execErr := tx.Exec(ctx, `INSERT INTO social.post_engagement (tenant_id,country,post_id,actor_identity_id,kind,created_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, actor.TenantID, actor.Country, postID, actor.Subject, kind, service.Now())
		if execErr != nil {
			return Post{}, false, mapSocialError(execErr)
		}
		changed = tag.RowsAffected() == 1
	} else {
		tag, execErr := tx.Exec(ctx, `DELETE FROM social.post_engagement WHERE tenant_id=$1 AND country=$2 AND post_id=$3 AND actor_identity_id=$4 AND kind=$5`, actor.TenantID, actor.Country, postID, actor.Subject, kind)
		if execErr != nil {
			return Post{}, false, execErr
		}
		changed = tag.RowsAffected() == 1
	}
	if changed {
		likeDelta := 0
		if kind == "LIKE" {
			if active {
				likeDelta = 1
			} else {
				likeDelta = -1
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE social.posts SET revision=revision+1,like_count=like_count+$2,updated_at=$3 WHERE id=$1`, postID, likeDelta, service.Now()); err != nil {
			return Post{}, false, err
		}
		if kind == "LIKE" && active {
			policy, policyErr := loadSocialPolicy(ctx, tx, actor)
			if policyErr != nil {
				return Post{}, false, policyErr
			}
			if err := queueSocialReward(ctx, tx, actor, "social-like:"+postID+":"+actor.Subject, policy.likePoints, policy.rewardExpiry, service.Now()); err != nil {
				return Post{}, false, err
			}
		}
	}
	value, err := loadSocialPost(ctx, tx, actor, postID)
	if err != nil {
		return Post{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Post{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Post{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Comments(actor Actor, postID string) ([]Comment, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !socialUUID(postID) {
		return nil, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	if _, err := loadSocialPost(ctx, service.pool, actor, postID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := service.pool.Query(ctx, socialCommentSelect+` WHERE c.tenant_id=$1 AND c.country=$2 AND c.post_id=$3 AND c.status='PUBLISHED' ORDER BY c.created_at,c.id`, actor.TenantID, actor.Country, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Comment{}
	for rows.Next() {
		value, scanErr := scanSocialComment(rows, actor.Subject)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) CreateComment(actor Actor, key, postID string, request CreateCommentRequest) (Comment, bool, error) {
	body := strings.TrimSpace(request.Body)
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(postID) || len([]rune(body)) < 1 || len([]rune(body)) > 1000 || request.ParentID != "" && !socialUUID(request.ParentID) {
		return Comment{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "comment:" + postID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Comment{}, false, err
	}
	fingerprint := digest(request)
	var replay Comment
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Comment{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Comment{}, false, err
	}
	if _, err := loadSocialPost(ctx, tx, actor, postID); errors.Is(err, pgx.ErrNoRows) {
		return Comment{}, false, ErrNotFound
	} else if err != nil {
		return Comment{}, false, err
	}
	depth := 0
	if request.ParentID != "" {
		if err := tx.QueryRow(ctx, `SELECT depth+1 FROM social.comments WHERE id=$1 AND post_id=$2 AND status='PUBLISHED' AND depth<2`, request.ParentID, postID).Scan(&depth); errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, false, ErrInvalidRequest
		} else if err != nil {
			return Comment{}, false, err
		}
	}
	id, now := uuid.NewString(), service.Now()
	_, err = tx.Exec(ctx, `INSERT INTO social.comments (id,tenant_id,country,post_id,parent_id,author_identity_id,depth,body,mentions,status,created_at) VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,$8,$9,'PUBLISHED',$10)`, id, actor.TenantID, actor.Country, postID, request.ParentID, actor.Subject, depth, body, extract(body, mentionPattern), now)
	if err != nil {
		return Comment{}, false, mapSocialError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE social.posts SET revision=revision+1,comment_count=comment_count+1,updated_at=$2 WHERE id=$1`, postID, now); err != nil {
		return Comment{}, false, err
	}
	value, err := loadSocialComment(ctx, tx, actor, id)
	if err != nil {
		return Comment{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Comment{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Follow(actor Actor, key, profileID string) (Follow, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(profileID) || profileID == actor.Subject {
		return Follow{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Follow{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "follow:" + profileID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Follow{}, false, err
	}
	fingerprint := digest(profileID)
	var replay Follow
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Follow{}, false, err
	} else if found {
		return replay, true, nil
	}
	if err := service.ensureProfile(ctx, tx, actor); err != nil {
		return Follow{}, false, err
	}
	var private, blocked bool
	err = tx.QueryRow(ctx, `SELECT p.private,EXISTS(SELECT 1 FROM social.relationship_controls r WHERE r.tenant_id=$1 AND r.country=$2 AND r.control='BLOCK' AND ((r.actor_identity_id=$3 AND r.target_identity_id=$4) OR (r.actor_identity_id=$4 AND r.target_identity_id=$3))) FROM social.profiles p WHERE p.identity_id=$4 AND p.tenant_id=$1 AND p.country=$2`, actor.TenantID, actor.Country, actor.Subject, profileID).Scan(&private, &blocked)
	if errors.Is(err, pgx.ErrNoRows) || blocked {
		return Follow{}, false, ErrNotFound
	}
	if err != nil {
		return Follow{}, false, err
	}
	value := Follow{}
	err = tx.QueryRow(ctx, `SELECT id::text,follower_identity_id::text,following_identity_id::text,status,created_at,updated_at FROM social.follows WHERE tenant_id=$1 AND country=$2 AND follower_identity_id=$3 AND following_identity_id=$4`, actor.TenantID, actor.Country, actor.Subject, profileID).Scan(&value.ID, &value.FollowerID, &value.FollowingID, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		value = Follow{ID: uuid.NewString(), FollowerID: actor.Subject, FollowingID: profileID, Status: "ACCEPTED", CreatedAt: service.Now(), UpdatedAt: service.Now()}
		if private {
			value.Status = "PENDING"
		}
		_, err = tx.Exec(ctx, `INSERT INTO social.follows (id,tenant_id,country,follower_identity_id,following_identity_id,status,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, value.ID, actor.TenantID, actor.Country, actor.Subject, profileID, value.Status, value.CreatedAt)
		if err != nil {
			return Follow{}, false, mapSocialError(err)
		}
		if value.Status == "ACCEPTED" {
			if err := updateFollowCounts(ctx, tx, actor.Subject, profileID, 1); err != nil {
				return Follow{}, false, err
			}
			policy, policyErr := loadSocialPolicy(ctx, tx, actor)
			if policyErr != nil {
				return Follow{}, false, policyErr
			}
			if err := queueSocialReward(ctx, tx, actor, "social-follow:"+profileID+":"+actor.Subject, policy.followPoints, policy.rewardExpiry, service.Now()); err != nil {
				return Follow{}, false, err
			}
		}
	} else if err != nil {
		return Follow{}, false, err
	}
	value.tenantID, value.country = actor.TenantID, actor.Country
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Follow{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Follow{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) AcceptFollow(actor Actor, key, followerID string) (Follow, bool, error) {
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(followerID) || followerID == actor.Subject {
		return Follow{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Follow{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "accept-follow:" + followerID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Follow{}, false, err
	}
	fingerprint := digest(followerID)
	var replay Follow
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Follow{}, false, err
	} else if found {
		return replay, true, nil
	}
	var value Follow
	err = tx.QueryRow(ctx, `SELECT id::text,follower_identity_id::text,following_identity_id::text,status,created_at,updated_at FROM social.follows WHERE tenant_id=$1 AND country=$2 AND follower_identity_id=$3 AND following_identity_id=$4 FOR UPDATE`, actor.TenantID, actor.Country, followerID, actor.Subject).Scan(&value.ID, &value.FollowerID, &value.FollowingID, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) || value.Status != "PENDING" {
		return Follow{}, false, ErrNotFound
	}
	if err != nil {
		return Follow{}, false, err
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM social.relationship_controls WHERE tenant_id=$1 AND country=$2 AND control='BLOCK' AND ((actor_identity_id=$3 AND target_identity_id=$4) OR (actor_identity_id=$4 AND target_identity_id=$3)))`, actor.TenantID, actor.Country, actor.Subject, followerID).Scan(&blocked); err != nil {
		return Follow{}, false, err
	}
	if blocked {
		return Follow{}, false, ErrNotFound
	}
	value.Status, value.UpdatedAt = "ACCEPTED", service.Now()
	if _, err := tx.Exec(ctx, `UPDATE social.follows SET status='ACCEPTED',updated_at=$2 WHERE id=$1`, value.ID, value.UpdatedAt); err != nil {
		return Follow{}, false, err
	}
	if err := updateFollowCounts(ctx, tx, followerID, actor.Subject, 1); err != nil {
		return Follow{}, false, err
	}
	policy, err := loadSocialPolicy(ctx, tx, actor)
	if err != nil {
		return Follow{}, false, err
	}
	followerActor := actor
	followerActor.Subject = followerID
	if err := queueSocialReward(ctx, tx, followerActor, "social-follow:"+actor.Subject+":"+followerID, policy.followPoints, policy.rewardExpiry, service.Now()); err != nil {
		return Follow{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Follow{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Follow{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) SetRelationship(actor Actor, key, profileID, action string) (Profile, bool, error) {
	action = strings.ToUpper(strings.TrimSpace(action))
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(profileID) || profileID == actor.Subject || action != "BLOCK" && action != "MUTE" && action != "NONE" {
		return Profile{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Profile{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "relationship:" + profileID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Profile{}, false, err
	}
	fingerprint := digest(RelationshipRequest{Action: action})
	var replay Profile
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Profile{}, false, err
	} else if found {
		return replay, true, nil
	}
	if _, err := loadSocialProfile(ctx, tx, actor, profileID); errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, false, ErrNotFound
	} else if err != nil {
		return Profile{}, false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM social.relationship_controls WHERE tenant_id=$1 AND country=$2 AND actor_identity_id=$3 AND target_identity_id=$4`, actor.TenantID, actor.Country, actor.Subject, profileID); err != nil {
		return Profile{}, false, err
	}
	if action != "NONE" {
		if _, err := tx.Exec(ctx, `INSERT INTO social.relationship_controls (tenant_id,country,actor_identity_id,target_identity_id,control,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, actor.TenantID, actor.Country, actor.Subject, profileID, action, service.Now()); err != nil {
			return Profile{}, false, mapSocialError(err)
		}
	}
	if action == "BLOCK" {
		rows, err := tx.Query(ctx, `DELETE FROM social.follows WHERE tenant_id=$1 AND country=$2 AND ((follower_identity_id=$3 AND following_identity_id=$4) OR (follower_identity_id=$4 AND following_identity_id=$3)) RETURNING follower_identity_id::text,following_identity_id::text,status`, actor.TenantID, actor.Country, actor.Subject, profileID)
		if err != nil {
			return Profile{}, false, err
		}
		for rows.Next() {
			var follower, following, status string
			if err := rows.Scan(&follower, &following, &status); err != nil {
				rows.Close()
				return Profile{}, false, err
			}
			if status == "ACCEPTED" {
				if err := updateFollowCounts(ctx, tx, follower, following, -1); err != nil {
					rows.Close()
					return Profile{}, false, err
				}
			}
		}
		rows.Close()
		if _, err := tx.Exec(ctx, `UPDATE social.conversations SET status='BLOCKED',updated_at=$5 WHERE tenant_id=$1 AND country=$2 AND participant_ids @> ARRAY[$3::uuid,$4::uuid]`, actor.TenantID, actor.Country, actor.Subject, profileID, service.Now()); err != nil {
			return Profile{}, false, err
		}
	}
	value, err := loadSocialProfile(ctx, tx, actor, profileID)
	if err != nil {
		return Profile{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Profile{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ReportPost(actor Actor, key, postID string, request ReportRequest) (Report, bool, error) {
	reason, details := strings.ToUpper(strings.TrimSpace(request.Reason)), strings.TrimSpace(request.Details)
	if !postgresSocialActor(actor) || !validCustomer(actor) || !validKey(key) || !socialUUID(postID) || !reportReasons[reason] || len([]rune(details)) < 8 || len([]rune(details)) > 1000 {
		return Report{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Report{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "report:" + postID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Report{}, false, err
	}
	fingerprint := digest(request)
	var replay Report
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Report{}, false, err
	} else if found {
		return replay, true, nil
	}
	post, err := loadSocialPost(ctx, tx, actor, postID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && post.Author.ID == actor.Subject {
		return Report{}, false, ErrNotFound
	}
	if err != nil {
		return Report{}, false, err
	}
	value := Report{ID: uuid.NewString(), PostID: postID, ReporterID: actor.Subject, Reason: reason, Details: details, Status: "OPEN", CreatedAt: service.Now(), UpdatedAt: service.Now(), tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO social.reports (id,tenant_id,country,post_id,reporter_identity_id,reason,details,status,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'OPEN',$8,$8)`, value.ID, actor.TenantID, actor.Country, postID, actor.Subject, reason, details, value.CreatedAt)
	if err != nil {
		return Report{}, false, mapSocialError(err)
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, service.Now()); err != nil {
		return Report{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Report{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ModerationQueue(actor Actor) ([]Report, error) {
	if !actor.MFAVerified {
		return nil, ErrMFARequired
	}
	if !postgresSocialActor(actor) || !validModerator(actor) {
		return nil, ErrForbidden
	}
	ctx, cancel := socialContext()
	defer cancel()
	rows, err := service.pool.Query(ctx, socialReportSelect+` WHERE tenant_id=$1 AND country=$2 AND status='OPEN' ORDER BY created_at,id`, actor.TenantID, actor.Country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Report{}
	for rows.Next() {
		value, scanErr := scanSocialReport(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) Moderate(actor Actor, key, reportID string, request ModerationDecisionRequest) (Report, bool, error) {
	if !actor.MFAVerified {
		return Report{}, false, ErrMFARequired
	}
	decision, note := strings.ToUpper(strings.TrimSpace(request.Decision)), strings.TrimSpace(request.Note)
	if !postgresSocialActor(actor) || !validModerator(actor) || !validKey(key) || !socialUUID(reportID) || decision != "REMOVE" && decision != "DISMISS" || len([]rune(note)) < 8 || len([]rune(note)) > 1000 {
		return Report{}, false, ErrInvalidRequest
	}
	ctx, cancel := socialContext()
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Report{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "moderate:" + reportID
	if err := socialCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Report{}, false, err
	}
	fingerprint := digest(request)
	var replay Report
	if found, err := loadSocialReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Report{}, false, err
	} else if found {
		return replay, true, nil
	}
	var postID, status string
	if err := tx.QueryRow(ctx, `SELECT post_id::text,status FROM social.reports WHERE id=$1 AND tenant_id=$2 AND country=$3 FOR UPDATE`, reportID, actor.TenantID, actor.Country).Scan(&postID, &status); errors.Is(err, pgx.ErrNoRows) || status != "OPEN" {
		return Report{}, false, ErrNotFound
	} else if err != nil {
		return Report{}, false, err
	}
	now := service.Now()
	if _, err := tx.Exec(ctx, `UPDATE social.reports SET status='DECIDED',decision=$2,decision_note=$3,decided_by_identity_id=$4,updated_at=$5 WHERE id=$1`, reportID, decision, note, actor.Subject, now); err != nil {
		return Report{}, false, err
	}
	if decision == "REMOVE" {
		if _, err := tx.Exec(ctx, `UPDATE social.posts SET status='REMOVED',moderation_reason='USER_REPORT_CONFIRMED',revision=revision+1,updated_at=$2 WHERE id=$1`, postID, now); err != nil {
			return Report{}, false, err
		}
	}
	value, err := loadSocialReport(ctx, tx, actor, reportID)
	if err != nil {
		return Report{}, false, err
	}
	if err := storeSocialReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Report{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Report{}, false, mapSocialError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ensureProfile(ctx context.Context, tx pgx.Tx, actor Actor) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM social.profiles WHERE identity_id=$1 AND tenant_id=$2 AND country=$3)`, actor.Subject, actor.TenantID, actor.Country).Scan(&exists); err != nil || exists {
		return err
	}
	var displayName string
	err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(p.display_name,''),'P4U Member') FROM identity.identities i JOIN identity.profiles p ON p.identity_id=i.id WHERE i.id=$1 AND i.tenant_id=$2 AND i.disabled_at IS NULL`, actor.Subject, actor.TenantID).Scan(&displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("resolve social identity profile: %w", err)
	}
	handle := "p4u_" + strings.ReplaceAll(actor.Subject, "-", "")[:12]
	_, err = tx.Exec(ctx, `INSERT INTO social.profiles (identity_id,tenant_id,country,handle,display_name,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$6) ON CONFLICT (identity_id) DO NOTHING`, actor.Subject, actor.TenantID, actor.Country, handle, displayName, service.Now())
	return err
}

const socialVisibleClause = ` WHERE p.tenant_id=$1 AND p.country=$2 AND (p.status='PUBLISHED' OR p.author_identity_id=$3) AND NOT EXISTS (SELECT 1 FROM social.relationship_controls b WHERE b.tenant_id=p.tenant_id AND b.country=p.country AND b.control='BLOCK' AND ((b.actor_identity_id=$3 AND b.target_identity_id=p.author_identity_id) OR (b.actor_identity_id=p.author_identity_id AND b.target_identity_id=$3))) AND (p.author_identity_id=$3 OR NOT a.private OR EXISTS (SELECT 1 FROM social.follows f WHERE f.tenant_id=p.tenant_id AND f.country=p.country AND f.follower_identity_id=$3 AND f.following_identity_id=p.author_identity_id AND f.status='ACCEPTED')) AND NOT EXISTS (SELECT 1 FROM social.relationship_controls m WHERE m.tenant_id=p.tenant_id AND m.country=p.country AND m.actor_identity_id=$3 AND m.target_identity_id=p.author_identity_id AND m.control='MUTE')`

const socialPostSelect = `SELECT p.id::text,p.revision,p.body,p.media_asset_ids::text[],p.hashtags,p.mentions,COALESCE(p.product_sticker_id::text,''),p.sponsored,COALESCE(p.sponsor_label,''),p.status,COALESCE(p.moderation_reason,''),p.like_count,p.comment_count,p.share_count,p.ranking_version,p.created_at,p.updated_at,a.identity_id::text,a.handle,a.display_name,a.bio,COALESCE(a.avatar_asset_id::text,''),a.private,a.verified,a.follower_count,a.following_count,EXISTS(SELECT 1 FROM social.post_engagement e WHERE e.tenant_id=p.tenant_id AND e.country=p.country AND e.post_id=p.id AND e.actor_identity_id=$3 AND e.kind='LIKE'),EXISTS(SELECT 1 FROM social.post_engagement e WHERE e.tenant_id=p.tenant_id AND e.country=p.country AND e.post_id=p.id AND e.actor_identity_id=$3 AND e.kind='SAVE'),p.tenant_id::text,p.country FROM social.posts p JOIN social.profiles a ON a.identity_id=p.author_identity_id`

func loadSocialPost(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string) (Post, error) {
	return scanSocialPost(querier.QueryRow(ctx, socialPostSelect+socialVisibleClause+` AND p.id=$4`, actor.TenantID, actor.Country, actor.Subject, id))
}

func scanSocialPost(row interface{ Scan(...any) error }) (Post, error) {
	var value Post
	err := row.Scan(&value.ID, &value.Revision, &value.Body, &value.MediaAssetIDs, &value.Hashtags, &value.Mentions, &value.ProductStickerID, &value.Sponsored, &value.SponsorLabel, &value.Status, &value.ModerationReason, &value.LikeCount, &value.CommentCount, &value.ShareCount, &value.RankingVersion, &value.CreatedAt, &value.UpdatedAt, &value.Author.ID, &value.Author.Handle, &value.Author.DisplayName, &value.Author.Bio, &value.Author.AvatarAssetID, &value.Author.Private, &value.Author.Verified, &value.Author.FollowerCount, &value.Author.FollowingCount, &value.Liked, &value.Saved, &value.tenantID, &value.country)
	if err != nil {
		return Post{}, err
	}
	value.Author.tenantID, value.Author.country = value.tenantID, value.country
	value.Author.Relationship, value.Author.AllowedActions = "NONE", []string{"FOLLOW", "MUTE", "BLOCK"}
	value.AllowedActions = []string{"LIKE", "SAVE", "SHARE", "COMMENT", "REPORT"}
	if value.Author.ID == "" {
		return Post{}, ErrNotFound
	}
	return value, nil
}

func loadSocialProfile(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string) (Profile, error) {
	var value Profile
	var blocked, muted bool
	var followStatus *string
	err := querier.QueryRow(ctx, `SELECT p.identity_id::text,p.handle,p.display_name,p.bio,COALESCE(p.avatar_asset_id::text,''),p.private,p.verified,p.follower_count,p.following_count,EXISTS(SELECT 1 FROM social.relationship_controls r WHERE r.tenant_id=p.tenant_id AND r.country=p.country AND r.actor_identity_id=$3 AND r.target_identity_id=p.identity_id AND r.control='BLOCK'),EXISTS(SELECT 1 FROM social.relationship_controls r WHERE r.tenant_id=p.tenant_id AND r.country=p.country AND r.actor_identity_id=$3 AND r.target_identity_id=p.identity_id AND r.control='MUTE'),(SELECT status FROM social.follows f WHERE f.tenant_id=p.tenant_id AND f.country=p.country AND f.follower_identity_id=$3 AND f.following_identity_id=p.identity_id),p.tenant_id::text,p.country FROM social.profiles p WHERE p.tenant_id=$1 AND p.country=$2 AND p.identity_id=$4 AND NOT EXISTS(SELECT 1 FROM social.relationship_controls r WHERE r.tenant_id=p.tenant_id AND r.country=p.country AND r.control='BLOCK' AND r.actor_identity_id=p.identity_id AND r.target_identity_id=$3)`, actor.TenantID, actor.Country, actor.Subject, id).Scan(&value.ID, &value.Handle, &value.DisplayName, &value.Bio, &value.AvatarAssetID, &value.Private, &value.Verified, &value.FollowerCount, &value.FollowingCount, &blocked, &muted, &followStatus, &value.tenantID, &value.country)
	if err != nil {
		return Profile{}, err
	}
	value.Relationship, value.AllowedActions = "NONE", []string{"FOLLOW", "MUTE", "BLOCK"}
	if actor.Subject == id {
		value.Relationship, value.AllowedActions = "SELF", []string{"EDIT", "PRIVACY"}
	} else if blocked {
		value.Relationship, value.AllowedActions = "BLOCKED", []string{"UNBLOCK"}
	} else if muted {
		value.Relationship, value.AllowedActions = "MUTED", []string{"UNMUTE", "BLOCK"}
	} else if followStatus != nil {
		value.Relationship = *followStatus
	}
	return value, nil
}

const socialCommentSelect = `SELECT c.id::text,c.post_id::text,COALESCE(c.parent_id::text,''),c.depth,c.body,c.mentions,c.status,c.created_at,p.identity_id::text,p.handle,p.display_name,p.bio,COALESCE(p.avatar_asset_id::text,''),p.private,p.verified,p.follower_count,p.following_count,c.tenant_id::text,c.country FROM social.comments c JOIN social.profiles p ON p.identity_id=c.author_identity_id`

func loadSocialComment(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string) (Comment, error) {
	return scanSocialComment(querier.QueryRow(ctx, socialCommentSelect+` WHERE c.id=$1 AND c.tenant_id=$2 AND c.country=$3`, id, actor.TenantID, actor.Country), actor.Subject)
}

func scanSocialComment(row interface{ Scan(...any) error }, viewer string) (Comment, error) {
	var value Comment
	if err := row.Scan(&value.ID, &value.PostID, &value.ParentID, &value.Depth, &value.Body, &value.Mentions, &value.Status, &value.CreatedAt, &value.Author.ID, &value.Author.Handle, &value.Author.DisplayName, &value.Author.Bio, &value.Author.AvatarAssetID, &value.Author.Private, &value.Author.Verified, &value.Author.FollowerCount, &value.Author.FollowingCount, &value.tenantID, &value.country); err != nil {
		return Comment{}, err
	}
	value.Author.tenantID, value.Author.country = value.tenantID, value.country
	value.AllowedActions = []string{}
	if value.Author.ID == viewer {
		value.AllowedActions = []string{"DELETE"}
	}
	return value, nil
}

const socialReportSelect = `SELECT id::text,post_id::text,reporter_identity_id::text,reason,details,status,COALESCE(decision,''),COALESCE(decision_note,''),created_at,updated_at,tenant_id::text,country FROM social.reports`

func loadSocialReport(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor, id string) (Report, error) {
	return scanSocialReport(querier.QueryRow(ctx, socialReportSelect+` WHERE id=$1 AND tenant_id=$2 AND country=$3`, id, actor.TenantID, actor.Country))
}

func scanSocialReport(row interface{ Scan(...any) error }) (Report, error) {
	var value Report
	err := row.Scan(&value.ID, &value.PostID, &value.ReporterID, &value.Reason, &value.Details, &value.Status, &value.Decision, &value.DecisionNote, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country)
	return value, err
}

func loadSocialPolicy(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor Actor) (socialPolicy, error) {
	var value socialPolicy
	var story, reel, media, presence, call, reward int64
	err := querier.QueryRow(ctx, `SELECT version,ranking_model,review_terms,story_ttl_seconds,reel_ttl_seconds,media_retention_seconds,presence_ttl_seconds,call_ttl_seconds,like_reward_points,follow_reward_points,share_reward_points,reward_expiry_seconds FROM social.policies WHERE tenant_id=$1 AND country=$2`, actor.TenantID, actor.Country).Scan(&value.version, &value.rankingModel, &value.reviewTerms, &story, &reel, &media, &presence, &call, &value.likePoints, &value.followPoints, &value.sharePoints, &reward)
	if errors.Is(err, pgx.ErrNoRows) {
		return socialPolicy{}, ErrInvalidRequest
	}
	value.storyTTL, value.reelTTL, value.mediaRetention = time.Duration(story)*time.Second, time.Duration(reel)*time.Second, time.Duration(media)*time.Second
	value.presenceTTL, value.callTTL, value.rewardExpiry = time.Duration(presence)*time.Second, time.Duration(call)*time.Second, time.Duration(reward)*time.Second
	return value, err
}

func loadSocialReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, destination any) (bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM social.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation=$4 AND idempotency_key=$5`, actor.TenantID, actor.Country, actor.Subject, operation, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedFingerprint != fingerprint {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return false, fmt.Errorf("decode social replay: %w", err)
	}
	return true, nil
}

func storeSocialReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, value any, now time.Time) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO social.idempotency_records (tenant_id,country,subject_id,operation,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.Subject, operation, key, fingerprint, payload, now)
	return err
}

func socialCommandLock(ctx context.Context, tx pgx.Tx, actor Actor, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+"|"+actor.Country+"|"+actor.Subject+"|"+operation+"|"+key)
	return err
}

func updateFollowCounts(ctx context.Context, tx pgx.Tx, follower, following string, delta int) error {
	if _, err := tx.Exec(ctx, `UPDATE social.profiles SET following_count=following_count+$2,updated_at=now() WHERE identity_id=$1`, follower, delta); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE social.profiles SET follower_count=follower_count+$2,updated_at=now() WHERE identity_id=$1`, following, delta)
	return err
}

func queueSocialReward(ctx context.Context, tx pgx.Tx, actor Actor, reference string, points int64, expiry time.Duration, now time.Time) error {
	if points < 1 {
		return nil
	}
	device := strings.TrimSpace(actor.DeviceReference)
	if device == "" {
		device = actor.Subject
	}
	_, err := tx.Exec(ctx, `INSERT INTO social.reward_events (id,tenant_id,country,actor_identity_id,event_reference,device_reference,points,expires_at,status,next_attempt_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'PENDING',$9,$9,$9) ON CONFLICT (tenant_id,country,event_reference) DO NOTHING`, uuid.NewString(), actor.TenantID, actor.Country, actor.Subject, reference, device, points, now.Add(expiry), now)
	return err
}

func (service *PostgresService) ProcessRewards(ctx context.Context, limit int) (int, error) {
	if service.rewarder == nil {
		return 0, nil
	}
	if limit < 1 || limit > 500 {
		return 0, ErrInvalidRequest
	}
	claimID := uuid.NewString()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,country,actor_identity_id::text,event_reference,device_reference,points,expires_at FROM social.reward_events WHERE status='PENDING' AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<=now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type event struct {
		id, tenant, country, actor, reference, device string
		points                                        int64
		expires                                       time.Time
	}
	values := []event{}
	for rows.Next() {
		var value event
		if err := rows.Scan(&value.id, &value.tenant, &value.country, &value.actor, &value.reference, &value.device, &value.points, &value.expires); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, value := range values {
		if _, err := tx.Exec(ctx, `UPDATE social.reward_events SET lease_owner=$2,lease_until=now()+interval '5 minutes',updated_at=now() WHERE id=$1 AND status='PENDING'`, value.id, claimID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	processed := 0
	for _, value := range values {
		entry, _, rewardErr := service.rewarder.RewardEngagement(wallet.Scope{TenantID: value.tenant, Country: value.country, CustomerID: value.actor}, "social-reward-"+value.id, value.reference, value.device, value.points, value.expires)
		if rewardErr == nil {
			tag, err := service.pool.Exec(ctx, `UPDATE social.reward_events SET status='APPLIED',wallet_entry_id=$2,last_error='',attempts=attempts+1,lease_owner=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND status='PENDING' AND lease_owner=$3`, value.id, entry.ID, claimID)
			if err != nil {
				return processed, err
			}
			if tag.RowsAffected() == 1 {
				processed++
			}
			continue
		}
		status := "PENDING"
		if errors.Is(rewardErr, wallet.ErrRewardNotEligible) || errors.Is(rewardErr, wallet.ErrInvalidRequest) {
			status = "REJECTED"
		}
		if _, err := service.pool.Exec(ctx, `UPDATE social.reward_events SET status=$2,last_error=$3,attempts=attempts+1,next_attempt_at=now()+LEAST(interval '1 hour',interval '5 seconds'*power(2,LEAST(attempts,10))),lease_owner=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND status='PENDING' AND lease_owner=$4`, value.id, status, truncateSocialError(rewardErr), claimID); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func socialContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), socialOperationTimeout)
}

func postgresSocialActor(actor Actor) bool {
	return uuid.Validate(actor.TenantID) == nil && uuid.Validate(actor.Subject) == nil && countryPattern.MatchString(actor.Country)
}

func socialUUID(value string) bool { return uuid.Validate(value) == nil }

func allSocialUUIDs(values []string) bool {
	for _, value := range values {
		if !socialUUID(value) {
			return false
		}
	}
	return true
}

func encodePostgresCursor(model string, offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(model + ":" + strconv.Itoa(offset)))
}

func decodePostgresCursor(cursor, model string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(decoded), ":")
	if err != nil || len(decoded) > 128 || len(parts) != 2 || parts[0] != model {
		return 0, ErrInvalidRequest
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return 0, ErrInvalidRequest
	}
	return offset, nil
}

func mapSocialError(err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && (postgres.Code == "23505" || postgres.Code == "40001" || postgres.Code == "40P01") {
		return ErrConflict
	}
	return err
}

func truncateSocialError(err error) string {
	value := err.Error()
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
