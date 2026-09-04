package social

import (
	"errors"
	"testing"
	"time"
)

func TestBEP5002PrivateFollowAndBlockPrecedence(t *testing.T) {
	service := testService(t)
	viewer := socialActorFor("customer-synthetic-001", "CUSTOMER", false)
	private := socialActorFor("customer-private-001", "CUSTOMER", false)

	feed, err := service.Feed(viewer, "", 20)
	if err != nil || len(feed.Items) != 1 || feed.Items[0].Author.ID != "customer-public-001" {
		t.Fatalf("public feed=%#v err=%v", feed, err)
	}
	follow, replay, err := service.Follow(viewer, "follow-key-0001", private.Subject)
	if err != nil || replay || follow.Status != "PENDING" {
		t.Fatalf("follow=%#v replay=%v err=%v", follow, replay, err)
	}
	if _, _, err := service.AcceptFollow(private, "accept-key-0001", viewer.Subject); err != nil {
		t.Fatalf("accept: %v", err)
	}
	feed, err = service.Feed(viewer, "", 20)
	if err != nil || len(feed.Items) != 2 {
		t.Fatalf("accepted feed=%#v err=%v", feed, err)
	}
	if _, _, err := service.SetRelationship(private, "block-key-0001", viewer.Subject, "BLOCK"); err != nil {
		t.Fatalf("block: %v", err)
	}
	feed, err = service.Feed(viewer, "", 20)
	if err != nil || len(feed.Items) != 1 || feed.Items[0].Author.ID == private.Subject {
		t.Fatalf("blocked content leaked: %#v err=%v", feed, err)
	}
}

