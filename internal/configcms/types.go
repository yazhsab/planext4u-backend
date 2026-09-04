package configcms

import "time"

type Platform string

const (
	PlatformAndroid Platform = "ANDROID"
	PlatformIOS     Platform = "IOS"
	PlatformWeb     Platform = "WEB"
)

type UpdateGate string

const (
	UpdateNone     UpdateGate = "NONE"
	UpdateOptional UpdateGate = "OPTIONAL"
	UpdateRequired UpdateGate = "REQUIRED"
)

type UpdateAction string

const (
	UpdateActionNone        UpdateAction = "NONE"
	UpdateActionStoreUpdate UpdateAction = "STORE_UPDATE"
	UpdateActionReload      UpdateAction = "RELOAD"
)

type ConsentPolicy struct {
	Purpose       string `json:"purpose"`
	PolicyVersion string `json:"policy_version"`
	Required      bool   `json:"required"`
}

type MaintenanceWindow struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Message  string    `json:"message"`
}

type HomeSection struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	TitleKey string `json:"title_key"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
}

type PageBlock struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	TitleKey string         `json:"title_key,omitempty"`
	Enabled  bool           `json:"enabled"`
	Priority int            `json:"priority"`
	Content  map[string]any `json:"content"`
}

type Page struct {
	ID       string      `json:"id"`
	Route    string      `json:"route"`
	TitleKey string      `json:"title_key"`
	Audience []string    `json:"audience"`
	Enabled  bool        `json:"enabled"`
	Blocks   []PageBlock `json:"blocks"`
}

type Snapshot struct {
	TenantID          string              `json:"tenant_id"`
	Country           string              `json:"country"`
	Revision          int64               `json:"revision"`
	PublishedAt       time.Time           `json:"published_at"`
	MinimumVersions   map[Platform]string `json:"minimum_versions"`
	LatestVersions    map[Platform]string `json:"latest_versions"`
	SupportedLocales  []string            `json:"supported_locales"`
	DefaultLocale     string              `json:"default_locale"`
	ConsentPolicies   []ConsentPolicy     `json:"consent_policies"`
	Flags             map[string]bool     `json:"flags"`
	HomeSections      []HomeSection       `json:"home_sections"`
	Pages             []Page              `json:"pages"`
	MaintenanceWindow *MaintenanceWindow  `json:"maintenance_window,omitempty"`
}

type Bootstrap struct {
	Revision           int64           `json:"revision"`
	PublishedAt        time.Time       `json:"published_at"`
	Platform           Platform        `json:"platform"`
	ClientVersion      string          `json:"client_version"`
	UpdateGate         UpdateGate      `json:"update_gate"`
	UpdateAction       UpdateAction    `json:"update_action"`
	LatestVersion      string          `json:"latest_version"`
	ClientDeploymentID string          `json:"client_deployment_id,omitempty"`
	LatestDeploymentID string          `json:"latest_deployment_id,omitempty"`
	Maintenance        bool            `json:"maintenance"`
	MaintenanceUntil   *time.Time      `json:"maintenance_until,omitempty"`
	MaintenanceText    string          `json:"maintenance_text,omitempty"`
	Locale             string          `json:"locale"`
	SupportedLocales   []string        `json:"supported_locales"`
	ConsentPolicies    []ConsentPolicy `json:"consent_policies"`
	Flags              map[string]bool `json:"flags"`
	HomeSections       []HomeSection   `json:"home_sections"`
	Pages              []Page          `json:"pages"`
}

type PageDocument struct {
	Revision    int64     `json:"revision"`
	PublishedAt time.Time `json:"published_at"`
	Locale      string    `json:"locale"`
	Page        Page      `json:"page"`
}

type PageDraft struct {
	TenantID  string    `json:"tenant_id"`
	Country   string    `json:"country"`
	Page      Page      `json:"page"`
	Revision  int64     `json:"revision"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Workspace struct {
	MinimumVersions   map[Platform]string `json:"minimum_versions"`
	LatestVersions    map[Platform]string `json:"latest_versions"`
	SupportedLocales  []string            `json:"supported_locales"`
	DefaultLocale     string              `json:"default_locale"`
	ConsentPolicies   []ConsentPolicy     `json:"consent_policies"`
	Flags             map[string]bool     `json:"flags"`
	HomeSections      []HomeSection       `json:"home_sections"`
	MaintenanceWindow *MaintenanceWindow  `json:"maintenance_window,omitempty"`
}

type WorkspaceDraft struct {
	TenantID  string    `json:"tenant_id"`
	Country   string    `json:"country"`
	Workspace Workspace `json:"workspace"`
	Revision  int64     `json:"revision"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AuditRecord struct {
	EventID          string    `json:"event_id"`
	ActorID          string    `json:"actor_id"`
	TenantID         string    `json:"tenant_id"`
	Country          string    `json:"country"`
	PreviousRevision int64     `json:"previous_revision"`
	NewRevision      int64     `json:"new_revision"`
	Reason           string    `json:"reason"`
	RecordedAt       time.Time `json:"recorded_at"`
}
