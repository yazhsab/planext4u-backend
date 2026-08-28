package adminops

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type recordingExecutor struct {
	changes []Change
	fail    bool
}

func (executor *recordingExecutor) Execute(_ Principal, change Change) error {
	executor.changes = append(executor.changes, change)
	if executor.fail {
		return fmt.Errorf("synthetic domain outage")
	}
	return nil
}

func TestBEAdminP3009RBACFreshMFAFourEyesAndAudit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	service, _ := NewService(func() time.Time { return now })
	requester := adminPrincipal(now, "admin-requester")
	command := Command{Domain: DomainWallet, Action: "ADJUST", TargetID: "customer-001", Reason: "Correct verified settlement discrepancy", Payload: map[string]any{"points": 100}, CorrelationID: "corr-admin-001"}
	change, err := service.Submit(requester, command)
	if err != nil || change.Status != StatusPending || change.Risk != RiskHigh {
		t.Fatalf("submit=%#v err=%v", change, err)
	}
	if _, err := service.Approve(requester, change.ID, change.Revision); !errors.Is(err, ErrFourEyesRequired) {
		t.Fatalf("self approval=%v", err)
	}
	approver := adminPrincipal(now, "admin-approver")
	executed, err := service.Approve(approver, change.ID, change.Revision)
	if err != nil || executed.Status != StatusExecuted || executed.ApprovedBy != approver.SubjectID {
		t.Fatalf("approval=%#v err=%v", executed, err)
	}
	events, err := service.Audit(approver)
	if err != nil || len(events) != 2 || events[0].Reason != string(StatusPending) || events[1].Reason != "FOUR_EYES_APPROVED" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestAdminStandardOperationsAndCountryIsolation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	service, _ := NewService(func() time.Time { return now })
	principal := adminPrincipal(now, "content-admin")
	change, err := service.Submit(principal, Command{Domain: DomainCatalog, Action: "UPSERT", TargetID: "item-001", Reason: "Publish verified seller catalogue update", Payload: map[string]any{"name": "Synthetic product"}, CorrelationID: "corr-admin-002"})
	if err != nil || change.Status != StatusExecuted {
		t.Fatalf("standard=%#v err=%v", change, err)
	}
	other := principal
	other.Country = "NG"
	values, _ := service.List(other)
	if len(values) != 0 {
		t.Fatalf("country leakage=%#v", values)
	}
	stale := principal
	stale.AuthenticatedAt = now.Add(-10 * time.Minute)
	if _, err := service.Submit(stale, Command{Domain: DomainPayment, Action: "REFUND", TargetID: "payment-001", Reason: "Refund verified failed delivery payment", Payload: map[string]any{"amount_minor": 100}, CorrelationID: "corr-admin-003"}); !errors.Is(err, ErrFreshMFARequired) {
		t.Fatalf("stale MFA=%v", err)
	}
}

func TestAdminOperationsFilterUnauthorizedDomainsAndProtectReturnedValues(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	service, _ := NewService(func() time.Time { return now })
	principal := adminPrincipal(now, "country-admin")
	_, _ = service.Submit(principal, Command{Domain: DomainCatalog, Action: ActionCatalogUpsert, TargetID: "item-001", Reason: "Publish verified catalogue information", Payload: map[string]any{"name": "Synthetic product"}, CorrelationID: "corr-admin-004"})
	_, _ = service.Submit(principal, Command{Domain: DomainWallet, Action: ActionWalletAdjust, TargetID: "customer-001", Reason: "Correct a verified points discrepancy", Payload: map[string]any{"points": 100}, CorrelationID: "corr-admin-005"})

	contentAdmin := principal
	contentAdmin.Capabilities = map[string]bool{CapabilityCatalog: true}
	values, err := service.List(contentAdmin)
	if err != nil || len(values) != 1 || values[0].Command.Domain != DomainCatalog {
		t.Fatalf("filtered changes=%#v err=%v", values, err)
	}
	values[0].Command.Payload["name"] = "tampered"
	again, _ := service.List(contentAdmin)
	if again[0].Command.Payload["name"] != "Synthetic product" {
		t.Fatalf("returned change mutated stored command: %#v", again[0])
	}

	if _, err := service.Reject(contentAdmin, "change-missing", 1, "Reject after independent verification"); !errors.Is(err, ErrChangeNotFound) {
		t.Fatalf("missing change=%v", err)
	}
}

func TestAdminOperationsRejectUnknownActionsAndSensitivePayloads(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	service, _ := NewService(func() time.Time { return now })
	principal := adminPrincipal(now, "country-admin")
	for name, command := range map[string]Command{
		"unknown action":    {Domain: DomainPayment, Action: "DELETE_EVERYTHING", TargetID: "payment-001", Reason: "Synthetic unsupported operation request", Payload: map[string]any{}, CorrelationID: "corr-admin-006"},
		"sensitive payload": {Domain: DomainPayment, Action: ActionPaymentRefund, TargetID: "payment-001", Reason: "Synthetic unsafe provider payload request", Payload: map[string]any{"provider_secret": "must-not-cross-boundary"}, CorrelationID: "corr-admin-007"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Submit(principal, command); !errors.Is(err, ErrForbidden) {
				t.Fatalf("submit=%v", err)
			}
		})
	}
}

