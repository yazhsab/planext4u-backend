package social

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type idempotentResult struct {
	fingerprint string
	resourceID  string
}

type Service struct {
	clock         func() time.Time
	configuration Configuration
	mu            sync.Mutex
	sequence      int64
	profiles      map[string]*Profile
	posts         map[string]*Post
	shares        map[string]bool
	comments      map[string]*Comment
	follows       map[string]*Follow
	blocked       map[string]bool
	muted         map[string]bool
	reports       map[string]*Report
	idempotency   map[string]idempotentResult
	mediaJobs     map[string]*MediaJob
	ephemeral     map[string]*EphemeralContent
	collections   map[string]*Collection
	conversations map[string]*Conversation
	messages      map[string][]DirectMessage
	presence      map[string]Presence
	calls         map[string]*CallSession
	tombstones    map[string]time.Time
}

func NewService(configuration Configuration, clock func() time.Time) (*Service, error) {
	if clock == nil || !safeID(configuration.TenantID) || !countryPattern.MatchString(configuration.Country) || len(configuration.Profiles) == 0 {
		return nil, ErrInvalidRequest
	}
	if configuration.RankingModel == "" {
		configuration.RankingModel = "socio-feed-v1"
	}
	service := &Service{
		clock: clock, configuration: configuration, profiles: map[string]*Profile{}, posts: map[string]*Post{}, shares: map[string]bool{},
		comments: map[string]*Comment{}, follows: map[string]*Follow{}, blocked: map[string]bool{}, muted: map[string]bool{},
		reports: map[string]*Report{}, idempotency: map[string]idempotentResult{},
		mediaJobs: map[string]*MediaJob{}, ephemeral: map[string]*EphemeralContent{}, collections: map[string]*Collection{},
		conversations: map[string]*Conversation{}, messages: map[string][]DirectMessage{}, presence: map[string]Presence{}, calls: map[string]*CallSession{}, tombstones: map[string]time.Time{},
	}
	for index := range configuration.Profiles {
		value := cloneProfile(configuration.Profiles[index])
		if !safeID(value.ID) || !handlePattern.MatchString(value.Handle) || len(strings.TrimSpace(value.DisplayName)) < 2 {
			return nil, ErrInvalidRequest
		}
		value.tenantID, value.country = configuration.TenantID, configuration.Country
		service.profiles[value.ID] = &value
	}
	for index := range configuration.Posts {
		value := clonePost(configuration.Posts[index])
		if !safeID(value.ID) || service.profiles[value.Author.ID] == nil || value.Status == "" || value.CreatedAt.IsZero() {
			return nil, ErrInvalidRequest
		}
		value.tenantID, value.country = configuration.TenantID, configuration.Country
		value.likes, value.saves = map[string]bool{}, map[string]bool{}
		value.RankingVersion = configuration.RankingModel
		service.posts[value.ID] = &value
	}
	return service, nil
}

