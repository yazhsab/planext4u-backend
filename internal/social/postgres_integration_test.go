//go:build integration

package social

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

const (
	socialTenantOne    = "13000000-0000-4000-8000-000000000001"
	socialTenantTwo    = "13000000-0000-4000-8000-000000000002"
	socialMemberOne    = "23000000-0000-4000-8000-000000000001"
	socialMemberTwo    = "23000000-0000-4000-8000-000000000002"
	socialMemberThree  = "23000000-0000-4000-8000-000000000003"
	socialModeratorOne = "33000000-0000-4000-8000-000000000001"
	socialModeratorTwo = "33000000-0000-4000-8000-000000000002"
	socialAssetOne     = "43000000-0000-4000-8000-000000000001"
	socialAssetTwo     = "43000000-0000-4000-8000-000000000002"
)

type recordedRewarder struct {
	mu     sync.Mutex
	events []string
}

func (rewarder *recordedRewarder) RewardEngagement(_ wallet.Scope, _ string, reference, _ string, _ int64, _ time.Time) (wallet.LedgerEntry, bool, error) {
	rewarder.mu.Lock()
	defer rewarder.mu.Unlock()
	rewarder.events = append(rewarder.events, reference)
	return wallet.LedgerEntry{ID: uuid.NewString()}, false, nil
}

