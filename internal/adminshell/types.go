package adminshell

import "time"

type Role string

const (
	RoleSuperAdmin   Role = "SUPER_ADMIN"
	RoleCountryAdmin Role = "COUNTRY_ADMIN"
	RoleContentAdmin Role = "CONTENT_ADMIN"
	RoleSupportAdmin Role = "SUPPORT_ADMIN"
	RoleAuditor      Role = "AUDITOR"
)

const (
	CapabilityShellRead      = "admin.shell.read"
	CapabilityAuditRead      = "admin.audit.read"
	CapabilityOperationsRead = "admin.operations.read"
	CapabilityContentManage  = "admin.content.manage"
	CapabilitySupportManage  = "admin.support.manage"
	CapabilityConfigManage   = "admin.config.manage"
	CapabilityGovernanceRead = "admin.governance.read"
)

type Principal struct {
	SubjectID        string
	SessionID        string
	TenantID         string
	DisplayName      string
	Roles            []Role
	AllowedCountries []string
	SelectedCountry  string
	AuthenticatedAt  time.Time
	AuthMethods      []string
}

type ResolvedSession struct {
	Principal Principal
	CSRFToken string
}

type Assurance struct {
	MFASatisfied bool      `json:"mfa_satisfied"`
	FreshAuth    bool      `json:"fresh_auth"`
	AuthTime     time.Time `json:"auth_time"`
}

type NavigationItem struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Path       string `json:"path"`
	Capability string `json:"capability"`
}

type SessionView struct {
	SubjectID        string           `json:"subject_id"`
	DisplayName      string           `json:"display_name"`
	Roles            []Role           `json:"roles"`
	Capabilities     []string         `json:"capabilities"`
	AllowedCountries []string         `json:"allowed_countries"`
	SelectedCountry  string           `json:"selected_country"`
	Assurance        Assurance        `json:"assurance"`
	Navigation       []NavigationItem `json:"navigation"`
	CSRFToken        string           `json:"csrf_token"`
}

type GovernanceMetric struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Value     int64     `json:"value"`
	Unit      string    `json:"unit"`
	Freshness time.Time `json:"freshness"`
	Masked    bool      `json:"masked"`
}

type GovernanceView struct {
	Country       string             `json:"country"`
	PolicyVersion string             `json:"policy_version"`
	FeatureFlags  map[string]bool    `json:"feature_flags"`
	Metrics       []GovernanceMetric `json:"metrics"`
	PrivacyMode   string             `json:"privacy_mode"`
	GeneratedAt   time.Time          `json:"generated_at"`
}
