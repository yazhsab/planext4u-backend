package adminops

import (
	"errors"
	"testing"
	"time"
)

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

func adminPrincipal(now time.Time, subject string) Principal {
	return Principal{TenantID: "tenant-synthetic-001", Country: "IN", SubjectID: subject, AuthenticatedAt: now.Add(-time.Minute), AuthMethods: []string{"password", "webauthn"}, Capabilities: map[string]bool{CapabilityCatalog: true, CapabilityOrder: true, CapabilityPayment: true, CapabilityWallet: true, CapabilityCampaign: true, CapabilitySupport: true, CapabilityReporting: true}}
}
