package adminops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

type Service struct {
	clock    func() time.Time
	executor Executor
	mu       sync.Mutex
	changes  map[string]Change
	events   []AuditEvent
}

func NewService(clock func() time.Time) (*Service, error) {
	executor, err := NewMemoryDomainExecutor(clock)
	if err != nil {
		return nil, err
	}
	return newService(clock, executor)
}

func NewServiceWithExecutor(clock func() time.Time, executor Executor) (*Service, error) {
	if executor == nil {
		return nil, ErrInvalidRequest
	}
	return newService(clock, executor)
}

func newService(clock func() time.Time, executor Executor) (*Service, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{clock: clock, executor: executor, changes: map[string]Change{}}, nil
}

func (service *Service) Submit(principal Principal, command Command) (Change, error) {
	if !validPrincipal(principal) || !validCommand(command) || !principal.Capabilities[capability(command.Domain)] {
		service.record(principal, command, "DENIED", "CAPABILITY_OR_INPUT_INVALID")
		return Change{}, ErrForbidden
	}
	risk := commandRisk(command)
	if !mfaSatisfied(principal) || (risk == RiskHigh && !fresh(principal, service.clock().UTC())) {
		service.record(principal, command, "DENIED", "FRESH_MFA_REQUIRED")
		return Change{}, ErrFreshMFARequired
	}
	encoded, _ := json.Marshal(struct {
		Tenant, Country, Actor string
		Command                Command
	}{principal.TenantID, principal.Country, principal.SubjectID, command})
	digest := sha256.Sum256(encoded)
	now := service.clock().UTC()
	status := StatusExecuted
	if risk == RiskHigh {
		status = StatusPending
	}
	value := Change{ID: "change-" + hex.EncodeToString(digest[:8]), Revision: 1, TenantID: principal.TenantID, Country: principal.Country, Command: cloneCommand(command), Risk: risk, Status: status, RequestedBy: principal.SubjectID, CreatedAt: now, UpdatedAt: now}
	service.mu.Lock()
	if existing, exists := service.changes[value.ID]; exists {
		service.mu.Unlock()
		return cloneChange(existing), nil
	}
	service.mu.Unlock()
	if status == StatusExecuted && service.executor != nil {
		if err := service.executor.Execute(principal, cloneChange(value)); err != nil {
			service.record(principal, command, "FAILED", "DOMAIN_EXECUTION_FAILED")
			return Change{}, ErrExecutionFailed
		}
	}
	service.mu.Lock()
	if existing, exists := service.changes[value.ID]; exists {
		service.mu.Unlock()
		return cloneChange(existing), nil
	}
	service.changes[value.ID] = cloneChange(value)
	service.mu.Unlock()
	service.record(principal, command, "SUCCEEDED", string(status))
	return cloneChange(value), nil
}