func (service *Service) Feed(actor Actor, cursor string, limit int) (FeedPage, error) {
	if !validCustomer(actor) {
		return FeedPage{}, ErrForbidden
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return FeedPage{}, ErrInvalidRequest
	}
	offset, err := service.decodeCursor(cursor)
	if err != nil {
		return FeedPage{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := make([]Post, 0, len(service.posts))
	for _, value := range service.posts {
		if value.Status != PostPublished || !service.canViewPostLocked(actor, value) || service.muted[relationKey(actor.Subject, value.Author.ID)] {
			continue
		}
		values = append(values, service.presentPostLocked(actor, value))
	}
	sort.SliceStable(values, func(left, right int) bool {
		leftScore, rightScore := feedScore(values[left]), feedScore(values[right])
		if leftScore == rightScore {
			if values[left].CreatedAt.Equal(values[right].CreatedAt) {
				return values[left].ID < values[right].ID
			}
			return values[left].CreatedAt.After(values[right].CreatedAt)
		}
		return leftScore > rightScore
	})
	if offset > len(values) {
		return FeedPage{}, ErrInvalidRequest
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	page := FeedPage{Items: append([]Post(nil), values[offset:end]...), RankingVersion: service.configuration.RankingModel}
	if end < len(values) {
		page.NextCursor = service.encodeCursor(end)
	}
	return page, nil
}

func (service *Service) Profile(actor Actor, profileID string) (Profile, error) {
	if !validCustomer(actor) || !safeID(profileID) {
		return Profile{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	profile := service.profiles[profileID]
	if profile == nil || profile.tenantID != actor.TenantID || profile.country != actor.Country || service.isBlockedLocked(actor.Subject, profileID) {
		return Profile{}, ErrNotFound
	}
	return service.presentProfileLocked(actor.Subject, profile), nil
}

func (service *Service) ProfileByHandle(actor Actor, handle string) (Profile, error) {
	handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	if !validCustomer(actor) || !handlePattern.MatchString(handle) {
		return Profile{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, profile := range service.profiles {
		if profile.Handle == handle && profile.tenantID == actor.TenantID && profile.country == actor.Country && !service.isBlockedLocked(actor.Subject, profile.ID) {
			return service.presentProfileLocked(actor.Subject, profile), nil
		}
	}
	return Profile{}, ErrNotFound
}

func (service *Service) ProfileContent(actor Actor, profileID, kind string) ([]any, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if !validCustomer(actor) || !safeID(profileID) || !map[string]bool{"POSTS": true, "REELS": true, "TAGGED": true, "SAVED": true}[kind] {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	profile := service.profiles[profileID]
	if profile == nil || profile.tenantID != actor.TenantID || profile.country != actor.Country || service.isBlockedLocked(actor.Subject, profileID) {
		return nil, ErrNotFound
	}
	if profile.Private && profile.ID != actor.Subject && !service.followAcceptedLocked(actor.Subject, profile.ID) {
		return nil, ErrNotFound
	}
	if kind == "SAVED" && profile.ID != actor.Subject {
		return nil, ErrNotFound
	}
	if kind == "REELS" {
		values := make([]EphemeralContent, 0)
		now := service.clock().UTC()
		for _, value := range service.ephemeral {
			if value.Author.ID != profile.ID || value.Kind != "REEL" || value.Status != "PUBLISHED" || (!value.Highlighted && !now.Before(value.ExpiresAt)) {
				continue
			}
			clone := cloneEphemeral(*value)
			clone.Author = service.presentProfileLocked(actor.Subject, profile)
			if profile.ID != actor.Subject {
				clone.AllowedActions = []string{"REPORT"}
			}
			values = append(values, clone)
		}
		sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
		items := make([]any, len(values))
		for index := range values {
			items[index] = values[index]
		}
		return items, nil
	}
	values := make([]Post, 0)
	for _, value := range service.posts {
		if value.Status != PostPublished || !service.canViewPostLocked(actor, value) {
			continue
		}
		matches := kind == "POSTS" && value.Author.ID == profile.ID || kind == "SAVED" && value.saves[actor.Subject]
		if kind == "TAGGED" {
			for _, mention := range value.Mentions {
				normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(mention)), "@")
				if normalized == profile.Handle || normalized == strings.ToLower(profile.ID) {
					matches = true
					break
				}
			}
		}
		if matches {
			values = append(values, service.presentPostLocked(actor, value))
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	items := make([]any, len(values))
	for index := range values {
		items[index] = values[index]
	}
	return items, nil
}

func (service *Service) Post(actor Actor, postID string) (Post, error) {
	if !validCustomer(actor) || !safeID(postID) {
		return Post{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.posts[postID]
	if value == nil || (value.Status != PostPublished && value.Author.ID != actor.Subject) || !service.canViewPostLocked(actor, value) {
		return Post{}, ErrNotFound
	}
	return service.presentPostLocked(actor, value), nil
}

func (service *Service) CreatePost(actor Actor, key string, request CreatePostRequest) (Post, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !validPostRequest(request) {
		return Post{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	profile := service.profiles[actor.Subject]
	if profile == nil || profile.tenantID != actor.TenantID || profile.country != actor.Country {
		return Post{}, false, ErrForbidden
	}
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "create-post", key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Post{}, false, ErrIdempotencyConflict
		}
		return service.presentPostLocked(actor, service.posts[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	service.sequence++
	body := strings.TrimSpace(request.Body)
	status, reason := PostPublished, ""
	for _, term := range service.configuration.ReviewTerms {
		if strings.Contains(strings.ToLower(body), strings.ToLower(strings.TrimSpace(term))) {
			status, reason = PostPendingReview, "AUTOMATED_POLICY_REVIEW"
			break
		}
	}
	value := &Post{
		ID: "social-post-" + sequenceID(service.sequence), Revision: 1, Author: cloneProfile(*profile), Body: body,
		MediaAssetIDs: append([]string(nil), request.MediaAssetIDs...), Hashtags: extract(body, hashtagPattern), Mentions: extract(body, mentionPattern),
		ProductStickerID: request.ProductStickerID, Status: status, ModerationReason: reason, RankingVersion: service.configuration.RankingModel,
		CreatedAt: now, UpdatedAt: now, likes: map[string]bool{}, saves: map[string]bool{}, tenantID: actor.TenantID, country: actor.Country,
	}
	service.posts[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return service.presentPostLocked(actor, value), false, nil
}

func (service *Service) SetLike(actor Actor, key, postID string, revision int64, active bool) (Post, bool, error) {
	return service.setEngagement(actor, key, postID, revision, "like", active)
}

func (service *Service) SetSave(actor Actor, key, postID string, revision int64, active bool) (Post, bool, error) {
	return service.setEngagement(actor, key, postID, revision, "save", active)
}

func (service *Service) SharePost(actor Actor, key, postID string, request ShareRequest) (Post, bool, error) {
	channel := strings.ToUpper(strings.TrimSpace(request.Channel))
	if !validCustomer(actor) || !validKey(key) || !safeID(postID) || !validShareChannel(channel) {
		return Post{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.posts[postID]
	if value == nil || value.Status != PostPublished || !service.canViewPostLocked(actor, value) {
		return Post{}, false, ErrNotFound
	}
	normalized := ShareRequest{Channel: channel}
	fingerprint := digest(normalized)
	scope := idempotencyScope(actor, "share:"+postID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Post{}, false, ErrIdempotencyConflict
		}
		return service.presentPostLocked(actor, value), true, nil
	}
	shareKey := actor.TenantID + "|" + actor.Country + "|" + postID + "|" + actor.Subject + "|" + channel
	replayed := service.shares[shareKey]
	if !replayed {
		service.shares[shareKey] = true
		value.ShareCount++
		value.Revision++
		value.UpdatedAt = service.clock().UTC()
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return service.presentPostLocked(actor, value), replayed, nil
}

func (service *Service) setEngagement(actor Actor, key, postID string, revision int64, kind string, active bool) (Post, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(postID) || revision < 1 {
		return Post{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.posts[postID]
	if value == nil || value.Status != PostPublished || !service.canViewPostLocked(actor, value) {
		return Post{}, false, ErrNotFound
	}
	fingerprint := digest(EngagementRequest{Active: active})
	scope := idempotencyScope(actor, kind+":"+postID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Post{}, false, ErrIdempotencyConflict
		}
		return service.presentPostLocked(actor, value), true, nil
	}
	if value.Revision != revision {
		return Post{}, false, ErrConflict
	}
	target := value.likes
	if kind == "save" {
		target = value.saves
	}
	if target[actor.Subject] != active {
		if active {
			target[actor.Subject] = true
		} else {
			delete(target, actor.Subject)
		}
		value.Revision++
		value.UpdatedAt = service.clock().UTC()
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return service.presentPostLocked(actor, value), false, nil
}

func validShareChannel(value string) bool {
	return value == "IN_APP" || value == "LINK" || value == "EXTERNAL"
}

func (service *Service) Comments(actor Actor, postID string) ([]Comment, error) {
	if !validCustomer(actor) || !safeID(postID) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	post := service.posts[postID]
	if post == nil || post.Status != PostPublished || !service.canViewPostLocked(actor, post) {
		return nil, ErrNotFound
	}
	values := []Comment{}
	for _, value := range service.comments {
		if value.PostID == postID && value.Status == "PUBLISHED" {
			clone := cloneComment(*value)
			clone.Author = service.presentProfileLocked(actor.Subject, service.profiles[value.Author.ID])
			clone.AllowedActions = []string{"REPORT"}
			if value.Author.ID == actor.Subject {
				clone.AllowedActions = []string{"DELETE"}
			}
			values = append(values, clone)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.Before(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) CreateComment(actor Actor, key, postID string, request CreateCommentRequest) (Comment, bool, error) {
	body := strings.TrimSpace(request.Body)
	if !validCustomer(actor) || !validKey(key) || !safeID(postID) || len([]rune(body)) < 1 || len([]rune(body)) > 1000 {
		return Comment{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	post := service.posts[postID]
	if post == nil || post.Status != PostPublished || !service.canViewPostLocked(actor, post) {
		return Comment{}, false, ErrNotFound
	}
	profile := service.profiles[actor.Subject]
	if profile == nil {
		return Comment{}, false, ErrForbidden
	}
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "comment:"+postID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Comment{}, false, ErrIdempotencyConflict
		}
		return cloneComment(*service.comments[previous.resourceID]), true, nil
	}
	depth := 0
	if request.ParentID != "" {
		parent := service.comments[request.ParentID]
		if parent == nil || parent.PostID != postID || parent.Status != "PUBLISHED" || parent.Depth >= 2 {
			return Comment{}, false, ErrInvalidRequest
		}
		depth = parent.Depth + 1
	}
	service.sequence++
	value := &Comment{
		ID: "social-comment-" + sequenceID(service.sequence), PostID: postID, ParentID: request.ParentID, Depth: depth,
		Author: cloneProfile(*profile), Body: body, Mentions: extract(body, mentionPattern), Status: "PUBLISHED",
		AllowedActions: []string{"DELETE"}, CreatedAt: service.clock().UTC(), tenantID: actor.TenantID, country: actor.Country,
	}
	service.comments[value.ID] = value
	post.CommentCount++
	post.Revision++
	post.UpdatedAt = service.clock().UTC()
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneComment(*value), false, nil
}

func (service *Service) Follow(actor Actor, key, profileID string) (Follow, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(profileID) || profileID == actor.Subject {
		return Follow{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	target := service.profiles[profileID]
	if target == nil || target.tenantID != actor.TenantID || target.country != actor.Country || service.isBlockedLocked(actor.Subject, profileID) {
		return Follow{}, false, ErrNotFound
	}
	fingerprint := digest(profileID)
	scope := idempotencyScope(actor, "follow:"+profileID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Follow{}, false, ErrIdempotencyConflict
		}
		return *service.follows[previous.resourceID], true, nil
	}
	pair := relationKey(actor.Subject, profileID)
	if existing := service.follows[pair]; existing != nil {
		return *existing, true, nil
	}
	now := service.clock().UTC()
	status := "ACCEPTED"
	if target.Private {
		status = "PENDING"
	}
	value := &Follow{ID: "social-follow-" + sequenceID(service.next()), FollowerID: actor.Subject, FollowingID: profileID, Status: status, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.follows[pair] = value
	if status == "ACCEPTED" {
		service.incrementFollowCountsLocked(value, 1)
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: pair}
	return *value, false, nil
}

func (service *Service) AcceptFollow(actor Actor, key, followerID string) (Follow, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(followerID) || followerID == actor.Subject {
		return Follow{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	pair := relationKey(followerID, actor.Subject)
	value := service.follows[pair]
	if value == nil || value.Status != "PENDING" || service.isBlockedLocked(actor.Subject, followerID) {
		return Follow{}, false, ErrNotFound
	}
	fingerprint := digest(followerID)
	scope := idempotencyScope(actor, "accept-follow:"+followerID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Follow{}, false, ErrIdempotencyConflict
		}
		return *value, true, nil
	}
	value.Status, value.UpdatedAt = "ACCEPTED", service.clock().UTC()
	service.incrementFollowCountsLocked(value, 1)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: pair}
	return *value, false, nil
}

func (service *Service) SetRelationship(actor Actor, key, profileID, action string) (Profile, bool, error) {
	action = strings.ToUpper(strings.TrimSpace(action))
	if !validCustomer(actor) || !validKey(key) || !safeID(profileID) || profileID == actor.Subject || (action != "BLOCK" && action != "MUTE" && action != "NONE") {
		return Profile{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	target := service.profiles[profileID]
	if target == nil || target.tenantID != actor.TenantID || target.country != actor.Country {
		return Profile{}, false, ErrNotFound
	}
	fingerprint := digest(RelationshipRequest{Action: action})
	scope := idempotencyScope(actor, "relationship:"+profileID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Profile{}, false, ErrIdempotencyConflict
		}
		return service.presentProfileLocked(actor.Subject, target), true, nil
	}
	pair := relationKey(actor.Subject, profileID)
	delete(service.blocked, pair)
	delete(service.muted, pair)
	switch action {
	case "BLOCK":
		service.blocked[pair] = true
		service.removeFollowLocked(relationKey(actor.Subject, profileID))
		service.removeFollowLocked(relationKey(profileID, actor.Subject))
	case "MUTE":
		service.muted[pair] = true
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: profileID}
	return service.presentProfileLocked(actor.Subject, target), false, nil
}

func (service *Service) ReportPost(actor Actor, key, postID string, request ReportRequest) (Report, bool, error) {
	reason, details := strings.ToUpper(strings.TrimSpace(request.Reason)), strings.TrimSpace(request.Details)
	if !validCustomer(actor) || !validKey(key) || !safeID(postID) || !reportReasons[reason] || len([]rune(details)) < 8 || len([]rune(details)) > 1000 {
		return Report{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	post := service.posts[postID]
	if post == nil || post.Status != PostPublished || !service.canViewPostLocked(actor, post) || post.Author.ID == actor.Subject {
		return Report{}, false, ErrNotFound
	}
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "report:"+postID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Report{}, false, ErrIdempotencyConflict
		}
		return cloneReport(*service.reports[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	value := &Report{ID: "social-report-" + sequenceID(service.next()), PostID: postID, ReporterID: actor.Subject, Reason: reason, Details: details, Status: "OPEN", CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.reports[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneReport(*value), false, nil
}

func (service *Service) ModerationQueue(actor Actor) ([]Report, error) {
	if !validModerator(actor) {
		if !actor.MFAVerified {
			return nil, ErrMFARequired
		}
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []Report{}
	for _, value := range service.reports {
		if value.tenantID == actor.TenantID && value.country == actor.Country && value.Status == "OPEN" {
			values = append(values, cloneReport(*value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.Before(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) Moderate(actor Actor, key, reportID string, request ModerationDecisionRequest) (Report, bool, error) {
	if !actor.MFAVerified {
		return Report{}, false, ErrMFARequired
	}
	decision, note := strings.ToUpper(strings.TrimSpace(request.Decision)), strings.TrimSpace(request.Note)
	if !validModerator(actor) || !validKey(key) || !safeID(reportID) || (decision != "REMOVE" && decision != "DISMISS") || len([]rune(note)) < 8 || len([]rune(note)) > 1000 {
		return Report{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.reports[reportID]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country || value.Status != "OPEN" {
		return Report{}, false, ErrNotFound
	}
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "moderate:"+reportID, key)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Report{}, false, ErrIdempotencyConflict
		}
		return cloneReport(*value), true, nil
	}
	value.Status, value.Decision, value.DecisionNote, value.UpdatedAt = "DECIDED", decision, note, service.clock().UTC()
	if decision == "REMOVE" {
		post := service.posts[value.PostID]
		post.Status, post.ModerationReason, post.Revision, post.UpdatedAt = PostRemoved, "USER_REPORT_CONFIRMED", post.Revision+1, service.clock().UTC()
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneReport(*value), false, nil
}

func (service *Service) canViewPostLocked(actor Actor, value *Post) bool {
	if value.tenantID != actor.TenantID || value.country != actor.Country || service.isBlockedLocked(actor.Subject, value.Author.ID) {
		return false
	}
	profile := service.profiles[value.Author.ID]
	return value.Author.ID == actor.Subject || !profile.Private || service.followAcceptedLocked(actor.Subject, value.Author.ID)
}

func (service *Service) presentPostLocked(actor Actor, value *Post) Post {
	clone := clonePost(*value)
	clone.Author = service.presentProfileLocked(actor.Subject, service.profiles[value.Author.ID])
	clone.LikeCount, clone.CommentCount = len(value.likes), value.CommentCount
	clone.Liked, clone.Saved = value.likes[actor.Subject], value.saves[actor.Subject]
	clone.AllowedActions = []string{"LIKE", "SAVE", "SHARE", "COMMENT", "REPORT"}
	if value.Author.ID == actor.Subject {
		clone.AllowedActions = []string{"EDIT", "DELETE"}
	}
	return clone
}

func (service *Service) presentProfileLocked(viewer string, value *Profile) Profile {
	clone := cloneProfile(*value)
	clone.AllowedActions = []string{"FOLLOW", "MUTE", "BLOCK"}
	clone.Relationship = "NONE"
	if viewer == value.ID {
		clone.Relationship, clone.AllowedActions = "SELF", []string{"EDIT", "PRIVACY"}
	} else if service.blocked[relationKey(viewer, value.ID)] {
		clone.Relationship, clone.AllowedActions = "BLOCKED", []string{"UNBLOCK"}
	} else if service.muted[relationKey(viewer, value.ID)] {
		clone.Relationship, clone.AllowedActions = "MUTED", []string{"UNMUTE", "BLOCK"}
	} else if follow := service.follows[relationKey(viewer, value.ID)]; follow != nil {
		clone.Relationship = follow.Status
	}
	return clone
}

func (service *Service) followAcceptedLocked(follower, following string) bool {
	value := service.follows[relationKey(follower, following)]
	return value != nil && value.Status == "ACCEPTED"
}

func (service *Service) isBlockedLocked(left, right string) bool {
	return service.blocked[relationKey(left, right)] || service.blocked[relationKey(right, left)]
}

func (service *Service) incrementFollowCountsLocked(value *Follow, amount int) {
	service.profiles[value.FollowerID].FollowingCount += amount
	service.profiles[value.FollowingID].FollowerCount += amount
}

func (service *Service) removeFollowLocked(pair string) {
	value := service.follows[pair]
	if value == nil {
		return
	}
	if value.Status == "ACCEPTED" {
		service.incrementFollowCountsLocked(value, -1)
	}
	delete(service.follows, pair)
}

func (service *Service) encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", service.configuration.RankingModel, offset)))
}

func (service *Service) decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) > 128 {
		return 0, ErrInvalidRequest
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 || parts[0] != service.configuration.RankingModel {
		return 0, ErrInvalidRequest
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return 0, ErrInvalidRequest
	}
	return offset, nil
}

func (service *Service) next() int64 {
	service.sequence++
	return service.sequence
}

func feedScore(value Post) int {
	score := value.LikeCount*10 + value.CommentCount*15
	if value.Sponsored {
		score += 5
	}
	return score
}

func validPostRequest(value CreatePostRequest) bool {
	bodyLength := len([]rune(strings.TrimSpace(value.Body)))
	if bodyLength > 5000 || len(value.MediaAssetIDs) > 10 || (bodyLength == 0 && len(value.MediaAssetIDs) == 0) || (value.ProductStickerID != "" && !safeID(value.ProductStickerID)) {
		return false
	}
	seen := map[string]bool{}
	for _, assetID := range value.MediaAssetIDs {
		if !safeID(assetID) || seen[assetID] {
			return false
		}
		seen[assetID] = true
	}
	return true
}

func validCustomer(actor Actor) bool {
	return validActor(actor) && hasRole(actor, "CUSTOMER")
}

func validModerator(actor Actor) bool {
	return validActor(actor) && actor.MFAVerified && (hasRole(actor, "MODERATOR") || hasRole(actor, "CONTENT_ADMIN") || hasRole(actor, "SUPER_ADMIN"))
}

func validActor(actor Actor) bool {
	return safeID(actor.TenantID) && countryPattern.MatchString(actor.Country) && safeID(actor.Subject) && len(actor.Roles) > 0
}

func hasRole(actor Actor, expected string) bool {
	for _, role := range actor.Roles {
		if strings.EqualFold(strings.TrimSpace(role), expected) {
			return true
		}
	}
	return false
}

func validKey(value string) bool { return len(value) >= 8 && len(value) <= 128 && safeID(value) }

func safeID(value string) bool { return safeIDPattern.MatchString(value) }

func relationKey(left, right string) string { return left + "\x00" + right }

func idempotencyScope(actor Actor, operation, key string) string {
	return actor.TenantID + "\x00" + actor.Country + "\x00" + actor.Subject + "\x00" + operation + "\x00" + key
}

func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func extract(value string, pattern *regexp.Regexp) []string {
	matches, seen := pattern.FindAllStringSubmatch(value, -1), map[string]bool{}
	values := []string{}
	for _, match := range matches {
		item := strings.ToLower(match[1])
		if !seen[item] {
			seen[item] = true
			values = append(values, item)
		}
	}
	sort.Strings(values)
	return values
}

func sequenceID(value int64) string { return fmt.Sprintf("%06d", value) }

func cloneProfile(value Profile) Profile {
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}

func clonePost(value Post) Post {
	value.Author = cloneProfile(value.Author)
	value.MediaAssetIDs, value.Hashtags, value.Mentions = append([]string(nil), value.MediaAssetIDs...), append([]string(nil), value.Hashtags...), append([]string(nil), value.Mentions...)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	value.likes, value.saves = nil, nil
	return value
}

func cloneComment(value Comment) Comment {
	value.Author = cloneProfile(value.Author)
	value.Mentions, value.AllowedActions = append([]string(nil), value.Mentions...), append([]string(nil), value.AllowedActions...)
	return value
}

func cloneReport(value Report) Report { return value }

var (
	safeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	handlePattern  = regexp.MustCompile(`^[a-z0-9_]{3,30}$`)
	hashtagPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])#([A-Za-z0-9_]{1,64})`)
	mentionPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])@([A-Za-z0-9_]{3,30})`)
	reportReasons  = map[string]bool{"SPAM": true, "HARASSMENT": true, "HATE": true, "VIOLENCE": true, "NUDITY": true, "MISINFORMATION": true, "OTHER": true}
)