func TestApprovedOperationsExecuteOwningDomainAndFailureStaysRetryable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	executor := &recordingExecutor{}
	service, err := NewServiceWithExecutor(func() time.Time { return now }, executor)
	if err != nil {
		t.Fatal(err)
	}
	requester := adminPrincipal(now, "admin-requester")
	standard, err := service.Submit(requester, Command{Domain: DomainCatalog, Action: ActionCatalogUpsert, TargetID: "item-001", Reason: "Publish verified catalogue update", Payload: map[string]any{"name": "Local item"}, CorrelationID: "corr-execute-001"})
	if err != nil || standard.Status != StatusExecuted || len(executor.changes) != 1 || executor.changes[0].ID != standard.ID {
		t.Fatalf("standard=%#v calls=%#v err=%v", standard, executor.changes, err)
	}
	high, err := service.Submit(requester, Command{Domain: DomainWallet, Action: ActionWalletAdjust, TargetID: "customer-001", Reason: "Correct verified wallet settlement", Payload: map[string]any{"points": 100}, CorrelationID: "corr-execute-002"})
	if err != nil || high.Status != StatusPending || len(executor.changes) != 1 {
		t.Fatalf("high=%#v calls=%d err=%v", high, len(executor.changes), err)
	}
	approver := adminPrincipal(now, "admin-approver")
	executed, err := service.Approve(approver, high.ID, high.Revision)
	if err != nil || executed.Status != StatusExecuted || len(executor.changes) != 2 || executor.changes[1].ID != high.ID {
		t.Fatalf("executed=%#v calls=%#v err=%v", executed, executor.changes, err)
	}
	failing := &recordingExecutor{fail: true}
	failedService, _ := NewServiceWithExecutor(func() time.Time { return now }, failing)
	failed, _ := failedService.Submit(requester, Command{Domain: DomainPayment, Action: ActionPaymentRefund, TargetID: "payment-001", Reason: "Refund verified failed delivery", Payload: map[string]any{"amount_minor": 100}, CorrelationID: "corr-execute-003"})
	if _, err = failedService.Approve(approver, failed.ID, failed.Revision); !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("execution failure=%v", err)
	}
	values, _ := failedService.List(requester)
	if len(values) != 1 || values[0].Status != StatusPending || values[0].Revision != 1 {
		t.Fatalf("unsafe failure state=%#v", values)
	}
}

func TestMemoryDomainExecutorAppliesEveryPhase3AdminDomainIdempotently(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	executor, _ := NewMemoryDomainExecutor(func() time.Time { return now })
	principal := adminPrincipal(now, "admin-executor")
	cases := []struct {
		domain  Domain
		action  string
		payload map[string]any
		state   string
	}{
		{DomainCatalog, ActionCatalogUpsert, map[string]any{"name": "Local product"}, "DRAFT"},
		{DomainOrder, ActionOrderCancel, map[string]any{}, "CANCELLED"},
		{DomainPayment, ActionPaymentRefund, map[string]any{"amount_minor": 100}, "REFUND_SUBMITTED"},
		{DomainWallet, ActionWalletAdjust, map[string]any{"points": 50}, "ADJUSTED"},
		{DomainCampaign, ActionCampaignActivate, map[string]any{}, "ACTIVE"},
		{DomainCMS, ActionCMSPublish, map[string]any{"revision": 7}, "PUBLISHED"},
		{DomainSupport, ActionSupportEscalate, map[string]any{}, "ESCALATED"},
		{DomainReporting, ActionReportingExport, map[string]any{"format": "CSV"}, "READY"},
	}
	for index, testCase := range cases {
		target := fmt.Sprintf("target-%03d", index)
		change := Change{ID: fmt.Sprintf("change-%03d", index), TenantID: principal.TenantID, Country: principal.Country, Status: StatusExecuted, Command: Command{Domain: testCase.domain, Action: testCase.action, TargetID: target, Payload: testCase.payload, CorrelationID: fmt.Sprintf("corr-%03d", index)}}
		if err := executor.Execute(principal, change); err != nil {
			t.Fatalf("%s execute: %v", testCase.domain, err)
		}
		if err := executor.Execute(principal, change); err != nil {
			t.Fatalf("%s replay: %v", testCase.domain, err)
		}
		record, exists := executor.Record(principal.TenantID, principal.Country, testCase.domain, target)
		if !exists || record.State != testCase.state || record.Revision != 1 || record.LastChange != change.ID {
			t.Fatalf("%s record=%#v exists=%v", testCase.domain, record, exists)
		}
	}
}

func adminPrincipal(now time.Time, subject string) Principal {
	return Principal{TenantID: "tenant-synthetic-001", Country: "IN", SubjectID: subject, AuthenticatedAt: now.Add(-time.Minute), AuthMethods: []string{"password", "webauthn"}, Capabilities: map[string]bool{CapabilityCatalog: true, CapabilityOrder: true, CapabilityPayment: true, CapabilityWallet: true, CapabilityCampaign: true, CapabilityCMS: true, CapabilitySupport: true, CapabilityReporting: true}}
}
