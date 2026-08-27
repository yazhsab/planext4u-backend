package configcms

import "time"

type Platform string

const (
	PlatformAndroid Platform = "ANDROID"
	PlatformIOS     Platform = "IOS"
)

type UpdateGate string

const (
	UpdateNone     UpdateGate = "NONE"
	UpdateOptional UpdateGate = "OPTIONAL"
	UpdateRequired UpdateGate = "REQUIRED"
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
	MaintenanceWindow *MaintenanceWindow  `json:"maintenance_window,omitempty"`
}

type Bootstrap struct {
	Revision         int64           `json:"revision"`
	PublishedAt      time.Time       `json:"published_at"`
	UpdateGate       UpdateGate      `json:"update_gate"`
	LatestVersion    string          `json:"latest_version"`
	Maintenance      bool            `json:"maintenance"`
	MaintenanceUntil *time.Time      `json:"maintenance_until,omitempty"`
	MaintenanceText  string          `json:"maintenance_text,omitempty"`
	Locale           string          `json:"locale"`
	SupportedLocales []string        `json:"supported_locales"`
	ConsentPolicies  []ConsentPolicy `json:"consent_policies"`
	Flags            map[string]bool `json:"flags"`
	HomeSections     []HomeSection   `json:"home_sections"`
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
