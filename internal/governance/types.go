package governance

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest = errors.New("invalid governance request")
	ErrForbidden      = errors.New("governance access forbidden")
	ErrMFARequired    = errors.New("governance access requires mfa")
)

type Actor struct {
	TenantID    string
	Country     string
	Subject     string
	Roles       []string
	MFAVerified bool
}

type ReportCard struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Domain       string    `json:"domain"`
	Metric       string    `json:"metric"`
	Value        int64     `json:"value"`
	Unit         string    `json:"unit"`
	Freshness    time.Time `json:"freshness"`
	Masked       bool      `json:"masked"`
	ExportPolicy string    `json:"export_policy"`
}

type MapCell struct {
	RegionCode string `json:"region_code"`
	Label      string `json:"label"`
	Count      int64  `json:"count"`
	Intensity  int    `json:"intensity"`
	Precision  string `json:"precision"`
}

type LeaderboardEntry struct {
	Rank      int    `json:"rank"`
	Label     string `json:"label"`
	Score     int64  `json:"score"`
	Badge     string `json:"badge"`
	PIIMasked bool   `json:"pii_masked"`
}

type Insight struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	Confidence  string    `json:"confidence"`
	Evidence    []string  `json:"evidence"`
	GeneratedAt time.Time `json:"generated_at"`
}

type CountryControl struct {
	Country       string          `json:"country"`
	Currency      string          `json:"currency"`
	Locales       []string        `json:"locales"`
	FeatureFlags  map[string]bool `json:"feature_flags"`
	PolicyVersion string          `json:"policy_version"`
}

type Dashboard struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Reports     []ReportCard       `json:"reports"`
	MapCells    []MapCell          `json:"map_cells"`
	Leaderboard []LeaderboardEntry `json:"leaderboard"`
	Insights    []Insight          `json:"insights"`
	Countries   []CountryControl   `json:"countries"`
	PrivacyMode string             `json:"privacy_mode"`
}

type Configuration struct {
	TenantID    string
	Countries   []CountryControl
	Reports     []ReportCard
	MapCells    []MapCell
	Leaderboard []LeaderboardEntry
	Insights    []Insight
}
