package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Publisher interface {
	Publish(context.Context, Message) error
}

type ReplayAuditor interface {
	RecordReplay(context.Context, ReplayAudit) error
}

type Service struct {
	outbox      OutboxStore
	inbox       InboxStore
	publisher   Publisher
	auditor     ReplayAuditor
	clock       func() time.Time
	lease       time.Duration
	maxAttempts int
}

func NewService(outbox OutboxStore, inbox InboxStore, publisher Publisher, auditor ReplayAuditor, lease time.Duration, maxAttempts int, clock func() time.Time) (*Service, error) {
	if outbox == nil || inbox == nil || publisher == nil || auditor == nil || clock == nil || lease < time.Second || lease > 5*time.Minute || maxAttempts < 1 || maxAttempts > 20 {
		return nil, errors.New("invalid messaging service")
	}
	return &Service{outbox: outbox, inbox: inbox, publisher: publisher, auditor: auditor, lease: lease, maxAttempts: maxAttempts, clock: clock}, nil
}

func (service *Service) Enqueue(ctx context.Context, message Message) error {
	if !validMessage(message) {
		return ErrInvalidRequest
	}
	return service.outbox.Enqueue(ctx, cloneMessage(message), service.clock().UTC())
}

func (service *Service) Dispatch(ctx context.Context, worker string, limit int) (int, error) {
	if !safeID(worker) || limit < 1 || limit > 100 {
		return 0, ErrInvalidRequest
	}
	now := service.clock().UTC()
	records, err := service.outbox.Claim(ctx, worker, now, service.lease, limit)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, record := range records {
		if err := service.publisher.Publish(ctx, record.Message); err != nil {
			next := now.Add(backoff(record.Attempts + 1))
			_, markErr := service.outbox.MarkFailed(ctx, record.Message.ID, worker, now, next, "PUBLISH_FAILED", service.maxAttempts)
			if markErr != nil {
				return published, markErr
			}
			continue
		}
		if err := service.outbox.MarkPublished(ctx, record.Message.ID, worker, now); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

func (service *Service) Consume(ctx context.Context, consumer string, message Message, handler func(context.Context, Message) error) (ConsumeOutcome, error) {
	if !safeID(consumer) || !validMessage(message) || handler == nil {
		return "", ErrInvalidRequest
	}
	now := service.clock().UTC()
	outcome, err := service.inbox.Begin(ctx, consumer, message, now, service.lease)
	if err != nil || outcome != ConsumeProcessed {
		return outcome, err
	}
	if err := handler(ctx, cloneMessage(message)); err != nil {
		_ = service.inbox.Release(ctx, consumer, message.ID)
		return ConsumeProcessed, err
	}
	if err := service.inbox.Complete(ctx, consumer, message, service.clock().UTC()); err != nil {
		return ConsumeProcessed, err
	}
	return ConsumeProcessed, nil
}

func (service *Service) DeadLetters(ctx context.Context, principal ReplayPrincipal, limit int) ([]OutboxRecord, error) {
	if !principal.Capabilities["messaging.dlq.read"] || !safeID(principal.TenantID) || limit < 1 || limit > 200 {
		return nil, ErrForbidden
	}
	return service.outbox.DeadLetters(ctx, principal.TenantID, limit)
}

func (service *Service) Replay(ctx context.Context, principal ReplayPrincipal, messageID, reason, correlationID string) error {
	now := service.clock().UTC()
	authAge := now.Sub(principal.AuthenticatedAt.UTC())
	if !principal.Capabilities["messaging.dlq.replay"] || !safeID(principal.TenantID) || !safeID(principal.SubjectID) ||
		!regexp.MustCompile(`^[A-Z]{2}$`).MatchString(principal.Country) || !safeID(messageID) || !safeID(correlationID) || authAge < -time.Minute || authAge > 5*time.Minute || !safeReason(reason) {
		return ErrForbidden
	}
	if err := service.auditor.RecordReplay(ctx, ReplayAudit{TenantID: principal.TenantID, Country: principal.Country, ActorID: principal.SubjectID, MessageID: messageID,
		Reason: strings.TrimSpace(reason), CorrelationID: correlationID, OccurredAt: now}); err != nil {
		return err
	}
	return service.outbox.Replay(ctx, principal.TenantID, messageID, now)
}

func backoff(attempt int) time.Duration {
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func validMessage(message Message) bool {
	if !uuidPattern.MatchString(message.ID) || !uuidPattern.MatchString(message.TenantID) || !regexp.MustCompile(`^planext4u\.[a-z][a-z0-9_.]{2,127}\.v[1-9][0-9]*$`).MatchString(message.EventType) ||
		message.SchemaVersion < 1 || message.SchemaVersion > 100 || !safeID(message.Producer) || !safeID(message.AggregateType) || !uuidPattern.MatchString(message.AggregateID) ||
		message.AggregateVersion < 1 || !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(message.Country) || !safeID(message.CorrelationID) || !safeID(message.CausationID) ||
		!regexp.MustCompile(`^00-[a-f0-9]{32}-[a-f0-9]{16}-[0-9a-f]{2}$`).MatchString(message.Traceparent) ||
		!map[string]bool{"PUBLIC": true, "INTERNAL": true, "CONFIDENTIAL": true, "RESTRICTED": true}[message.Classification] || message.OccurredAt.IsZero() || message.Data == nil {
		return false
	}
	encoded, err := json.Marshal(message.Data)
	return err == nil && len(encoded) <= 256*1024
}

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

func safeID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(value)
}
func safeReason(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 8 && len(trimmed) <= 256 && !strings.ContainsAny(trimmed, "\r\n")
}
