package adminshell

import (
	"context"
	"errors"
	"time"
)

var (
	ErrReportInvalid  = errors.New("invalid report request")
	ErrReportNotFound = errors.New("report not found")
	ErrExportNotFound = errors.New("report export not found")
	ErrExportExpired  = errors.New("report export expired")
	ErrExportToken    = errors.New("report export token invalid")
)

type ReportQuery struct {
	Domain string
	From   time.Time
	To     time.Time
	Cursor string
	Limit  int
}

type ReportSummary struct {
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

type ReportPage struct {
	Items      []ReportSummary `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type ReportRow struct {
	Label      string            `json:"label"`
	Dimensions map[string]string `json:"dimensions"`
	Value      int64             `json:"value"`
	Unit       string            `json:"unit"`
}

type ReportLineage struct {
	SourceProjection string    `json:"source_projection"`
	Aggregation      string    `json:"aggregation"`
	Freshness        time.Time `json:"freshness"`
	GeneratedAt      time.Time `json:"generated_at"`
}

type ReportDetail struct {
	Report     ReportSummary `json:"report"`
	Country    string        `json:"country"`
	From       *time.Time    `json:"from,omitempty"`
	To         *time.Time    `json:"to,omitempty"`
	Items      []ReportRow   `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Lineage    ReportLineage `json:"lineage"`
}

type ReportExport struct {
	ID             string    `json:"id"`
	ReportID       string    `json:"report_id"`
	Format         string    `json:"format"`
	Status         string    `json:"status"`
	ContentType    string    `json:"content_type,omitempty"`
	FileName       string    `json:"file_name,omitempty"`
	ChecksumSHA256 string    `json:"checksum_sha256,omitempty"`
	SizeBytes      int64     `json:"size_bytes,omitempty"`
	DownloadURL    string    `json:"download_url,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

type ReportArtifact struct {
	Bytes       []byte
	ContentType string
	FileName    string
}

type ReportService interface {
	List(context.Context, Principal, ReportQuery) (ReportPage, error)
	Detail(context.Context, Principal, string, ReportQuery) (ReportDetail, error)
	CreateExport(context.Context, Principal, string, ReportQuery, string) (ReportExport, error)
	Export(context.Context, Principal, string) (ReportExport, error)
	Download(context.Context, Principal, string, string) (ReportArtifact, error)
}
