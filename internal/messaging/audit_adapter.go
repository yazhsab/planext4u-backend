package messaging

import (
	"context"

	"github.com/yazhsab/planext4u-backend/internal/audit"
)

type AuditAdapter struct{ service *audit.Service }

func NewAuditAdapter(service *audit.Service) (*AuditAdapter, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	return &AuditAdapter{service: service}, nil
}

func (adapter *AuditAdapter) RecordReplay(ctx context.Context, replay ReplayAudit) error {
	_, err := adapter.service.Record(ctx, audit.Principal{TenantID: replay.TenantID, SubjectID: replay.ActorID,
		Capabilities: map[string]bool{"audit.write": true}}, audit.RecordRequest{
		Country: replay.Country, Actor: audit.Actor{SubjectID: replay.ActorID, ActorType: "USER"}, Action: "messaging.dlq.replay_requested",
		Target: audit.Target{Type: "outbox_message", ID: replay.MessageID}, Outcome: audit.OutcomeSucceeded,
		ReasonCode: "DLQ_REPLAY_APPROVED", CorrelationID: replay.CorrelationID, OccurredAt: replay.OccurredAt,
		After: map[string]any{"reason": replay.Reason},
	})
	return err
}
