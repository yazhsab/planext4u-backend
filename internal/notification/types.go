package notification

import "time"

type Channel string

const (
	ChannelEmail    Channel = "EMAIL"
	ChannelPush     Channel = "PUSH"
	ChannelWhatsApp Channel = "WHATSAPP"
	ChannelInApp    Channel = "IN_APP"
)

type Purpose string

const (
	PurposeSecurity      Purpose = "SECURITY"
	PurposeTransactional Purpose = "TRANSACTIONAL"
	PurposeMarketing     Purpose = "MARKETING"
)

type TemplateStatus string

const (
	TemplateDraft     TemplateStatus = "DRAFT"
	TemplatePublished TemplateStatus = "PUBLISHED"
)

type DeliveryStatus string

const (
	DeliveryQueued     DeliveryStatus = "QUEUED"
	DeliverySending    DeliveryStatus = "SENDING"
	DeliverySent       DeliveryStatus = "SENT"
	DeliveryDelivered  DeliveryStatus = "DELIVERED"
	DeliveryFailed     DeliveryStatus = "FAILED"
	DeliverySuppressed DeliveryStatus = "SUPPRESSED"
)

type Preference struct {
	TenantID  string    `json:"tenant_id"`
	SubjectID string    `json:"subject_id"`
	Purpose   Purpose   `json:"purpose"`
	Channel   Channel   `json:"channel"`
	Enabled   bool      `json:"enabled"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Consent struct {
	TenantID  string    `json:"tenant_id"`
	SubjectID string    `json:"subject_id"`
	Purpose   Purpose   `json:"purpose"`
	Granted   bool      `json:"granted"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Template struct {
	TenantID    string         `json:"tenant_id"`
	Country     string         `json:"country"`
	Key         string         `json:"key"`
	Version     int64          `json:"version"`
	Locale      string         `json:"locale"`
	Channel     Channel        `json:"channel"`
	Status      TemplateStatus `json:"status"`
	Subject     string         `json:"subject,omitempty"`
	Body        string         `json:"body"`
	Variables   []string       `json:"variables"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
}

type QueueCommand struct {
	TenantID        string            `json:"tenant_id"`
	Country         string            `json:"country"`
	SubjectID       string            `json:"subject_id"`
	RecipientRef    string            `json:"recipient_ref"`
	Channel         Channel           `json:"channel"`
	Purpose         Purpose           `json:"purpose"`
	TemplateKey     string            `json:"template_key"`
	TemplateVersion int64             `json:"template_version"`
	Locale          string            `json:"locale"`
	Variables       map[string]string `json:"variables"`
	Data            map[string]string `json:"data,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key"`
	CorrelationID   string            `json:"correlation_id"`
	CausationID     string            `json:"causation_id"`
	Traceparent     string            `json:"traceparent"`
}

type Delivery struct {
	ID                string            `json:"id"`
	TenantID          string            `json:"tenant_id"`
	Country           string            `json:"country"`
	SubjectID         string            `json:"subject_id"`
	RecipientRef      string            `json:"recipient_ref"`
	Channel           Channel           `json:"channel"`
	Purpose           Purpose           `json:"purpose"`
	TemplateKey       string            `json:"template_key"`
	TemplateVersion   int64             `json:"template_version"`
	Locale            string            `json:"locale"`
	RenderedSubject   string            `json:"rendered_subject,omitempty"`
	RenderedBody      string            `json:"rendered_body"`
	Data              map[string]string `json:"data,omitempty"`
	Status            DeliveryStatus    `json:"status"`
	SuppressionReason string            `json:"suppression_reason,omitempty"`
	ProviderMessageID string            `json:"provider_message_id,omitempty"`
	LastErrorCode     string            `json:"last_error_code,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

type ProviderMessage struct {
	DeliveryID   string
	TenantID     string
	RecipientRef string
	Subject      string
	Body         string
	Data         map[string]string
}

type ProviderReceipt struct {
	ReceiptID         string         `json:"receipt_id"`
	ProviderMessageID string         `json:"provider_message_id"`
	Status            DeliveryStatus `json:"status"`
	ErrorCode         string         `json:"error_code,omitempty"`
	OccurredAt        time.Time      `json:"occurred_at"`
}

type ProviderError struct {
	Code      string
	Retryable bool
}

type DevicePlatform string

const (
	DeviceAndroid DevicePlatform = "ANDROID"
	DeviceIOS     DevicePlatform = "IOS"
)

type DeviceEndpoint struct {
	ID              string         `json:"id"`
	TenantID        string         `json:"-"`
	Country         string         `json:"country"`
	SubjectID       string         `json:"-"`
	DeviceReference string         `json:"device_reference"`
	Platform        DevicePlatform `json:"platform"`
	Locale          string         `json:"locale"`
	Enabled         bool           `json:"enabled"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Token           string         `json:"-"`
}

func (err *ProviderError) Error() string { return err.Code }
