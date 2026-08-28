package social

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid social request")
	ErrNotFound            = errors.New("social resource not found")
	ErrForbidden           = errors.New("social action forbidden")
	ErrConflict            = errors.New("social revision conflict")
	ErrIdempotencyConflict = errors.New("social idempotency conflict")
	ErrMFARequired         = errors.New("social moderation requires mfa")
)

type Actor struct {
	TenantID    string
	Country     string
	Subject     string
	Roles       []string
	MFAVerified bool
}

type Profile struct {
	ID             string   `json:"id"`
	Handle         string   `json:"handle"`
	DisplayName    string   `json:"display_name"`
	Bio            string   `json:"bio"`
	AvatarAssetID  string   `json:"avatar_asset_id,omitempty"`
	Private        bool     `json:"private"`
	Verified       bool     `json:"verified"`
	FollowerCount  int      `json:"follower_count"`
	FollowingCount int      `json:"following_count"`
	Relationship   string   `json:"relationship"`
	AllowedActions []string `json:"allowed_actions"`
	tenantID       string
	country        string
}

type PostStatus string

const (
	PostPublished     PostStatus = "PUBLISHED"
	PostPendingReview PostStatus = "PENDING_REVIEW"
	PostRemoved       PostStatus = "REMOVED"
)

type Post struct {
	ID               string     `json:"id"`
	Revision         int64      `json:"revision"`
	Author           Profile    `json:"author"`
	Body             string     `json:"body"`
	MediaAssetIDs    []string   `json:"media_asset_ids"`
	Hashtags         []string   `json:"hashtags"`
	Mentions         []string   `json:"mentions"`
	ProductStickerID string     `json:"product_sticker_id,omitempty"`
	Sponsored        bool       `json:"sponsored"`
	SponsorLabel     string     `json:"sponsor_label,omitempty"`
	Status           PostStatus `json:"status"`
	ModerationReason string     `json:"moderation_reason,omitempty"`
	LikeCount        int        `json:"like_count"`
	CommentCount     int        `json:"comment_count"`
	Liked            bool       `json:"liked"`
	Saved            bool       `json:"saved"`
	AllowedActions   []string   `json:"allowed_actions"`
	RankingVersion   string     `json:"ranking_version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	likes            map[string]bool
	saves            map[string]bool
	tenantID         string
	country          string
}

type Comment struct {
	ID             string    `json:"id"`
	PostID         string    `json:"post_id"`
	ParentID       string    `json:"parent_id,omitempty"`
	Depth          int       `json:"depth"`
	Author         Profile   `json:"author"`
	Body           string    `json:"body"`
	Mentions       []string  `json:"mentions"`
	Status         string    `json:"status"`
	AllowedActions []string  `json:"allowed_actions"`
	CreatedAt      time.Time `json:"created_at"`
	tenantID       string
	country        string
}

type Follow struct {
	ID          string    `json:"id"`
	FollowerID  string    `json:"follower_id"`
	FollowingID string    `json:"following_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	tenantID    string
	country     string
}

type Report struct {
	ID           string    `json:"id"`
	PostID       string    `json:"post_id"`
	ReporterID   string    `json:"reporter_id"`
	Reason       string    `json:"reason"`
	Details      string    `json:"details"`
	Status       string    `json:"status"`
	Decision     string    `json:"decision,omitempty"`
	DecisionNote string    `json:"decision_note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	tenantID     string
	country      string
}

type CreatePostRequest struct {
	Body             string   `json:"body"`
	MediaAssetIDs    []string `json:"media_asset_ids"`
	ProductStickerID string   `json:"product_sticker_id,omitempty"`
}

type EngagementRequest struct {
	Active bool `json:"active"`
}

type RelationshipRequest struct {
	Action string `json:"action"`
}

type CreateCommentRequest struct {
	ParentID string `json:"parent_id,omitempty"`
	Body     string `json:"body"`
}

type ReportRequest struct {
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

type ModerationDecisionRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

type FeedPage struct {
	Items          []Post `json:"items"`
	NextCursor     string `json:"next_cursor,omitempty"`
	RankingVersion string `json:"ranking_version"`
}

type Configuration struct {
	TenantID     string
	Country      string
	Profiles     []Profile
	Posts        []Post
	ReviewTerms  []string
	RankingModel string
}
