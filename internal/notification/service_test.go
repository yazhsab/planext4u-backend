package notification

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	testTenant = "11111111-1111-4111-8111-111111111111"
	testTrace  = "00-11111111111111111111111111111111-2222222222222222-01"
)

func TestBENotify001ConsentPreferencesTemplatesAndIdempotency(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.PutTemplate(publishedTemplate(now, "en", "Hello {{name}}", "Your plan {{plan}} is ready"))
	service := mustService(t, repository, map[Channel]Provider{}, func() time.Time { return now })

	command := validCommandFixture(PurposeMarketing)
	suppressed, err := service.Queue(context.Background(), command)
	if err != nil {
		t.Fatalf("queue without consent: %v", err)
	}
	if suppressed.Status != DeliverySuppressed || suppressed.SuppressionReason != "CONSENT_NOT_GRANTED" || len(repository.PendingMessages()) != 0 {
		t.Fatalf("delivery without consent = %#v, messages = %d", suppressed, len(repository.PendingMessages()))
	}

	repository.PutConsent(Consent{TenantID: testTenant, SubjectID: command.SubjectID, Purpose: PurposeMarketing, Granted: true, Version: 1, UpdatedAt: now})
	repository.PutPreference(Preference{TenantID: testTenant, SubjectID: command.SubjectID, Purpose: PurposeMarketing, Channel: ChannelEmail, Enabled: true, Version: 1, UpdatedAt: now})
	command.IdempotencyKey = "notify-marketing-002"
	delivery, err := service.Queue(context.Background(), command)
	if err != nil {
		t.Fatalf("queue allowed marketing: %v", err)
	}
	if delivery.Status != DeliveryQueued || delivery.RenderedSubject != "Hello Ada" || delivery.RenderedBody != "Your plan Pro is ready" {
		t.Fatalf("queued delivery = %#v", delivery)
	}
	duplicate, err := service.Queue(context.Background(), command)
	if err != nil || duplicate.ID != delivery.ID || len(repository.PendingMessages()) != 1 {
		t.Fatalf("idempotent queue = %#v, %v, messages = %d", duplicate, err, len(repository.PendingMessages()))
	}
	message := repository.PendingMessages()[0]
	if message.EventType != "planext4u.notification.delivery.requested.v1" || message.Data["delivery_id"] != delivery.ID || message.TenantID != testTenant {
		t.Fatalf("outbox message = %#v", message)
	}
}

func TestNotificationLocaleFallbackAndSecurityBypass(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.PutTemplate(publishedTemplate(now, "en", "Security alert", "Hello {{name}}, plan {{plan}} changed"))
	repository.PutPreference(Preference{TenantID: testTenant, SubjectID: "customer-1", Purpose: PurposeSecurity, Channel: ChannelEmail, Enabled: false, Version: 1, UpdatedAt: now})
	service := mustService(t, repository, map[Channel]Provider{}, func() time.Time { return now })
	command := validCommandFixture(PurposeSecurity)
	command.Locale, command.IdempotencyKey = "fr-FR", "security-001"
	delivery, err := service.Queue(context.Background(), command)
	if err != nil {
		t.Fatalf("queue security alert: %v", err)
	}
	if delivery.Status != DeliveryQueued || delivery.Locale != "en" || delivery.RenderedSubject != "Security alert" {
		t.Fatalf("security delivery = %#v", delivery)
	}
}

