package media

import "time"

type State string

const (
	StatePendingUpload State = "PENDING_UPLOAD"
	StatePendingScan   State = "PENDING_SCAN"
	StateReady         State = "READY"
	StateRejected      State = "REJECTED"
	StateExpired       State = "EXPIRED"
	StateDeleted       State = "DELETED"
)

type Purpose string

const (
	PurposeAvatar           Purpose = "AVATAR"
	PurposeCatalogImage     Purpose = "CATALOG_IMAGE"
	PurposeCompletionProof  Purpose = "COMPLETION_PROOF"
	PurposeIdentityDocument Purpose = "IDENTITY_DOCUMENT"
)

type Asset struct {
	ID              string     `json:"id"`
	Purpose         Purpose    `json:"purpose"`
	ContentType     string     `json:"content_type"`
	SizeBytes       int64      `json:"size_bytes"`
	SHA256          string     `json:"sha256"`
	State           State      `json:"state"`
	CreatedAt       time.Time  `json:"created_at"`
	UploadExpiresAt time.Time  `json:"upload_expires_at"`
	ReadyAt         *time.Time `json:"ready_at,omitempty"`
	RejectedCode    string     `json:"rejected_code,omitempty"`
	AltText         string     `json:"alt_text,omitempty"`
	Width           int        `json:"width,omitempty"`
	Height          int        `json:"height,omitempty"`
	Version         int64      `json:"version"`

	TenantID  string `json:"-"`
	Country   string `json:"-"`
	OwnerID   string `json:"-"`
	ObjectKey string `json:"-"`
}

type PresignRequest struct {
	Purpose     Purpose `json:"purpose"`
	ContentType string  `json:"content_type"`
	SizeBytes   int64   `json:"size_bytes"`
	SHA256      string  `json:"sha256"`
	AltText     string  `json:"alt_text,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
}

type UploadGrant struct {
	Asset     Asset             `json:"asset"`
	UploadURL string            `json:"upload_url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type ObjectMetadata struct {
	ContentType string
	SizeBytes   int64
	SHA256      string
}

type ScanResult struct {
	Clean      bool   `json:"clean"`
	ReasonCode string `json:"reason_code"`
}

type ResponsiveVariant struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Presentation struct {
	AssetID     string              `json:"asset_id"`
	URL         string              `json:"url"`
	ContentType string              `json:"content_type"`
	Width       int                 `json:"width"`
	Height      int                 `json:"height"`
	AltText     string              `json:"alt_text"`
	Variants    []ResponsiveVariant `json:"variants"`
	ExpiresAt   *time.Time          `json:"expires_at"`
}

type PresentationRequest struct {
	AssetIDs []string `json:"asset_ids"`
}

type PresentationResponse struct {
	Items               []Presentation `json:"items"`
	UnavailableAssetIDs []string       `json:"unavailable_asset_ids"`
}
