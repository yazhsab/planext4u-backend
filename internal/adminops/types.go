package adminops

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest   = errors.New("invalid admin operation")
	ErrForbidden        = errors.New("admin operation forbidden")
	ErrFreshMFARequired = errors.New("fresh MFA is required")
	ErrFourEyesRequired = errors.New("a different approver is required")
	ErrChangeNotFound   = errors.New("admin change not found")
	ErrRevisionConflict = errors.New("admin change revision conflict")
	ErrInvalidState     = errors.New("invalid admin change state")
)

type Domain string

const (
	DomainCatalog   Domain = "CATALOG"
	DomainOrder     Domain = "ORDER"
	DomainPayment   Domain = "PAYMENT"
	DomainWallet    Domain = "WALLET"
	DomainCampaign  Domain = "CAMPAIGN"
	DomainSupport   Domain = "SUPPORT"
	DomainReporting Domain = "REPORTING"
)

const (
	ActionCatalogUpsert      = "UPSERT"
	ActionCatalogPublish     = "PUBLISH"
	ActionCatalogUnpublish   = "UNPUBLISH"
	ActionCatalogApprove     = "APPROVE"
	ActionCatalogReject      = "REJECT"
	ActionOrderCancel        = "CANCEL"
	ActionOrderOverride      = "STATUS_OVERRIDE"
	ActionOrderRefundApprove = "REFUND_APPROVE"
	ActionPaymentRefund      = "REFUND"
	ActionPaymentReconcile   = "RECONCILE"
	ActionPaymentCODCollect  = "MARK_COD_COLLECTED"
	ActionWalletAdjust       = "ADJUST"
	ActionWalletReverse      = "REVERSE"
	ActionWalletFreeze       = "FREEZE"
	ActionCampaignUpsert     = "UPSERT"
	ActionCampaignActivate   = "ACTIVATE"
	ActionCampaignPause      = "PAUSE"
	ActionSupportUpdate      = "UPDATE_CASE"
	ActionSupportEscalate    = "ESCALATE"
	ActionSupportResolve     = "RESOLVE"
	ActionReportingExport    = "EXPORT"
)

const (
	CapabilityCatalog   = "admin.catalog.manage"
	CapabilityOrder     = "admin.order.manage"
	CapabilityPayment   = "admin.payment.manage"
	CapabilityWallet    = "admin.wallet.manage"
	CapabilityCampaign  = "admin.campaign.manage"
	CapabilitySupport   = "admin.support.manage"
	CapabilityReporting = "admin.reporting.export"
)

type Principal struct {
	TenantID        string
	Country         string
	SubjectID       string
	Capabilities    map[string]bool
	AuthMethods     []string
	AuthenticatedAt time.Time
}

type Command struct {
	Domain        Domain         `json:"domain"`
	Action        string         `json:"action"`
	TargetID      string         `json:"target_id"`
	Reason        string         `json:"reason"`
	Payload       map[string]any `json:"payload"`
	CorrelationID string         `json:"correlation_id"`
}

type Risk string

const (
	RiskStandard Risk = "STANDARD"
	RiskHigh     Risk = "HIGH"
)

type Status string

const (
	StatusPending  Status = "PENDING_APPROVAL"
	StatusExecuted Status = "EXECUTED"
	StatusRejected Status = "REJECTED"
)

type Change struct {
	ID          string    `json:"id"`
	Revision    int64     `json:"revision"`
	TenantID    string    `json:"tenant_id"`
	Country     string    `json:"country"`
	Command     Command   `json:"command"`
	Risk        Risk      `json:"risk"`
	Status      Status    `json:"status"`
	RequestedBy string    `json:"requested_by"`
	ApprovedBy  string    `json:"approved_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AuditEvent struct {
	Sequence      int64     `json:"sequence"`
	TenantID      string    `json:"tenant_id"`
	Country       string    `json:"country"`
	ActorID       string    `json:"actor_id"`
	Action        string    `json:"action"`
	TargetID      string    `json:"target_id"`
	Outcome       string    `json:"outcome"`
	Reason        string    `json:"reason"`
	CorrelationID string    `json:"correlation_id"`
	CreatedAt     time.Time `json:"created_at"`
}
