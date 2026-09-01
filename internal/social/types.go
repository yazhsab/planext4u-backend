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
	TenantID        string
	Country         string
	Subject         string
	DeviceReference string
	Roles           []string
	MFAVerified     bool
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
	ShareCount       int        `json:"share_count"`
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

type ShareRequest struct {
	Channel string `json:"channel"`
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

type MediaState string

const (
	MediaQuarantined MediaState = "QUARANTINED"
	MediaReady       MediaState = "READY"
	MediaRejected    MediaState = "REJECTED"
	MediaTombstoned  MediaState = "TOMBSTONED"
)

type MediaJob struct {
	ID              string     `json:"id"`
	OwnerID         string     `json:"owner_id"`
	AssetID         string     `json:"asset_id"`
	Kind            string     `json:"kind"`
	State           MediaState `json:"state"`
	ScanStatus      string     `json:"scan_status"`
	BlurStatus      string     `json:"blur_status"`
	TranscodeStatus string     `json:"transcode_status"`
	ModeratedBy     string     `json:"moderated_by,omitempty"`
	AppealStatus    string     `json:"appeal_status,omitempty"`
	RetentionUntil  time.Time  `json:"retention_until"`
	CreatedAt       time.Time  `json:"created_at"`
	tenantID        string
	country         string
}

type EphemeralContent struct {
	ID             string    `json:"id"`
	Author         Profile   `json:"author"`
	Kind           string    `json:"kind"`
	MediaJobID     string    `json:"media_job_id"`
	Caption        string    `json:"caption"`
	Status         string    `json:"status"`
	Highlighted    bool      `json:"highlighted"`
	ExpiresAt      time.Time `json:"expires_at"`
	AllowedActions []string  `json:"allowed_actions"`
	CreatedAt      time.Time `json:"created_at"`
	tenantID       string
	country        string
}

type Collection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PostIDs   []string  `json:"post_ids"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ownerID   string
	tenantID  string
	country   string
}

type Conversation struct {
	ID             string    `json:"id"`
	ParticipantIDs []string  `json:"participant_ids"`
	Status         string    `json:"status"`
	RequestedBy    string    `json:"requested_by"`
	AllowedActions []string  `json:"allowed_actions"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	tenantID       string
	country        string
}

type DirectMessage struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	SenderID       string    `json:"sender_id"`
	Body           string    `json:"body,omitempty"`
	VoiceMediaID   string    `json:"voice_media_id,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	tenantID       string
	country        string
}

type Presence struct {
	ProfileID string    `json:"profile_id"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CallSession struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	InitiatorID    string    `json:"initiator_id"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	SignalCount    int       `json:"signal_count"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
	tenantID       string
	country        string
}

type CreateMediaRequest struct {
	AssetID string `json:"asset_id"`
	Kind    string `json:"kind"`
}

type ProcessMediaRequest struct {
	Clean bool `json:"clean"`
}

type AppealDecisionRequest struct {
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

type CreateEphemeralRequest struct {
	Kind       string `json:"kind"`
	MediaJobID string `json:"media_job_id"`
	Caption    string `json:"caption"`
}

type HighlightRequest struct {
	Active bool `json:"active"`
}

type CreateCollectionRequest struct {
	Name string `json:"name"`
}

type CollectionPostRequest struct {
	PostID string `json:"post_id"`
	Active bool   `json:"active"`
}

type CreateConversationRequest struct {
	ProfileID string `json:"profile_id"`
}

type SendMessageRequest struct {
	Body         string `json:"body"`
	VoiceMediaID string `json:"voice_media_id,omitempty"`
}

type PresenceRequest struct {
	State string `json:"state"`
}

type CreateCallRequest struct {
	Kind string `json:"kind"`
}

type SignalRequest struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}
