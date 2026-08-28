package adminops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"
)

type DomainRecord struct {
	TenantID   string         `json:"tenant_id"`
	Country    string         `json:"country"`
	Domain     Domain         `json:"domain"`
	TargetID   string         `json:"target_id"`
	State      string         `json:"state"`
	Revision   int64          `json:"revision"`
	Payload    map[string]any `json:"payload"`
	LastChange string         `json:"last_change"`
	UpdatedBy  string         `json:"updated_by"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// MemoryDomainExecutor is the deterministic local/staging implementation of
// the Phase 3 domain command boundary. Production BFFs use NewServiceWithExecutor
// with service-owned adapters; this implementation ensures local operation
// commands mutate inspectable state instead of being reported as successful no-ops.
type MemoryDomainExecutor struct {
	clock      func() time.Time
	mu         sync.Mutex
	records    map[string]DomainRecord
	executions map[string]string
}

func NewMemoryDomainExecutor(clock func() time.Time) (*MemoryDomainExecutor, error) {
	if clock == nil {
		return nil, ErrInvalidRequest
	}
	return &MemoryDomainExecutor{clock: clock, records: map[string]DomainRecord{}, executions: map[string]string{}}, nil
}

func (executor *MemoryDomainExecutor) Execute(principal Principal, change Change) error {
	if !validPrincipal(principal) || principal.TenantID != change.TenantID || principal.Country != change.Country || change.Status == StatusRejected {
		return ErrForbidden
	}
	encoded, err := json.Marshal(change.Command)
	if err != nil {
		return ErrInvalidRequest
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if previous, exists := executor.executions[change.ID]; exists {
		if previous != fingerprint {
			return ErrRevisionConflict
		}
		return nil
	}
	key := domainRecordKey(change.TenantID, change.Country, change.Command.Domain, change.Command.TargetID)
	record := executor.records[key]
	if record.Revision == 0 {
		record = DomainRecord{TenantID: change.TenantID, Country: change.Country, Domain: change.Command.Domain, TargetID: change.Command.TargetID, Payload: map[string]any{}}
	}
	state, payload, err := applyDomainCommand(record, change.Command)
	if err != nil {
		return err
	}
	record.State, record.Payload, record.LastChange, record.UpdatedBy, record.UpdatedAt = state, payload, change.ID, principal.SubjectID, executor.clock().UTC()
	record.Revision++
	executor.records[key] = cloneDomainRecord(record)
	executor.executions[change.ID] = fingerprint
	return nil
}

func (executor *MemoryDomainExecutor) Record(tenantID, country string, domain Domain, targetID string) (DomainRecord, bool) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	value, exists := executor.records[domainRecordKey(tenantID, country, domain, targetID)]
	return cloneDomainRecord(value), exists
}

func applyDomainCommand(current DomainRecord, command Command) (string, map[string]any, error) {
	payload := cloneMap(current.Payload)
	if payload == nil {
		payload = map[string]any{}
	}
	merge := func() {
		for key, value := range command.Payload {
			payload[key] = value
		}
	}
	switch command.Domain {
	case DomainCatalog:
		switch command.Action {
		case ActionCatalogUpsert:
			if name, ok := command.Payload["name"].(string); !ok || strings.TrimSpace(name) == "" || len(name) > 240 {
				return "", nil, ErrInvalidRequest
			}
			merge()
			return "DRAFT", payload, nil
		case ActionCatalogPublish, ActionCatalogApprove:
			merge()
			return "PUBLISHED", payload, nil
		case ActionCatalogUnpublish:
			return "UNPUBLISHED", payload, nil
		case ActionCatalogReject:
			return "REJECTED", payload, nil
		}
	case DomainOrder:
		merge()
		if command.Action == ActionOrderCancel {
			return "CANCELLED", payload, nil
		}
		if command.Action == ActionOrderRefundApprove {
			return "REFUND_APPROVED", payload, nil
		}
		if status, ok := command.Payload["status"].(string); command.Action == ActionOrderOverride && ok && safeID(status) {
			return status, payload, nil
		}
	case DomainPayment:
		merge()
		if command.Action == ActionPaymentRefund {
			if amount, ok := integerPayload(command.Payload["amount_minor"]); !ok || amount <= 0 {
				return "", nil, ErrInvalidRequest
			}
			return "REFUND_SUBMITTED", payload, nil
		}
		if command.Action == ActionPaymentReconcile {
			return "RECONCILED", payload, nil
		}
		if command.Action == ActionPaymentCODCollect {
			return "COD_COLLECTED", payload, nil
		}
	case DomainWallet:
		merge()
		if command.Action == ActionWalletAdjust {
			if points, ok := integerPayload(command.Payload["points"]); !ok || points == 0 || points < -1_000_000 || points > 1_000_000 {
				return "", nil, ErrInvalidRequest
			}
			return "ADJUSTED", payload, nil
		}
		if command.Action == ActionWalletReverse {
			return "REVERSED", payload, nil
		}
		if command.Action == ActionWalletFreeze {
			return "FROZEN", payload, nil
		}
	case DomainCampaign:
		merge()
		if command.Action == ActionCampaignUpsert {
			return "DRAFT", payload, nil
		}
		if command.Action == ActionCampaignActivate {
			return "ACTIVE", payload, nil
		}
		if command.Action == ActionCampaignPause {
			return "PAUSED", payload, nil
		}
	case DomainCMS:
		merge()
		if command.Action == ActionCMSUpsert {
			return "DRAFT", payload, nil
		}
		if command.Action == ActionCMSPublish {
			return "PUBLISHED", payload, nil
		}
		if command.Action == ActionCMSRollback {
			return "ROLLED_BACK", payload, nil
		}
	case DomainSupport:
		merge()
		if command.Action == ActionSupportUpdate {
			return "OPEN", payload, nil
		}
		if command.Action == ActionSupportEscalate {
			return "ESCALATED", payload, nil
		}
		if command.Action == ActionSupportResolve {
			return "RESOLVED", payload, nil
		}
	case DomainReporting:
		if command.Action == ActionReportingExport {
			merge()
			payload["export_reference"] = "export-" + changeSafeDigest(command.CorrelationID+"\x00"+command.TargetID)
			return "READY", payload, nil
		}
	}
	return "", nil, ErrInvalidRequest
}

func integerPayload(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case float64:
		return int64(number), !math.IsNaN(number) && !math.IsInf(number, 0) && number == math.Trunc(number)
	default:
		return 0, false
	}
}

func domainRecordKey(tenantID, country string, domain Domain, targetID string) string {
	return tenantID + "\x00" + country + "\x00" + string(domain) + "\x00" + targetID
}

func cloneDomainRecord(value DomainRecord) DomainRecord {
	value.Payload = cloneMap(value.Payload)
	return value
}
func changeSafeDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}
