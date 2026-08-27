package audit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBEAudit001CompletenessImmutabilityRedactionAndChain(t *testing.T) {
	t.Parallel()
	service, principal, now := fixture(t)
	first, err := service.Record(context.Background(), principal, recordRequest("admin.role.updated"))
	if err != nil || first.Sequence != 1 || first.Hash == "" || first.PreviousHash != "" {
		t.Fatalf("first = %#v, %v", first, err)
	}
	if first.Before["access_token"] != "[REDACTED]" || first.After["profile"].(map[string]any)["email"] != "[REDACTED]" {
		t.Fatalf("redaction = %#v %#v", first.Before, first.After)
	}
	second, err := service.Record(context.Background(), principal, recordRequest("config.published"))
	if err != nil || second.Sequence != 2 || second.PreviousHash != first.Hash {
		t.Fatalf("second = %#v, %v", second, err)
	}
	page, err := service.Search(context.Background(), principal, SearchFilter{Limit: 1})
	if err != nil || len(page.Entries) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	after, _ := DecodeCursor(page.NextCursor)
	page, _ = service.Search(context.Background(), principal, SearchFilter{After: after, Limit: 10})
	if len(page.Entries) != 1 || page.Entries[0].Sequence != 2 {
		t.Fatalf("second page = %#v", page)
	}
	principal.Capabilities["audit.export"] = true
	principal.AuthenticatedAt = *now
	export, err := service.Export(context.Background(), principal, "Incident investigation", SearchFilter{})
	if err != nil || len(export.Entries) != 2 || !export.ChainValid {
		t.Fatalf("export = %#v, %v", export, err)
	}
	first.Before["access_token"] = "mutated"
	page, _ = service.Search(context.Background(), principal, SearchFilter{Limit: 10})
	if page.Entries[0].Before["access_token"] != "[REDACTED]" {
		t.Fatal("returned entry mutated append-only storage")
	}
}

func TestBEAudit001AuthorizationFreshAuthAndTenantIsolation(t *testing.T) {
	t.Parallel()
	service, principal, now := fixture(t)
	if _, err := service.Record(context.Background(), principal, recordRequest("admin.role.updated")); err != nil {
		t.Fatal(err)
	}
	denied := principal
	denied.Capabilities = map[string]bool{}
	if _, err := service.Search(context.Background(), denied, SearchFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("search denial = %v", err)
	}
	principal.Capabilities["audit.export"] = true
	principal.AuthenticatedAt = now.Add(-10 * time.Minute)
	if _, err := service.Export(context.Background(), principal, "Incident investigation", SearchFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale export = %v", err)
	}
	other := principal
	other.TenantID = "tenant-other"
	other.AuthenticatedAt = *now
	export, err := service.Export(context.Background(), other, "Incident investigation", SearchFilter{})
	if err != nil || len(export.Entries) != 0 {
		t.Fatalf("cross tenant export = %#v, %v", export, err)
	}
}

func fixture(t *testing.T) (*Service, Principal, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	service, err := NewService(NewMemoryRepository(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	service.newID = func() (string, error) { sequence++; return fmt.Sprintf("aud_synthetic_%d", sequence), nil }
	principal := Principal{TenantID: "tenant-synthetic", SubjectID: "admin-synthetic", Capabilities: map[string]bool{"audit.write": true, "audit.read": true}}
	return service, principal, &now
}

func recordRequest(action string) RecordRequest {
	return RecordRequest{Country: "IN", Actor: Actor{SubjectID: "admin-synthetic", ActorType: "USER"}, Action: action,
		Target: Target{Type: "customer", ID: "customer-synthetic"}, Outcome: OutcomeSucceeded, ReasonCode: "APPROVED_CHANGE",
		CorrelationID: "corr-synthetic-audit", OccurredAt: time.Date(2026, 8, 27, 9, 59, 0, 0, time.UTC),
		Before: map[string]any{"access_token": "secret-token"}, After: map[string]any{"profile": map[string]any{"email": "customer@example.test"}}}
}
