package notification

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/order"
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

func TestNotificationDeliveryIsClaimedOnceAcrossConcurrentWorkers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.PutTemplate(publishedTemplate(now, "en", "Hello {{name}}", "Your plan {{plan}} is ready"))
	provider := &countingProvider{}
	service := mustService(t, repository, map[Channel]Provider{ChannelEmail: provider}, func() time.Time { return now })
	delivery, err := service.Queue(context.Background(), validCommandFixture(PurposeTransactional))
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	start := make(chan struct{})
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			errorsSeen <- service.Process(context.Background(), testTenant, delivery.ID)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)

	successes, conflicts := 0, 0
	for processErr := range errorsSeen {
		switch {
		case processErr == nil:
			successes++
		case errors.Is(processErr, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected process error: %v", processErr)
		}
	}
	if successes != 1 || conflicts != workers-1 || provider.calls.Load() != 1 {
		t.Fatalf("successes=%d conflicts=%d provider calls=%d", successes, conflicts, provider.calls.Load())
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

func TestDeviceRegistrationIsScopedUpsertableAndNeverReturnsToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	service := mustService(t, repository, map[Channel]Provider{}, func() time.Time { return now })
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	token := "fcm_registration_token_synthetic_000000000001"
	request := httptest.NewRequest(http.MethodPut, "/v1/notifications/devices/current", bytes.NewBufferString(`{"platform":"ANDROID","locale":"en","token":"`+token+`"}`))
	request.Header.Set("X-Planext4u-Tenant", testTenant)
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "customer-1")
	request.Header.Set("X-Planext4u-Device", "device-1")
	request.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), token) || !strings.Contains(response.Body.String(), `"device_reference"`) {
		t.Fatalf("registration status=%d body=%s", response.Code, response.Body.String())
	}
	endpoints, err := service.DeviceEndpoints(context.Background(), testTenant, "IN", "customer-1")
	if err != nil || len(endpoints) != 1 || endpoints[0].Token != token || !endpoints[0].Enabled {
		t.Fatalf("endpoints=%#v err=%v", endpoints, err)
	}
	request = httptest.NewRequest(http.MethodDelete, "/v1/notifications/devices/current", nil)
	request.Header.Set("X-Planext4u-Tenant", testTenant)
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "customer-1")
	request.Header.Set("X-Planext4u-Device", "device-1")
	request.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("unregister status=%d body=%s", response.Code, response.Body.String())
	}
	endpoints, _ = service.DeviceEndpoints(context.Background(), testTenant, "IN", "customer-1")
	if len(endpoints) != 0 {
		t.Fatalf("active endpoints=%#v", endpoints)
	}
}

func TestOrderNotifierFansOutRegisteredDevicesWithIdempotentOutbox(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.PutTemplate(Template{TenantID: testTenant, Country: "IN", Key: "order-status", Version: 1, Locale: "en", Channel: ChannelPush, Status: TemplatePublished, Body: "Order {{order_id}} is now {{status}}.", Variables: []string{"order_id", "status"}, PublishedAt: &now})
	provider := &scriptedProvider{results: []providerResult{{receipt: ProviderReceipt{ProviderMessageID: "fcm-message-001", Status: DeliverySent}}}}
	service := mustService(t, repository, map[Channel]Provider{ChannelPush: provider}, func() time.Time { return now })
	if _, err := service.RegisterDevice(context.Background(), testTenant, "IN", "customer-1", "device-1", DeviceAndroid, "en", "fcm_registration_token_synthetic_000000000001"); err != nil {
		t.Fatal(err)
	}
	notifier, _ := NewOrderNotifier(service, 1)
	value := order.Notification{ID: "notification-order-001", TenantID: testTenant, Country: "IN", CustomerID: "customer-1", OrderID: "order-001", Status: order.StatusPlaced, Revision: 1, CreatedAt: now}
	if err := notifier.Send(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := notifier.Send(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if messages := repository.PendingMessages(); len(messages) != 1 || messages[0].AggregateType != "notification-delivery" {
		t.Fatalf("outbox messages=%#v", messages)
	}
	if provider.calls != 1 || provider.messages[0].Data["deep_link"] != "/app/orders/order-001" {
		t.Fatalf("provider calls=%d messages=%#v", provider.calls, provider.messages)
	}
}

type providerResult struct {
	receipt ProviderReceipt
	err     error
}

type scriptedProvider struct {
	results  []providerResult
	calls    int
	messages []ProviderMessage
}

type countingProvider struct{ calls atomic.Int32 }

func (provider *countingProvider) Send(_ context.Context, _ ProviderMessage) (ProviderReceipt, error) {
	provider.calls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return ProviderReceipt{ProviderMessageID: "provider-once", Status: DeliverySent}, nil
}

func (provider *scriptedProvider) Send(_ context.Context, message ProviderMessage) (ProviderReceipt, error) {
	provider.messages = append(provider.messages, message)
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
