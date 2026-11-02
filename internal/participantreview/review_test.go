package participantreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFiltersAreBoundedLiteralAndAuthorityFree(t *testing.T) {
	statuses := []string{"KYC_REVIEW", "APPROVED"}
	for _, raw := range []string{"country=IN", "tenant_id=other", "status=APPROVED&status=KYC_REVIEW", "status=FAKE", "after=not-a-uuid", "search=%ZZ", "search=%00", "search=" + strings.Repeat("a", 129)} {
		if _, err := Parse(raw, statuses); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", raw, err)
		}
	}
	f, err := Parse("status=KYC_REVIEW&search=%25_%E0%AE%B5%E0%AF%80%E0%AE%9F%E0%AF%81&after=10000000-0000-4000-8000-000000000001", statuses)
	if err != nil || f.Search != "%_வீடு" || f.Validate(statuses) != nil {
		t.Fatalf("literal Tamil filter: %+v %v", f, err)
	}
	if UUID("not-uuid") || UUID("10000000000040008000000000000001") {
		t.Fatal("noncanonical UUID accepted")
	}
}

func TestRegionalQueueWireOmitsPrivateReviewMetadata(t *testing.T) {
	page := Page{Items: []Summary{{
		ID: "10000000-0000-4000-8000-000000000001", ParticipantID: "20000000-0000-4000-8000-000000000001",
		Kind: "VENDOR", Revision: 2, Status: "KYC_REVIEW", DisplayName: "Regional vendor",
		Category: "RETAIL", DocumentCount: 3, BankStatus: "VERIFIED", Actions: []string{"REJECT"},
	}}, NextAfter: "10000000-0000-4000-8000-000000000001"}
	regional := MinimalRegionalPage(page)
	data, err := json.Marshal(regional)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"document_count", "bank_status", "VERIFIED"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("regional owner response leaked %q: %s", private, data)
		}
	}
	if len(regional.Items) != 1 || len(regional.Items[0].Actions) != 1 || regional.NextAfter != page.NextAfter {
		t.Fatalf("regional queue lost scope actions or cursor: %+v", regional)
	}
	regional.Items[0].Actions[0] = "changed"
	if page.Items[0].Actions[0] != "REJECT" {
		t.Fatal("regional response retained mutable owner action slice")
	}
	full, err := json.Marshal(page)
	if err != nil || !strings.Contains(string(full), "document_count") || !strings.Contains(string(full), "bank_status") {
		t.Fatalf("country review projection changed: %s %v", full, err)
	}
}
func TestSourceProofCannotBeOmittedAndPaginationRemainsBounded(t *testing.T) {
	for _, scope := range []Scope{{}, {TenantID: "10000000-0000-4000-8000-000000000001", Subject: "20000000-0000-4000-8000-000000000001", MFA: true, Country: "IN", Revision: 1}} {
		if err := Lock(context.Background(), nil, scope); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
	}
	rows := make([]Summary, 51)
	rows[49].ID = "10000000-0000-4000-8000-000000000049"
	page := Bounded(rows)
	if len(page.Items) != 50 || page.NextAfter != rows[49].ID {
		t.Fatal(page)
	}
	if page = Bounded(make([]Summary, 0)); page.Items == nil || page.NextAfter != "" {
		t.Fatal(page)
	}
}
