//go:build integration

package notification

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresNotificationQueueReceiptsPreferencesAndEncryptedDevices(t *testing.T) {
	databaseURL := os.Getenv("NOTIFICATION_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("NOTIFICATION_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, schema := range []string{"notification", "messaging"} {
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS notification CASCADE`)
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS messaging CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/messaging/000001_messaging.up.sql",
		"../../migrations/messaging/000002_notification_outbox_writer.up.sql",
		"../../migrations/messaging/000003_notification_schema_usage.up.sql",
		"../../migrations/notification/000001_notification.up.sql",
		"../../migrations/notification/000002_push_devices.up.sql",
		"../../migrations/notification/000003_delivery_data.up.sql",
		"../../migrations/notification/000004_notification_persistence.up.sql",
		"../../migrations/notification/000005_delivery_retry_leases.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	key := bytes.Repeat([]byte{0x64}, 32)
	tokenCipher, err := NewAESGCMTokenCipher(map[int][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewPostgresRepository(pool, tokenCipher)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	provider := &postgresNotificationProvider{}
	service, err := NewService(repository, map[Channel]Provider{ChannelPush: provider}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f"
	template, err := service.PublishTemplate(ctx, Template{
		TenantID: tenantID, Country: "IN", Key: "order-status", Version: 1, Locale: "en",
		Channel: ChannelPush, Body: "Order {{order_id}} is ready", Variables: []string{"order_id"},
	})
	if err != nil || template.Status != TemplatePublished {
		t.Fatalf("template=%#v err=%v", template, err)
	}
	command := QueueCommand{
		TenantID: tenantID, Country: "IN", SubjectID: "customer-postgres", RecipientRef: "device-ref-postgres",
		Channel: ChannelPush, Purpose: PurposeTransactional, TemplateKey: template.Key, TemplateVersion: 1,
		Locale: "en", Variables: map[string]string{"order_id": "order-postgres"},
		Data: map[string]string{"route": "/orders/order-postgres"}, IdempotencyKey: "notification-postgres-001",
		CorrelationID: "notification-postgres", CausationID: "order-postgres",
		Traceparent: "00-00000000000000000000000000000001-0000000000000001-01",
	}
	delivery, err := service.Queue(ctx, command)
	if err != nil || delivery.Status != DeliveryQueued {
		t.Fatalf("delivery=%#v err=%v", delivery, err)
	}
	replay, err := service.Queue(ctx, command)
	if err != nil || replay.ID != delivery.ID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	var deliveryCount, outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification.deliveries WHERE tenant_id = $1`, tenantID).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messaging.outbox WHERE tenant_id = $1`, tenantID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 || outboxCount != 1 {
		t.Fatalf("delivery count=%d outbox count=%d", deliveryCount, outboxCount)
	}
	if err := service.Process(ctx, tenantID, delivery.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Delivery(ctx, tenantID, delivery.ID)
	if err != nil || stored.Status != DeliverySent || provider.calls != 1 {
		t.Fatalf("stored=%#v provider calls=%d err=%v", stored, provider.calls, err)
	}
	receipt := ProviderReceipt{ReceiptID: "receipt-postgres", ProviderMessageID: stored.ProviderMessageID, Status: DeliveryDelivered, OccurredAt: now.Add(time.Second)}
	applied, err := service.RecordReceipt(ctx, tenantID, delivery.ID, receipt)
	if err != nil || !applied {
		t.Fatalf("receipt applied=%v err=%v", applied, err)
	}
	applied, err = service.RecordReceipt(ctx, tenantID, delivery.ID, receipt)
	if err != nil || applied {
		t.Fatalf("duplicate receipt applied=%v err=%v", applied, err)
	}
	preference, err := service.Preference(ctx, tenantID, "customer-postgres", PurposeMarketing, ChannelPush)
	if err != nil || preference.Version != 1 || preference.Enabled {
		t.Fatalf("default preference=%#v err=%v", preference, err)
	}
	preference, err = service.UpdatePreference(ctx, tenantID, "customer-postgres", PurposeMarketing, ChannelPush, true, 1)
	if err != nil || preference.Version != 2 || !preference.Enabled {
		t.Fatalf("updated preference=%#v err=%v", preference, err)
	}
	if _, err := service.UpdatePreference(ctx, tenantID, "customer-postgres", PurposeMarketing, ChannelPush, false, 1); err != ErrConflict {
		t.Fatalf("stale preference error=%v", err)
	}
	deviceToken := "synthetic-fcm-token-postgres-123456789"
	device, err := service.RegisterDevice(ctx, tenantID, "IN", "customer-postgres", "device-postgres", DeviceAndroid, "en", deviceToken)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT token_ciphertext FROM notification.push_device WHERE id = $1`, device.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(deviceToken)) {
		t.Fatal("push token was stored in plaintext")
	}
	devices, err := service.DeviceEndpoints(ctx, tenantID, "IN", "customer-postgres")
	if err != nil || len(devices) != 1 || devices[0].Token != deviceToken {
		t.Fatalf("devices=%#v err=%v", devices, err)
	}
	if _, err := service.UnregisterDevice(ctx, tenantID, "customer-postgres", "device-postgres"); err != nil {
		t.Fatal(err)
	}
	devices, err = service.DeviceEndpoints(ctx, tenantID, "IN", "customer-postgres")
	if err != nil || len(devices) != 0 {
		t.Fatalf("disabled devices=%#v err=%v", devices, err)
	}
}

type postgresNotificationProvider struct{ calls int }

func (provider *postgresNotificationProvider) Send(_ context.Context, message ProviderMessage) (ProviderReceipt, error) {
	provider.calls++
	return ProviderReceipt{ReceiptID: "send-receipt-postgres", ProviderMessageID: "provider-" + message.DeliveryID, Status: DeliverySent}, nil
}
