package adminshell

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReportCursorCannotBeReusedAcrossScopes(t *testing.T) {
	cursor := encodeReportCursor("catalog:finance", 20)
	if offset, err := decodeReportCursor(cursor, "catalog:finance"); err != nil || offset != 20 {
		t.Fatalf("decode matching cursor: offset=%d err=%v", offset, err)
	}
	if _, err := decodeReportCursor(cursor, "catalog:operations"); !errors.Is(err, ErrReportInvalid) {
		t.Fatalf("expected scoped cursor rejection, got %v", err)
	}
}

func TestReportDownloadSignatureIsTenantAndCountryBound(t *testing.T) {
	service := &PostgresReportService{secret: []byte("test-derived-report-signing-secret")}
	expiresAt := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	principal := Principal{TenantID: "tenant-one", SelectedCountry: "IN"}
	signature := service.signDownload("report-export-0123456789abcdef0123456789abcdef", principal, expiresAt)

	otherTenant := principal
	otherTenant.TenantID = "tenant-two"
	otherCountry := principal
	otherCountry.SelectedCountry = "SG"
	if signature == service.signDownload("report-export-0123456789abcdef0123456789abcdef", otherTenant, expiresAt) {
		t.Fatal("signature must change with tenant scope")
	}
	if signature == service.signDownload("report-export-0123456789abcdef0123456789abcdef", otherCountry, expiresAt) {
		t.Fatal("signature must change with country scope")
	}
}

func TestReportCSVPreventsFormulaInjection(t *testing.T) {
	detail := ReportDetail{
		Report:  ReportSummary{ID: "sales", Title: "=HYPERLINK(\"https://example.invalid\")", Domain: "finance", Metric: "gross_sales", Freshness: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)},
		Country: "IN",
		Items:   []ReportRow{{Value: 4200, Unit: "INR_MINOR"}},
	}
	artifact, err := encodeReportCSV(detail)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact) < 3 || string(artifact[:3]) != "\xef\xbb\xbf" {
		t.Fatal("expected UTF-8 BOM")
	}
	if !strings.Contains(string(artifact), "'=HYPERLINK") {
		t.Fatalf("expected formula-safe CSV cell, got %q", artifact)
	}
}