func (service *Service) Approve(principal Principal, changeID string, expectedRevision int64) (Change, error) {
	if !validPrincipal(principal) || !safeID(changeID) || expectedRevision < 1 || !mfaSatisfied(principal) || !fresh(principal, service.clock().UTC()) {
		return Change{}, ErrFreshMFARequired
	}
	service.mu.Lock()
	value, exists := service.changes[changeID]
	if !exists || value.TenantID != principal.TenantID || value.Country != principal.Country {
		service.mu.Unlock()
		return Change{}, ErrChangeNotFound
	}
	if !principal.Capabilities[capability(value.Command.Domain)] {
		service.mu.Unlock()
		return Change{}, ErrForbidden
	}
	if value.RequestedBy == principal.SubjectID {
		service.mu.Unlock()
		return Change{}, ErrFourEyesRequired
	}
	if value.Revision != expectedRevision {
		service.mu.Unlock()
		return Change{}, ErrRevisionConflict
	}
	if value.Status != StatusPending {
		service.mu.Unlock()
		return Change{}, ErrInvalidState
	}
	service.mu.Unlock()
	if service.executor != nil {
		if err := service.executor.Execute(principal, cloneChange(value)); err != nil {
			service.record(principal, value.Command, "FAILED", "DOMAIN_EXECUTION_FAILED")
			return Change{}, ErrExecutionFailed
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	latest, exists := service.changes[changeID]
	if !exists || latest.Revision != expectedRevision || latest.Status != StatusPending {
		return Change{}, ErrRevisionConflict
	}
	value = latest
	value.Status, value.ApprovedBy, value.Revision, value.UpdatedAt = StatusExecuted, principal.SubjectID, value.Revision+1, service.clock().UTC()
	service.changes[changeID] = cloneChange(value)
	service.appendEventLocked(principal, value.Command, "SUCCEEDED", "FOUR_EYES_APPROVED")
	return cloneChange(value), nil
}

func (service *Service) Reject(principal Principal, changeID string, expectedRevision int64, reason string) (Change, error) {
	if !validPrincipal(principal) || !safeID(changeID) || expectedRevision < 1 || !safeReason(reason) || !mfaSatisfied(principal) || !fresh(principal, service.clock().UTC()) {
		return Change{}, ErrFreshMFARequired
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, exists := service.changes[changeID]
	if !exists || value.TenantID != principal.TenantID || value.Country != principal.Country {
		return Change{}, ErrChangeNotFound
	}
	if !principal.Capabilities[capability(value.Command.Domain)] {
		return Change{}, ErrForbidden
	}
	if value.RequestedBy == principal.SubjectID {
		return Change{}, ErrFourEyesRequired
	}
	if value.Revision != expectedRevision {
		return Change{}, ErrRevisionConflict
	}
	if value.Status != StatusPending {
		return Change{}, ErrInvalidState
	}
	value.Status, value.ApprovedBy, value.Revision, value.UpdatedAt = StatusRejected, principal.SubjectID, value.Revision+1, service.clock().UTC()
	service.changes[changeID] = cloneChange(value)
	service.appendEventLocked(principal, value.Command, "SUCCEEDED", "FOUR_EYES_REJECTED")
	return cloneChange(value), nil
}

func (service *Service) List(principal Principal) ([]Change, error) {
	if !validPrincipal(principal) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := []Change{}
	for _, value := range service.changes {
		if value.TenantID == principal.TenantID && value.Country == principal.Country && principal.Capabilities[capability(value.Command.Domain)] {
			result = append(result, cloneChange(value))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.After(result[right].CreatedAt)
	})
	return result, nil
}

func (service *Service) Audit(principal Principal) ([]AuditEvent, error) {
	if !validPrincipal(principal) || !principal.Capabilities[CapabilityReporting] {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := []AuditEvent{}
	for _, value := range service.events {
		if value.TenantID == principal.TenantID && value.Country == principal.Country {
			result = append(result, value)
		}
	}
	return result, nil
}

func (service *Service) record(principal Principal, command Command, outcome, reason string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.appendEventLocked(principal, command, outcome, reason)
}
func (service *Service) appendEventLocked(principal Principal, command Command, outcome, reason string) {
	service.events = append(service.events, AuditEvent{Sequence: int64(len(service.events) + 1), TenantID: principal.TenantID, Country: principal.Country, ActorID: principal.SubjectID, Action: string(command.Domain) + "." + command.Action, TargetID: command.TargetID, Outcome: outcome, Reason: reason, CorrelationID: command.CorrelationID, CreatedAt: service.clock().UTC()})
}
func commandRisk(command Command) Risk {
	if command.Domain == DomainCatalog && (command.Action == ActionCatalogUpsert || command.Action == ActionCatalogPublish) ||
		command.Domain == DomainCampaign && command.Action == ActionCampaignUpsert ||
		command.Domain == DomainCMS && (command.Action == ActionCMSUpsert || command.Action == ActionCMSPublish) ||
		command.Domain == DomainSupport && (command.Action == ActionSupportUpdate || command.Action == ActionSupportResolve) ||
		command.Domain == DomainReporting && command.Action == ActionReportingExport {
		return RiskStandard
	}
	return RiskHigh
}
func capability(domain Domain) string {
	return map[Domain]string{DomainCatalog: CapabilityCatalog, DomainOrder: CapabilityOrder, DomainPayment: CapabilityPayment, DomainWallet: CapabilityWallet, DomainCampaign: CapabilityCampaign, DomainCMS: CapabilityCMS, DomainSupport: CapabilitySupport, DomainReporting: CapabilityReporting}[domain]
}
func fresh(value Principal, now time.Time) bool {
	age := now.Sub(value.AuthenticatedAt.UTC())
	return age >= -time.Minute && age <= 5*time.Minute
}
func mfaSatisfied(value Principal) bool {
	for _, method := range value.AuthMethods {
		if method == "webauthn" || method == "totp" || method == "mfa" {
			return true
		}
	}
	return false
}
func validPrincipal(value Principal) bool {
	return safeID(value.TenantID) && len(value.Country) == 2 && value.Country == strings.ToUpper(value.Country) && safeID(value.SubjectID) && !value.AuthenticatedAt.IsZero()
}
func validCommand(value Command) bool {
	encoded, err := json.Marshal(value.Payload)
	return capability(value.Domain) != "" && validAction(value.Domain, value.Action) && safeID(value.TargetID) && safeReason(value.Reason) && safeID(value.CorrelationID) && err == nil && len(encoded) <= 64*1024 && !containsSensitive(value.Payload)
}

func validAction(domain Domain, action string) bool {
	allowed := map[Domain]map[string]bool{
		DomainCatalog: {
			ActionCatalogUpsert: true, ActionCatalogPublish: true, ActionCatalogUnpublish: true,
			ActionCatalogApprove: true, ActionCatalogReject: true,
		},
		DomainOrder: {
			ActionOrderCancel: true, ActionOrderOverride: true, ActionOrderRefundApprove: true,
		},
		DomainPayment: {
			ActionPaymentRefund: true, ActionPaymentReconcile: true, ActionPaymentCODCollect: true,
		},
		DomainWallet: {
			ActionWalletAdjust: true, ActionWalletReverse: true, ActionWalletFreeze: true,
		},
		DomainCampaign: {
			ActionCampaignUpsert: true, ActionCampaignActivate: true, ActionCampaignPause: true,
		},
		DomainCMS: {
			ActionCMSUpsert: true, ActionCMSPublish: true, ActionCMSRollback: true,
		},
		DomainSupport: {
			ActionSupportUpdate: true, ActionSupportEscalate: true, ActionSupportResolve: true,
		},
		DomainReporting: {
			ActionReportingExport: true,
		},
	}
	return allowed[domain][action]
}
func containsSensitive(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || containsSensitive(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsSensitive(child) {
				return true
			}
		}
	}
	return false
}
func safeID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') && !strings.ContainsRune("._:-", char) {
			return false
		}
	}
	return true
}
func safeReason(value string) bool {
	return len(value) >= 8 && len(value) <= 500 && strings.TrimSpace(value) == value
}
func cloneCommand(value Command) Command { value.Payload = cloneMap(value.Payload); return value }
func cloneMap(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}
func cloneChange(value Change) Change { value.Command = cloneCommand(value.Command); return value }
