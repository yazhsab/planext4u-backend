package adminshell

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const reportExportTTL = 10 * time.Minute

type PostgresReportService struct {
	pool   *pgxpool.Pool
	clock  func() time.Time
	secret []byte
}

func NewPostgresReportService(pool *pgxpool.Pool, clock func() time.Time, secret []byte) (*PostgresReportService, error) {
	if pool == nil || clock == nil || len(secret) < 32 || len(secret) > 1024 {
		return nil, ErrReportInvalid
	}
	derivation := hmac.New(sha256.New, secret)
	_, _ = derivation.Write([]byte("planext4u-report-download-v1"))
	return &PostgresReportService{pool: pool, clock: clock, secret: derivation.Sum(nil)}, nil
}

func (service *PostgresReportService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `SELECT to_regclass('governance.report_cards') IS NOT NULL AND to_regclass('admin.report_exports') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check report store readiness: %w", err)
	}
	if !ready {
		return errors.New("report store schema is unavailable")
	}
	return nil
}

func (service *PostgresReportService) List(ctx context.Context, principal Principal, query ReportQuery) (ReportPage, error) {
	query.Domain = strings.ToLower(strings.TrimSpace(query.Domain))
	if !validReportPrincipal(principal) || !validReportQuery(query) {
		return ReportPage{}, ErrReportInvalid
	}
	offset, err := decodeReportCursor(query.Cursor, "catalog:"+query.Domain)
	if err != nil {
		return ReportPage{}, err
	}
	rows, err := service.pool.Query(ctx, `SELECT id,title,domain,metric,value,unit,freshness FROM governance.report_cards WHERE tenant_id=$1 AND country=$2 AND published AND ($3='' OR domain=$3) ORDER BY domain,id OFFSET $4 LIMIT $5`, principal.TenantID, principal.SelectedCountry, query.Domain, offset, query.Limit+1)
	if err != nil {
		return ReportPage{}, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()
	items := make([]ReportSummary, 0, query.Limit+1)
	for rows.Next() {
		var item ReportSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.Domain, &item.Metric, &item.Value, &item.Unit, &item.Freshness); err != nil {
			return ReportPage{}, fmt.Errorf("scan report: %w", err)
		}
		item.Masked, item.ExportPolicy = true, "MFA_AND_AUDIT_REQUIRED"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ReportPage{}, err
	}
	page := ReportPage{Items: items}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		page.NextCursor = encodeReportCursor("catalog:"+query.Domain, offset+query.Limit)
	}
	return page, nil
}

func (service *PostgresReportService) Detail(ctx context.Context, principal Principal, reportID string, query ReportQuery) (ReportDetail, error) {
	if !validReportPrincipal(principal) || !safeIdentifier(reportID) || !validReportQuery(query) || !validReportRange(query.From, query.To) {
		return ReportDetail{}, ErrReportInvalid
	}
	scope := reportDetailScope(reportID, query)
	offset, err := decodeReportCursor(query.Cursor, scope)
	if err != nil {
		return ReportDetail{}, err
	}
	var report ReportSummary
	err = service.pool.QueryRow(ctx, `SELECT id,title,domain,metric,value,unit,freshness FROM governance.report_cards WHERE tenant_id=$1 AND country=$2 AND id=$3 AND published`, principal.TenantID, principal.SelectedCountry, reportID).Scan(&report.ID, &report.Title, &report.Domain, &report.Metric, &report.Value, &report.Unit, &report.Freshness)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportDetail{}, ErrReportNotFound
	}
	if err != nil {
		return ReportDetail{}, fmt.Errorf("load report: %w", err)
	}
	report.Masked, report.ExportPolicy = true, "MFA_AND_AUDIT_REQUIRED"
	detail := ReportDetail{
		Report:  report,
		Country: principal.SelectedCountry,
		Items:   []ReportRow{},
		Lineage: ReportLineage{SourceProjection: "governance.report_cards", Aggregation: "country_aggregate", Freshness: report.Freshness, GeneratedAt: service.clock().UTC()},
	}
	if !query.From.IsZero() {
		value := query.From.UTC()
		detail.From = &value
	}
	if !query.To.IsZero() {
		value := query.To.UTC()
		detail.To = &value
	}
	inRange := (query.From.IsZero() || !report.Freshness.Before(query.From)) && (query.To.IsZero() || !report.Freshness.After(query.To))
	if inRange && offset == 0 {
		detail.Items = append(detail.Items, ReportRow{Label: report.Title, Dimensions: map[string]string{"country": principal.SelectedCountry, "domain": report.Domain, "metric": report.Metric}, Value: report.Value, Unit: report.Unit})
	}
	return detail, nil
}

func (service *PostgresReportService) CreateExport(ctx context.Context, principal Principal, reportID string, query ReportQuery, operationChangeID string) (ReportExport, error) {
	if !safeIdentifier(operationChangeID) {
		return ReportExport{}, ErrReportInvalid
	}
	query.Cursor, query.Limit = "", 50
	detail, err := service.Detail(ctx, principal, reportID, query)
	if err != nil {
		return ReportExport{}, err
	}
	artifact, err := encodeReportCSV(detail)
	if err != nil {
		return ReportExport{}, fmt.Errorf("encode report export: %w", err)
	}
	id, err := newReportExportID()
	if err != nil {
		return ReportExport{}, fmt.Errorf("create report export id: %w", err)
	}
	now := service.clock().UTC()
	expiresAt := now.Add(reportExportTTL)
	digest := sha256.Sum256(artifact)
	fileName := strings.ToLower(detail.Report.ID) + "-" + strings.ToLower(principal.SelectedCountry) + ".csv"
	_, err = service.pool.Exec(ctx, `INSERT INTO admin.report_exports (id,tenant_id,country,report_id,requested_by,operation_change_id,format,status,content_type,file_name,checksum_sha256,artifact,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,'CSV','READY','text/csv; charset=utf-8',$7,$8,$9,$10,$11)`, id, principal.TenantID, principal.SelectedCountry, detail.Report.ID, principal.SubjectID, operationChangeID, fileName, hex.EncodeToString(digest[:]), artifact, expiresAt, now)
	if err != nil {
		return ReportExport{}, fmt.Errorf("store report export: %w", err)
	}
	return service.Export(ctx, principal, id)
}

func (service *PostgresReportService) Export(ctx context.Context, principal Principal, exportID string) (ReportExport, error) {
	if !validReportPrincipal(principal) || !safeIdentifier(exportID) {
		return ReportExport{}, ErrReportInvalid
	}
	var value ReportExport
	err := service.pool.QueryRow(ctx, `SELECT id,report_id,format,status,content_type,file_name,checksum_sha256,octet_length(artifact),expires_at,created_at FROM admin.report_exports WHERE id=$1 AND tenant_id=$2 AND country=$3`, exportID, principal.TenantID, principal.SelectedCountry).Scan(&value.ID, &value.ReportID, &value.Format, &value.Status, &value.ContentType, &value.FileName, &value.ChecksumSHA256, &value.SizeBytes, &value.ExpiresAt, &value.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportExport{}, ErrExportNotFound
	}
	if err != nil {
		return ReportExport{}, fmt.Errorf("load report export: %w", err)
	}
	if !service.clock().UTC().Before(value.ExpiresAt) {
		return ReportExport{}, ErrExportExpired
	}
	if value.Status == "READY" {
		value.DownloadURL = "/admin/api/v1/report-exports/" + value.ID + "/download?token=" + service.signDownload(value.ID, principal, value.ExpiresAt)
	}
	return value, nil
}

func (service *PostgresReportService) Download(ctx context.Context, principal Principal, exportID, token string) (ReportArtifact, error) {
	if !validReportPrincipal(principal) || !safeIdentifier(exportID) || token == "" || len(token) > 256 {
		return ReportArtifact{}, ErrExportToken
	}
	var artifact ReportArtifact
	var expiresAt time.Time
	var status string
	err := service.pool.QueryRow(ctx, `SELECT artifact,content_type,file_name,status,expires_at FROM admin.report_exports WHERE id=$1 AND tenant_id=$2 AND country=$3`, exportID, principal.TenantID, principal.SelectedCountry).Scan(&artifact.Bytes, &artifact.ContentType, &artifact.FileName, &status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportArtifact{}, ErrExportNotFound
	}
	if err != nil {
		return ReportArtifact{}, fmt.Errorf("load report artifact: %w", err)
	}
	if status != "READY" || !service.clock().UTC().Before(expiresAt) {
		return ReportArtifact{}, ErrExportExpired
	}
	expected := service.signDownload(exportID, principal, expiresAt)
	if len(expected) != len(token) || subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return ReportArtifact{}, ErrExportToken
	}
	return artifact, nil
}

func (service *PostgresReportService) signDownload(exportID string, principal Principal, expiresAt time.Time) string {
	mac := hmac.New(sha256.New, service.secret)
	_, _ = mac.Write([]byte(exportID + "\x00" + principal.TenantID + "\x00" + principal.SelectedCountry + "\x00" + expiresAt.UTC().Format(time.RFC3339Nano)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type reportCursor struct {
	Scope  string `json:"scope"`
	Offset int    `json:"offset"`
}

func encodeReportCursor(scope string, offset int) string {
	encoded, _ := json.Marshal(reportCursor{Scope: scope, Offset: offset})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeReportCursor(value, scope string) (int, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) > 512 {
		return 0, ErrReportInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, ErrReportInvalid
	}
	var cursor reportCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.Scope != scope || cursor.Offset < 0 {
		return 0, ErrReportInvalid
	}
	return cursor.Offset, nil
}

func reportDetailScope(reportID string, query ReportQuery) string {
	return strings.Join([]string{"detail", reportID, query.From.UTC().Format(time.RFC3339Nano), query.To.UTC().Format(time.RFC3339Nano)}, ":")
}

func validReportPrincipal(principal Principal) bool {
	return safeIdentifier(principal.TenantID) && safeIdentifier(principal.SubjectID) && validCountry(principal.SelectedCountry)
}

func validReportQuery(query ReportQuery) bool {
	return query.Limit >= 1 && query.Limit <= 50 && (query.Domain == "" || safeIdentifier(query.Domain))
}

func validReportRange(from, to time.Time) bool {
	return from.IsZero() || to.IsZero() || !from.After(to)
}

func encodeReportCSV(detail ReportDetail) ([]byte, error) {
	buffer := bytes.NewBuffer(nil)
	buffer.Write([]byte{0xef, 0xbb, 0xbf})
	writer := csv.NewWriter(buffer)
	if err := writer.Write([]string{"report_id", "title", "domain", "metric", "country", "value", "unit", "freshness"}); err != nil {
		return nil, err
	}
	for _, row := range detail.Items {
		if err := writer.Write([]string{detail.Report.ID, safeCSVCell(detail.Report.Title), detail.Report.Domain, detail.Report.Metric, detail.Country, strconv.FormatInt(row.Value, 10), row.Unit, detail.Report.Freshness.UTC().Format(time.RFC3339)}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buffer.Bytes(), writer.Error()
}

func safeCSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func newReportExportID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "report-export-" + hex.EncodeToString(value), nil
}
