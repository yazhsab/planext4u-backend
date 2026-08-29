package adminops

import (
	"testing"
	"time"
)

func TestBEP5010Phase5GovernanceDomainsRemainMFAFourEyesAndAudited(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	executor, _ := NewMemoryDomainExecutor(func() time.Time { return now })
	service, err := NewServiceWithExecutor(func() time.Time { return now }, executor)
	if err != nil {
		t.Fatal(err)
	}
	requester := adminPrincipal(now, "governance-admin-001")
	approver := adminPrincipal(now, "governance-admin-002")
	for _, capability := range []string{CapabilityContent, CapabilityPolicy, CapabilityCountry, CapabilityEmergency, CapabilityIntelligence} {
		requester.Capabilities[capability] = true
		approver.Capabilities[capability] = true
	}
	tests := []struct {
		domain                Domain
		action, target, state string
	}{{DomainContent, ActionContentModerate, "social-post-001", "MODERATED"}, {DomainPolicy, ActionPolicyPublish, "policy-IN-001", "PUBLISHED"}, {DomainCountry, ActionCountryUpdate, "country-IN", "UPDATED"}, {DomainEmergency, ActionEmergencySLA, "sla-IN", "SLA_UPDATED"}, {DomainIntelligence, ActionIntelligencePublish, "insight-001", "PUBLISHED"}}
	for index, testCase := range tests {
		change, submitErr := service.Submit(requester, Command{Domain: testCase.domain, Action: testCase.action, TargetID: testCase.target, Reason: "Verified Phase 5 governance operation", Payload: map[string]any{"phase": 5}, CorrelationID: "corr-phase5-00" + string(rune('1'+index))})
		if submitErr != nil {
			t.Fatalf("submit %s: %v", testCase.domain, submitErr)
		}
		if change.Status == StatusPending {
			change, submitErr = service.Approve(approver, change.ID, change.Revision)
		}
		if submitErr != nil || change.Status != StatusExecuted {
			t.Fatalf("change=%#v err=%v", change, submitErr)
		}
		record, ok := executor.Record(requester.TenantID, requester.Country, testCase.domain, testCase.target)
		if !ok || record.State != testCase.state {
			t.Fatalf("record=%#v ok=%v", record, ok)
		}
	}
	events, err := service.Audit(requester)
	if err != nil || len(events) < len(tests)*2 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}