func TestPostgresSocialDurabilityPrivacyConcurrencyRewardsAndRetention(t *testing.T) {
	databaseURL := os.Getenv("SOCIAL_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("SOCIAL_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS social CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS social CASCADE`)
	})
	for _, path := range []string{"../../migrations/platform/000006_phase5_roles.up.sql", "../../migrations/social/000001_social_trust.up.sql", "../../migrations/social/000002_engagement_media_messaging.up.sql", "../../migrations/social/000003_durable_social_runtime.up.sql", "../../migrations/social/000004_shares_reward_leases_admin_grant.up.sql"} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `INSERT INTO social.policies (tenant_id,country,version,ranking_model,review_terms,story_ttl_seconds,reel_ttl_seconds,media_retention_seconds,presence_ttl_seconds,call_ttl_seconds,like_reward_points,follow_reward_points,share_reward_points,reward_expiry_seconds,updated_at)
		VALUES ($1,'IN','social-v1','socio-feed-v1',ARRAY['manual-review'],86400,2592000,2592000,120,120,10,20,15,31536000,$2)`, socialTenantOne, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO social.profiles (identity_id,tenant_id,country,handle,display_name,private,created_at,updated_at) VALUES
		($3,$1,'IN','member_one','Member One',false,$2,$2),
		($4,$1,'IN','member_two','Member Two',false,$2,$2),
		($5,$1,'IN','member_three','Member Three',true,$2,$2),
		($6,$1,'IN','moderator_one','Moderator One',false,$2,$2),
		($7,$1,'IN','moderator_two','Moderator Two',false,$2,$2)`, socialTenantOne, now, socialMemberOne, socialMemberTwo, socialMemberThree, socialModeratorOne, socialModeratorTwo)
	if err != nil {
		t.Fatal(err)
	}
	rewarder := &recordedRewarder{}
	service, err := NewPostgresService(pool, func() time.Time { return now }, rewarder)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	one := Actor{TenantID: socialTenantOne, Country: "IN", Subject: socialMemberOne, DeviceReference: "device-social-one", Roles: []string{"CUSTOMER"}}
	two := Actor{TenantID: socialTenantOne, Country: "IN", Subject: socialMemberTwo, DeviceReference: "device-social-two", Roles: []string{"CUSTOMER"}}
	three := Actor{TenantID: socialTenantOne, Country: "IN", Subject: socialMemberThree, Roles: []string{"CUSTOMER"}}
	moderatorOne := Actor{TenantID: socialTenantOne, Country: "IN", Subject: socialModeratorOne, Roles: []string{"MODERATOR"}, MFAVerified: true}
	moderatorTwo := Actor{TenantID: socialTenantOne, Country: "IN", Subject: socialModeratorTwo, Roles: []string{"CONTENT_ADMIN"}, MFAVerified: true}

	postRequest := CreatePostRequest{Body: "A durable #Chennai neighbourhood update"}
	post, replay, err := service.CreatePost(one, "social-post-create-0001", postRequest)
	if err != nil || replay || post.Status != PostPublished {
		t.Fatalf("post=%#v replay=%t err=%v", post, replay, err)
	}
	restarted, err := NewPostgresService(pool, func() time.Time { return now }, rewarder)
	if err != nil {
		t.Fatal(err)
	}
	postReplay, replay, err := restarted.CreatePost(one, "social-post-create-0001", postRequest)
	if err != nil || !replay || postReplay.ID != post.ID {
		t.Fatalf("post replay=%#v replay=%t err=%v", postReplay, replay, err)
	}
	if visible, err := restarted.Post(two, post.ID); err != nil || visible.ID != post.ID {
		t.Fatalf("post visibility=%#v err=%v", visible, err)
	}

	results := make(chan struct {
		post   Post
		replay bool
		err    error
	}, 2)
	for range 2 {
		go func() {
			value, replayed, likeErr := restarted.SetLike(two, "social-like-concurrent-0001", post.ID, post.Revision, true)
			results <- struct {
				post   Post
				replay bool
				err    error
			}{value, replayed, likeErr}
		}()
	}
	replays := 0
	for range 2 {
		result := <-results
		if result.err != nil || result.post.LikeCount != 1 {
			t.Fatalf("concurrent like=%#v err=%v", result.post, result.err)
		}
		if result.replay {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf("like replays=%d", replays)
	}
	rewardResults := make(chan struct {
		processed int
		err       error
	}, 2)
	for range 2 {
		go func() {
			processed, processErr := restarted.ProcessRewards(ctx, 20)
			rewardResults <- struct {
				processed int
				err       error
			}{processed: processed, err: processErr}
		}()
	}
	processedRewards := 0
	for range 2 {
		result := <-rewardResults
		if result.err != nil {
			t.Fatal(result.err)
		}
		processedRewards += result.processed
	}
	if processedRewards != 1 {
		t.Fatalf("concurrent rewards processed=%d", processedRewards)
	}
	shared, replay, err := restarted.SharePost(two, "social-share-0001", post.ID, ShareRequest{Channel: "LINK"})
	if err != nil || replay || shared.ShareCount != 1 {
		t.Fatalf("share=%#v replay=%t err=%v", shared, replay, err)
	}
	shared, replay, err = restarted.SharePost(two, "social-share-0001", post.ID, ShareRequest{Channel: "LINK"})
	if err != nil || !replay || shared.ShareCount != 1 {
		t.Fatalf("share replay=%#v replay=%t err=%v", shared, replay, err)
	}
	if processed, err := restarted.ProcessRewards(ctx, 20); err != nil || processed != 1 {
		t.Fatalf("share rewards processed=%d err=%v", processed, err)
	}

	follow, replay, err := restarted.Follow(one, "social-follow-private-0001", three.Subject)
	if err != nil || replay || follow.Status != "PENDING" {
		t.Fatalf("follow=%#v replay=%t err=%v", follow, replay, err)
	}
	if _, _, err := restarted.AcceptFollow(three, "social-follow-accept-0001", one.Subject); err != nil {
		t.Fatal(err)
	}
	if processed, err := restarted.ProcessRewards(ctx, 20); err != nil || processed != 1 {
		t.Fatalf("follow rewards processed=%d err=%v", processed, err)
	}

	comment, _, err := restarted.CreateComment(two, "social-comment-create-0001", post.ID, CreateCommentRequest{Body: "Useful neighbourhood update"})
	if err != nil || comment.Depth != 0 {
		t.Fatalf("comment=%#v err=%v", comment, err)
	}
	report, _, err := restarted.ReportPost(two, "social-report-create-0001", post.ID, ReportRequest{Reason: "SPAM", Details: "Repeated commercial promotion content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.Moderate(moderatorOne, "social-report-moderate-0001", report.ID, ModerationDecisionRequest{Decision: "DISMISS", Note: "Content complies with the published policy"}); err != nil {
		t.Fatal(err)
	}

	rejected, _, err := restarted.CreateMedia(one, "social-media-create-0001", CreateMediaRequest{AssetID: socialAssetOne, Kind: "IMAGE"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, _, err = restarted.ProcessMedia(moderatorOne, "social-media-process-0001", rejected.ID, ProcessMediaRequest{Clean: false})
	if err != nil || rejected.State != MediaRejected {
		t.Fatalf("rejected=%#v err=%v", rejected, err)
	}
	if _, _, err := restarted.AppealMedia(one, "social-media-appeal-0001", rejected.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.DecideMediaAppeal(moderatorOne, "social-media-appeal-decision-01", rejected.ID, AppealDecisionRequest{Approve: true, Note: "Independent review is required here"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same moderator appeal err=%v", err)
	}
	approved, _, err := restarted.DecideMediaAppeal(moderatorTwo, "social-media-appeal-decision-02", rejected.ID, AppealDecisionRequest{Approve: true, Note: "Independent review confirms safe media"})
	if err != nil || approved.State != MediaReady {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	story, _, err := restarted.CreateEphemeral(one, "social-story-create-0001", CreateEphemeralRequest{Kind: "STORY", MediaJobID: approved.ID, Caption: "Local update"})
	if err != nil {
		t.Fatal(err)
	}
	collection, _, err := restarted.CreateCollection(one, "social-collection-create-01", "Local favourites")
	if err != nil {
		t.Fatal(err)
	}
	collection, _, err = restarted.SetCollectionPost(one, "social-collection-post-0001", collection.ID, post.ID, true)
	if err != nil || len(collection.PostIDs) != 1 {
		t.Fatalf("collection=%#v err=%v", collection, err)
	}

	conversation, _, err := restarted.OpenConversation(one, "social-conversation-open-01", two.Subject)
	if err != nil || conversation.Status != "REQUESTED" {
		t.Fatalf("conversation=%#v err=%v", conversation, err)
	}
	conversation, _, err = restarted.AcceptConversation(two, "social-conversation-accept-01", conversation.ID)
	if err != nil || conversation.Status != "ACCEPTED" {
		t.Fatalf("accept conversation=%#v err=%v", conversation, err)
	}
	voice, _, err := restarted.CreateMedia(one, "social-voice-create-000001", CreateMediaRequest{AssetID: socialAssetTwo, Kind: "VOICE"})
	if err != nil {
		t.Fatal(err)
	}
	voice, _, err = restarted.ProcessMedia(moderatorOne, "social-voice-process-00001", voice.ID, ProcessMediaRequest{Clean: true})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := restarted.SendMessage(one, "social-message-send-000001", conversation.ID, SendMessageRequest{Body: "Durable voice note", VoiceMediaID: voice.ID})
	if err != nil {
		t.Fatal(err)
	}
	restartedAgain, _ := NewPostgresService(pool, func() time.Time { return now }, rewarder)
	messages, err := restartedAgain.Messages(two, conversation.ID)
	if err != nil || len(messages) != 1 || messages[0].ID != message.ID {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	if _, err := restartedAgain.SetPresence(two, PresenceRequest{State: "ONLINE"}); err != nil {
		t.Fatal(err)
	}
	presence, err := restartedAgain.Presence(one, two.Subject)
	if err != nil || presence.State != "ONLINE" {
		t.Fatalf("presence=%#v err=%v", presence, err)
	}
	call, _, err := restartedAgain.CreateCall(one, "social-call-create-000001", conversation.ID, "VIDEO")
	if err != nil {
		t.Fatal(err)
	}
	call, err = restartedAgain.SignalCall(two, call.ID, SignalRequest{Type: "ANSWER", Payload: "opaque-sdp-answer"})
	if err != nil || call.Status != "CONNECTED" || call.SignalCount != 1 {
		t.Fatalf("call=%#v err=%v", call, err)
	}
	var digestOnly string
	if err := pool.QueryRow(ctx, `SELECT payload_digest FROM social.call_signals WHERE call_id=$1`, call.ID).Scan(&digestOnly); err != nil || digestOnly == "opaque-sdp-answer" || len(digestOnly) != 64 {
		t.Fatalf("signal digest=%q err=%v", digestOnly, err)
	}

	now = now.Add(25 * time.Hour)
	if count, err := restartedAgain.PurgeExpired(moderatorOne); err != nil || count != 1 {
		t.Fatalf("purge=%d err=%v story=%s", count, err, story.ID)
	}
	wrongTenant := Actor{TenantID: socialTenantTwo, Country: "IN", Subject: socialMemberOne, Roles: []string{"CUSTOMER"}}
	if _, err := restartedAgain.Post(wrongTenant, post.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant post err=%v", err)
	}
	var posts, messagesCount, calls, signals, rewards, replaysCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM social.posts),(SELECT count(*) FROM social.direct_messages),(SELECT count(*) FROM social.call_sessions),(SELECT count(*) FROM social.call_signals),(SELECT count(*) FROM social.reward_events),(SELECT count(*) FROM social.idempotency_records)`).Scan(&posts, &messagesCount, &calls, &signals, &rewards, &replaysCount); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || messagesCount != 1 || calls != 1 || signals != 1 || rewards != 3 || replaysCount < 15 {
		t.Fatalf("rows posts=%d messages=%d calls=%d signals=%d rewards=%d replays=%d", posts, messagesCount, calls, signals, rewards, replaysCount)
	}
}