func TestNotificationProviderRetryPermanentFailureAndReceiptDeduplication(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.PutTemplate(publishedTemplate(now, "en", "Hello {{name}}", "Your plan {{plan}} is ready"))
	provider := &scriptedProvider{results: []providerResult{{err: &ProviderError{Code: "RATE_LIMITED", Retryable: true}}, {receipt: ProviderReceipt{ProviderMessageID: "provider-001", Status: DeliverySent}}}}
	service := mustService(t, repository, map[Channel]Provider{ChannelEmail: provider}, func() time.Time { return now })
	delivery, err := service.Queue(context.Background(), validCommandFixture(PurposeTransactional))
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := service.Process(context.Background(), testTenant, delivery.ID); !errors.Is(err, ErrProviderRetryable) {
		t.Fatalf("first process error = %v", err)
	}
	afterRetry, _ := repository.Delivery(context.Background(), testTenant, delivery.ID)
	if afterRetry.Status != DeliveryQueued || afterRetry.LastErrorCode != "RATE_LIMITED" {
		t.Fatalf("retry state = %#v", afterRetry)
	}
	if err := service.Process(context.Background(), testTenant, delivery.ID); err != nil {
		t.Fatalf("second process: %v", err)
	}
	sent, _ := repository.Delivery(context.Background(), testTenant, delivery.ID)
	if sent.Status != DeliverySent || sent.ProviderMessageID != "provider-001" {
		t.Fatalf("sent state = %#v", sent)
	}
	receipt := ProviderReceipt{ReceiptID: "receipt-001", ProviderMessageID: "provider-001", Status: DeliveryDelivered, OccurredAt: now.Add(time.Minute)}
	recorded, err := service.RecordReceipt(context.Background(), testTenant, delivery.ID, receipt)
	if err != nil || !recorded {
		t.Fatalf("record receipt = %v, %v", recorded, err)
	}
	recorded, err = service.RecordReceipt(context.Background(), testTenant, delivery.ID, receipt)
	if err != nil || recorded {
		t.Fatalf("duplicate receipt = %v, %v", recorded, err)
	}

	permanent := &scriptedProvider{results: []providerResult{{err: &ProviderError{Code: "INVALID_RECIPIENT", Retryable: false}}}}
	permanentService := mustService(t, repository, map[Channel]Provider{ChannelEmail: permanent}, func() time.Time { return now })
	command := validCommandFixture(PurposeTransactional)
	command.IdempotencyKey = "notify-transactional-002"
	failed, _ := permanentService.Queue(context.Background(), command)
	if err := permanentService.Process(context.Background(), testTenant, failed.ID); err != nil {
		t.Fatalf("permanent provider result should be consumed: %v", err)
	}
	failed, _ = repository.Delivery(context.Background(), testTenant, failed.ID)
	if failed.Status != DeliveryFailed || failed.LastErrorCode != "INVALID_RECIPIENT" {
		t.Fatalf("permanent failure = %#v", failed)
	}
}

func TestNotificationRejectsUnpublishedAndTemplateVariableDrift(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	draft := publishedTemplate(now, "en", "Hello {{name}}", "Plan {{plan}}")
	draft.Status = TemplateDraft
	repository.PutTemplate(draft)
	service := mustService(t, repository, map[Channel]Provider{}, func() time.Time { return now })
	if _, err := service.Queue(context.Background(), validCommandFixture(PurposeTransactional)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft template error = %v", err)
	}
	draft.Status = TemplatePublished
	repository.PutTemplate(draft)
	command := validCommandFixture(PurposeTransactional)
	command.Variables["unexpected"] = "value"
	if _, err := service.Queue(context.Background(), command); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("template drift error = %v", err)
	}
}

func TestNotificationPreferenceConcurrencyAndImmutableTemplatePublishing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	service := mustService(t, repository, map[Channel]Provider{}, func() time.Time { return now })
	preference, err := service.UpdatePreference(context.Background(), testTenant, "customer-1", PurposeMarketing, ChannelPush, true, 0)
	if err != nil || preference.Version != 1 {
		t.Fatalf("create preference = %#v, %v", preference, err)
	}
	if _, err := service.UpdatePreference(context.Background(), testTenant, "customer-1", PurposeMarketing, ChannelPush, false, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preference error = %v", err)
	}
	template := publishedTemplate(now, "en", "Hello {{name}}", "Your plan {{plan}} is ready")
	template.Status, template.PublishedAt = TemplateDraft, nil
	published, err := service.PublishTemplate(context.Background(), template)
	if err != nil || published.Status != TemplatePublished || published.PublishedAt == nil {
		t.Fatalf("publish template = %#v, %v", published, err)
	}
	if _, err := service.PublishTemplate(context.Background(), template); !errors.Is(err, ErrConflict) {
		t.Fatalf("republish immutable version error = %v", err)
	}
}

type providerResult struct {
	receipt ProviderReceipt
	err     error
}

type scriptedProvider struct {
	results []providerResult
	calls   int
}

func (provider *scriptedProvider) Send(_ context.Context, _ ProviderMessage) (ProviderReceipt, error) {
	result := provider.results[provider.calls]
	provider.calls++
	return result.receipt, result.err
}

func validCommandFixture(purpose Purpose) QueueCommand {
	return QueueCommand{TenantID: testTenant, Country: "IN", SubjectID: "customer-1", RecipientRef: "contact-ref-1", Channel: ChannelEmail, Purpose: purpose,
		TemplateKey: "plan-ready", TemplateVersion: 1, Locale: "en", Variables: map[string]string{"name": "Ada", "plan": "Pro"},
		IdempotencyKey: "notify-transactional-001", CorrelationID: "corr-notify-001", CausationID: "cause-notify-001", Traceparent: testTrace}
}

func publishedTemplate(now time.Time, locale, subject, body string) Template {
	return Template{TenantID: testTenant, Country: "IN", Key: "plan-ready", Version: 1, Locale: locale, Channel: ChannelEmail,
		Status: TemplatePublished, Subject: subject, Body: body, Variables: []string{"name", "plan"}, PublishedAt: &now}
}

func mustService(t *testing.T, repository Repository, providers map[Channel]Provider, clock func() time.Time) *Service {
	t.Helper()
	service, err := NewService(repository, providers, clock)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}