func TestBEP5003ModeratedFeedCursorAndIdempotentEngagement(t *testing.T) {
	service := testService(t)
	actor := socialActorFor("customer-synthetic-001", "CUSTOMER", false)

	pending, replay, err := service.CreatePost(actor, "post-key-review-0001", CreatePostRequest{Body: "This needs manual-review before publishing"})
	if err != nil || replay || pending.Status != PostPendingReview {
		t.Fatalf("pending=%#v replay=%v err=%v", pending, replay, err)
	}
	created, _, err := service.CreatePost(actor, "post-key-public-0001", CreatePostRequest{Body: "Local #Chennai update for @public_user", MediaAssetIDs: []string{"asset-private-social-001"}})
	if err != nil || created.Status != PostPublished || len(created.Hashtags) != 1 || len(created.Mentions) != 1 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	liked, replay, err := service.SetLike(actor, "like-key-0001", created.ID, created.Revision, true)
	if err != nil || replay || !liked.Liked || liked.LikeCount != 1 || liked.Revision != 2 {
		t.Fatalf("liked=%#v replay=%v err=%v", liked, replay, err)
	}
	replayed, replay, err := service.SetLike(actor, "like-key-0001", created.ID, created.Revision, true)
	if err != nil || !replay || replayed.LikeCount != 1 {
		t.Fatalf("replayed=%#v replay=%v err=%v", replayed, replay, err)
	}
	if _, _, err := service.SetSave(actor, "save-key-0001", created.ID, created.Revision, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save error=%v", err)
	}
	page, err := service.Feed(actor, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.RankingVersion != "socio-feed-v1" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	next, err := service.Feed(actor, page.NextCursor, 10)
	if err != nil || len(next.Items) < 1 {
		t.Fatalf("next=%#v err=%v", next, err)
	}
	for _, item := range append(page.Items, next.Items...) {
		if item.ID == pending.ID {
			t.Fatal("pending review post appeared in feed")
		}
	}
}

func TestBEP5004NestedCommentsReportAndMFAModeration(t *testing.T) {
	service := testService(t)
	viewer := socialActorFor("customer-synthetic-001", "CUSTOMER", false)
	post, err := service.Post(viewer, "social-post-public-001")
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := service.CreateComment(viewer, "comment-key-root-0001", post.ID, CreateCommentRequest{Body: "Useful update @public_user"})
	if err != nil || root.Depth != 0 {
		t.Fatalf("root=%#v err=%v", root, err)
	}
	reply, _, err := service.CreateComment(viewer, "comment-key-reply-0001", post.ID, CreateCommentRequest{ParentID: root.ID, Body: "Adding a reply"})
	if err != nil || reply.Depth != 1 {
		t.Fatalf("reply=%#v err=%v", reply, err)
	}
	nested, _, err := service.CreateComment(viewer, "comment-key-nested-0001", post.ID, CreateCommentRequest{ParentID: reply.ID, Body: "Final nested reply"})
	if err != nil || nested.Depth != 2 {
		t.Fatalf("nested=%#v err=%v", nested, err)
	}
	if _, _, err := service.CreateComment(viewer, "comment-key-too-deep", post.ID, CreateCommentRequest{ParentID: nested.ID, Body: "Too deep"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("depth error=%v", err)
	}
	report, _, err := service.ReportPost(viewer, "report-key-0001", post.ID, ReportRequest{Reason: "SPAM", Details: "Repeated misleading promotion"})
	if err != nil {
		t.Fatal(err)
	}
	moderatorWithoutMFA := socialActorFor("moderator-001", "MODERATOR", false)
	if _, err := service.ModerationQueue(moderatorWithoutMFA); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("queue MFA error=%v", err)
	}
	moderator := socialActorFor("moderator-001", "MODERATOR", true)
	queue, err := service.ModerationQueue(moderator)
	if err != nil || len(queue) != 1 || queue[0].ID != report.ID {
		t.Fatalf("queue=%#v err=%v", queue, err)
	}
	if _, _, err := service.Moderate(moderator, "moderate-key-0001", report.ID, ModerationDecisionRequest{Decision: "REMOVE", Note: "Confirmed repetitive commercial spam"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Post(viewer, post.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed post error=%v", err)
	}
}

func TestSocialHandleAndPrivateProfileContentBoundaries(t *testing.T) {
	service := testService(t)
	viewer := socialActorFor("customer-synthetic-001", "CUSTOMER", false)
	private := socialActorFor("customer-private-001", "CUSTOMER", false)

	profile, err := service.ProfileByHandle(viewer, "@PUBLIC_USER")
	if err != nil || profile.ID != "customer-public-001" {
		t.Fatalf("resolved profile=%#v err=%v", profile, err)
	}
	if _, err := service.ProfileContent(viewer, private.Subject, "POSTS"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private content error=%v", err)
	}
	if _, _, err := service.Follow(viewer, "profile-content-follow-0001", private.Subject); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AcceptFollow(private, "profile-content-accept-0001", viewer.Subject); err != nil {
		t.Fatal(err)
	}
	items, err := service.ProfileContent(viewer, private.Subject, "POSTS")
	if err != nil || len(items) != 1 {
		t.Fatalf("private content=%#v err=%v", items, err)
	}
	post, err := service.Post(viewer, "social-post-public-001")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.SetSave(viewer, "profile-content-save-0001", post.ID, post.Revision, true); err != nil {
		t.Fatal(err)
	}
	saved, err := service.ProfileContent(viewer, viewer.Subject, "SAVED")
	if err != nil || len(saved) != 1 {
		t.Fatalf("saved content=%#v err=%v", saved, err)
	}
	if _, _, err := service.SetRelationship(private, "profile-content-block-0001", viewer.Subject, "BLOCK"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProfileByHandle(viewer, "private_user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blocked handle error=%v", err)
	}
}

func TestSocialListCursorScopeAndLimits(t *testing.T) {
	first, cursor, err := paginateSocial([]int{1, 2, 3}, "", 1, "comments:post-1")
	if err != nil || len(first) != 1 || cursor == "" {
		t.Fatalf("first=%v cursor=%q err=%v", first, cursor, err)
	}
	if _, _, err := paginateSocial([]int{1, 2, 3}, cursor, 1, "messages:conversation-1"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cross-scope cursor error=%v", err)
	}
	if _, _, err := paginateSocial([]int{1}, "", maximumSocialPageLimit+1, "comments:post-1"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("maximum limit error=%v", err)
	}
}

func testService(t *testing.T) *Service {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	profiles := []Profile{
		{ID: "customer-synthetic-001", Handle: "synthetic_user", DisplayName: "Synthetic Customer"},
		{ID: "customer-public-001", Handle: "public_user", DisplayName: "Public Neighbour", Verified: true},
		{ID: "customer-private-001", Handle: "private_user", DisplayName: "Private Neighbour", Private: true},
	}
	posts := []Post{
		{ID: "social-post-public-001", Revision: 1, Author: profiles[1], Body: "Community market this weekend #local", Status: PostPublished, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{ID: "social-post-private-001", Revision: 1, Author: profiles[2], Body: "Followers-only update", Status: PostPublished, CreatedAt: now.Add(-30 * time.Minute), UpdatedAt: now.Add(-30 * time.Minute)},
	}
	service, err := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN", Profiles: profiles, Posts: posts, ReviewTerms: []string{"manual-review"}, RankingModel: "socio-feed-v1"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func socialActorFor(subject, role string, mfa bool) Actor {
	return Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: subject, Roles: []string{role}, MFAVerified: mfa}
}
