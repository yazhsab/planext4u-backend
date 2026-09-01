package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/messaging"
)

type PostgresRepository struct {
	pool   *pgxpool.Pool
	cipher TokenCipher
}

func NewPostgresRepository(pool *pgxpool.Pool, tokenCipher TokenCipher) (*PostgresRepository, error) {
	if pool == nil || tokenCipher == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresRepository{pool: pool, cipher: tokenCipher}, nil
}

func (repository *PostgresRepository) Ready(ctx context.Context) error {
	var ready bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT to_regclass('notification.preferences') IS NOT NULL
		   AND to_regclass('notification.consents') IS NOT NULL
		   AND to_regclass('notification.templates') IS NOT NULL
		   AND to_regclass('notification.deliveries') IS NOT NULL
		   AND to_regclass('notification.provider_receipts') IS NOT NULL
		   AND to_regclass('notification.push_device') IS NOT NULL
		   AND to_regclass('messaging.outbox') IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema = 'notification' AND table_name = 'deliveries' AND column_name = 'claim_until'
		   )`).Scan(&ready); err != nil {
		return fmt.Errorf("check notification schema readiness: %w", err)
	}
	if !ready {
		return errors.New("notification schema is unavailable")
	}
	return nil
}

func (repository *PostgresRepository) Preference(ctx context.Context, tenantID, subjectID string, purpose Purpose, channel Channel) (Preference, bool, error) {
	var value Preference
	err := repository.pool.QueryRow(ctx, `
		SELECT tenant_id::text, subject_id, purpose, channel, enabled, version, updated_at
		FROM notification.preferences
		WHERE tenant_id = $1 AND subject_id = $2 AND purpose = $3 AND channel = $4`,
		tenantID, subjectID, purpose, channel).Scan(&value.TenantID, &value.SubjectID, &value.Purpose, &value.Channel, &value.Enabled, &value.Version, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preference{}, false, nil
	}
	if err != nil {
		return Preference{}, false, fmt.Errorf("read notification preference: %w", err)
	}
	return value, true, nil
}

func (repository *PostgresRepository) SavePreference(ctx context.Context, value Preference, expectedVersion int64) error {
	if expectedVersion == 0 {
		_, err := repository.pool.Exec(ctx, `
			INSERT INTO notification.preferences
				(tenant_id, subject_id, purpose, channel, enabled, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, value.TenantID, value.SubjectID, value.Purpose,
			value.Channel, value.Enabled, value.Version, value.UpdatedAt)
		if err != nil {
			if notificationConflict(err) {
				return ErrConflict
			}
			return fmt.Errorf("insert notification preference: %w", err)
		}
		return nil
	}
	result, err := repository.pool.Exec(ctx, `
		UPDATE notification.preferences
		SET enabled = $5, version = $6, updated_at = $7
		WHERE tenant_id = $1 AND subject_id = $2 AND purpose = $3 AND channel = $4 AND version = $8`,
		value.TenantID, value.SubjectID, value.Purpose, value.Channel, value.Enabled,
		value.Version, value.UpdatedAt, expectedVersion)
	if err != nil {
		return fmt.Errorf("update notification preference: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (repository *PostgresRepository) Consent(ctx context.Context, tenantID, subjectID string, purpose Purpose) (Consent, bool, error) {
	var value Consent
	err := repository.pool.QueryRow(ctx, `
		SELECT tenant_id::text, subject_id, purpose, granted, version, updated_at
		FROM notification.consents
		WHERE tenant_id = $1 AND subject_id = $2 AND purpose = $3`, tenantID, subjectID, purpose).
		Scan(&value.TenantID, &value.SubjectID, &value.Purpose, &value.Granted, &value.Version, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Consent{}, false, nil
	}
	if err != nil {
		return Consent{}, false, fmt.Errorf("read notification consent: %w", err)
	}
	return value, true, nil
}

func (repository *PostgresRepository) Template(ctx context.Context, tenantID, country, key string, version int64, locale string, channel Channel) (Template, error) {
	value, err := repository.template(ctx, tenantID, country, key, version, locale, channel)
	if errors.Is(err, pgx.ErrNoRows) && locale != "en" {
		return repository.template(ctx, tenantID, country, key, version, "en", channel)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	return value, err
}

func (repository *PostgresRepository) template(ctx context.Context, tenantID, country, key string, version int64, locale string, channel Channel) (Template, error) {
	var value Template
	var variables []byte
	if err := repository.pool.QueryRow(ctx, `
		SELECT tenant_id::text, country, key, version, locale, channel, subject, body, variables, published_at
		FROM notification.templates
		WHERE tenant_id = $1 AND country = $2 AND key = $3 AND version = $4 AND locale = $5 AND channel = $6`,
		tenantID, country, key, version, locale, channel).Scan(&value.TenantID, &value.Country, &value.Key,
		&value.Version, &value.Locale, &value.Channel, &value.Subject, &value.Body, &variables, &value.PublishedAt); err != nil {
		return Template{}, err
	}
	if err := json.Unmarshal(variables, &value.Variables); err != nil {
		return Template{}, errors.New("stored notification template variables are invalid")
	}
	value.Status = TemplatePublished
	return value, nil
}

func (repository *PostgresRepository) PublishTemplate(ctx context.Context, value Template) error {
	variables, err := json.Marshal(value.Variables)
	if err != nil || value.PublishedAt == nil {
		return ErrInvalidRequest
	}
	_, err = repository.pool.Exec(ctx, `
		INSERT INTO notification.templates
			(tenant_id, country, key, version, locale, channel, subject, body, variables, published_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)`, value.TenantID, value.Country,
		value.Key, value.Version, value.Locale, value.Channel, value.Subject, value.Body, string(variables), *value.PublishedAt)
	if err != nil {
		if notificationConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("publish notification template: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) Queue(ctx context.Context, delivery Delivery, message messaging.Message, idempotencyKey string) (Delivery, bool, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Delivery{}, false, fmt.Errorf("begin notification queue: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	existing, err := deliveryByIdempotency(ctx, transaction, delivery.TenantID, idempotencyKey)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, false, err
	}
	data, err := json.Marshal(delivery.Data)
	if err != nil {
		return Delivery{}, false, ErrInvalidRequest
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO notification.deliveries
			(id, tenant_id, country, subject_id, recipient_ref, channel, purpose,
			 template_key, template_version, locale, rendered_subject, rendered_body,
			 status, suppression_reason, provider_message_id, last_error_code,
			 idempotency_key, created_at, updated_at, data, attempts, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		        $15, $16, $17, $18, $19, $20::jsonb, 0, $18)`, delivery.ID, delivery.TenantID, delivery.Country,
		delivery.SubjectID, delivery.RecipientRef, delivery.Channel, delivery.Purpose, delivery.TemplateKey,
		delivery.TemplateVersion, delivery.Locale, delivery.RenderedSubject, delivery.RenderedBody, delivery.Status,
		nullableNotificationString(delivery.SuppressionReason), nullableNotificationString(delivery.ProviderMessageID),
		nullableNotificationString(delivery.LastErrorCode), idempotencyKey, delivery.CreatedAt, delivery.UpdatedAt, string(data))
	if err != nil {
		if notificationConflict(err) {
			return Delivery{}, false, ErrConflict
		}
		return Delivery{}, false, fmt.Errorf("insert notification delivery: %w", err)
	}
	if delivery.Status == DeliveryQueued {
		envelope, marshalErr := json.Marshal(message)
		if marshalErr != nil {
			return Delivery{}, false, ErrInvalidRequest
		}
		_, err = transaction.Exec(ctx, `
			INSERT INTO messaging.outbox
				(event_id, tenant_id, aggregate_type, aggregate_id, aggregate_version,
				 envelope, state, attempts, next_attempt_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, 'PENDING', 0, $7, $7)`, message.ID,
			message.TenantID, message.AggregateType, message.AggregateID, message.AggregateVersion,
			string(envelope), delivery.CreatedAt)
		if err != nil {
			if notificationConflict(err) {
				return Delivery{}, false, ErrConflict
			}
			return Delivery{}, false, fmt.Errorf("insert notification outbox event: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return Delivery{}, false, fmt.Errorf("commit notification queue: %w", err)
	}
	return cloneDelivery(delivery), true, nil
}

func (repository *PostgresRepository) Delivery(ctx context.Context, tenantID, id string) (Delivery, error) {
	value, err := scanPostgresDelivery(repository.pool.QueryRow(ctx, deliverySelect+` WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("read notification delivery: %w", err)
	}
	return value, nil
}

func (repository *PostgresRepository) QueuedDeliveryIDs(ctx context.Context, limit int) ([]struct{ TenantID, ID string }, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	if _, err := repository.pool.Exec(ctx, `
		UPDATE notification.deliveries
		SET status = CASE WHEN attempts >= 19 THEN 'FAILED' ELSE 'QUEUED' END,
		    attempts = LEAST(attempts + 1, 20),
		    last_error_code = 'DELIVERY_LEASE_EXPIRED', claim_until = NULL,
		    next_attempt_at = now() + (LEAST((1::bigint << LEAST(attempts, 8)), 300) * interval '1 second'),
		    updated_at = now()
		WHERE status = 'SENDING' AND claim_until <= now()`); err != nil {
		return nil, fmt.Errorf("recover expired notification claims: %w", err)
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT tenant_id::text, id::text
		FROM notification.deliveries
		WHERE status = 'QUEUED' AND next_attempt_at <= now()
		ORDER BY created_at, id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list queued notification deliveries: %w", err)
	}
	defer rows.Close()
	result := make([]struct{ TenantID, ID string }, 0, limit)
	for rows.Next() {
		var value struct{ TenantID, ID string }
		if err := rows.Scan(&value.TenantID, &value.ID); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (repository *PostgresRepository) ClaimDelivery(ctx context.Context, tenantID, id string, claimedAt time.Time) (Delivery, bool, error) {
	value, err := scanPostgresDelivery(repository.pool.QueryRow(ctx, `
		UPDATE notification.deliveries SET status = 'SENDING', updated_at = $3::timestamptz, claim_until = $3::timestamptz + interval '1 minute'
		WHERE tenant_id = $1 AND id = $2 AND status = 'QUEUED'
		RETURNING id::text, tenant_id::text, country, subject_id, recipient_ref, channel,
		          purpose, template_key, template_version, locale, rendered_subject,
		          rendered_body, data, status, suppression_reason, provider_message_id,
		          last_error_code, created_at, updated_at`, tenantID, id, claimedAt.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		current, currentErr := repository.Delivery(ctx, tenantID, id)
		if currentErr != nil {
			return Delivery{}, false, currentErr
		}
		return current, false, nil
	}
	if err != nil {
		return Delivery{}, false, fmt.Errorf("claim notification delivery: %w", err)
	}
	return value, true, nil
}

func (repository *PostgresRepository) UpdateDelivery(ctx context.Context, value Delivery) error {
	data, err := json.Marshal(value.Data)
	if err != nil {
		return ErrInvalidRequest
	}
	result, err := repository.pool.Exec(ctx, `
		UPDATE notification.deliveries
		SET status = $3, suppression_reason = $4, provider_message_id = $5,
		    last_error_code = $6, updated_at = $7, data = $8::jsonb,
		    attempts = CASE WHEN $3 = 'QUEUED' THEN LEAST(attempts + 1, 20) ELSE attempts END,
		    next_attempt_at = CASE WHEN $3 = 'QUEUED'
		        THEN $7::timestamptz + (LEAST((1::bigint << LEAST(attempts, 8)), 300) * interval '1 second')
		        ELSE next_attempt_at END,
		    claim_until = NULL
		WHERE tenant_id = $1 AND id = $2 AND status = 'SENDING'`, value.TenantID, value.ID, value.Status,
		nullableNotificationString(value.SuppressionReason), nullableNotificationString(value.ProviderMessageID),
		nullableNotificationString(value.LastErrorCode), value.UpdatedAt, string(data))
	if err != nil {
		return fmt.Errorf("update notification delivery: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (repository *PostgresRepository) RecordReceipt(ctx context.Context, tenantID, deliveryID string, receipt ProviderReceipt) (bool, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, fmt.Errorf("begin notification receipt: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status DeliveryStatus
	if err := transaction.QueryRow(ctx, `
		SELECT status FROM notification.deliveries WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenantID, deliveryID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("lock notification delivery: %w", err)
	}
	if status == DeliveryDelivered || status == DeliveryFailed || status == DeliverySuppressed {
		return false, nil
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO notification.provider_receipts
			(tenant_id, receipt_id, delivery_id, provider_message_id, status, error_code, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, tenantID, receipt.ReceiptID, deliveryID,
		receipt.ProviderMessageID, receipt.Status, nullableNotificationString(receipt.ErrorCode), receipt.OccurredAt.UTC())
	if err != nil {
		if notificationConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("insert notification receipt: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE notification.deliveries
		SET status = $3, provider_message_id = $4, last_error_code = $5, updated_at = $6
		WHERE tenant_id = $1 AND id = $2`, tenantID, deliveryID, receipt.Status,
		receipt.ProviderMessageID, nullableNotificationString(receipt.ErrorCode), receipt.OccurredAt.UTC()); err != nil {
		return false, fmt.Errorf("apply notification receipt: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit notification receipt: %w", err)
	}
	return true, nil
}

func (repository *PostgresRepository) SaveDevice(ctx context.Context, value DeviceEndpoint) error {
	if value.Token == "" {
		result, err := repository.pool.Exec(ctx, `
			UPDATE notification.push_device
			SET country = $3, device_reference = $4, platform = $5, locale = $6,
			    enabled = $7, updated_at = $8
			WHERE id = $1 AND tenant_id = $2 AND subject_id = $9`, value.ID, value.TenantID,
			value.Country, value.DeviceReference, value.Platform, value.Locale, value.Enabled,
			value.UpdatedAt, value.SubjectID)
		if err != nil {
			return fmt.Errorf("disable push device: %w", err)
		}
		if result.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	}
	ciphertext, keyVersion, err := repository.cipher.Encrypt(value.TenantID, value.ID, value.Token)
	if err != nil {
		return err
	}
	result, err := repository.pool.Exec(ctx, `
		INSERT INTO notification.push_device
			(id, tenant_id, country, subject_id, device_reference, platform, locale,
			 token_ciphertext, token_key_version, enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE
		SET country = EXCLUDED.country, device_reference = EXCLUDED.device_reference,
		    platform = EXCLUDED.platform, locale = EXCLUDED.locale,
		    token_ciphertext = EXCLUDED.token_ciphertext, token_key_version = EXCLUDED.token_key_version,
		    enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at
		WHERE notification.push_device.tenant_id = EXCLUDED.tenant_id
		  AND notification.push_device.subject_id = EXCLUDED.subject_id`,
		value.ID, value.TenantID, value.Country, value.SubjectID, value.DeviceReference,
		value.Platform, value.Locale, ciphertext, keyVersion, value.Enabled, value.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save push device: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (repository *PostgresRepository) Device(ctx context.Context, tenantID, id string) (DeviceEndpoint, error) {
	value, err := repository.scanDevice(repository.pool.QueryRow(ctx, deviceSelect+` WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceEndpoint{}, ErrNotFound
	}
	return value, err
}

func (repository *PostgresRepository) Devices(ctx context.Context, tenantID, country, subjectID string) ([]DeviceEndpoint, error) {
	rows, err := repository.pool.Query(ctx, deviceSelect+` WHERE tenant_id = $1 AND country = $2 AND subject_id = $3 AND enabled ORDER BY updated_at DESC`, tenantID, country, subjectID)
	if err != nil {
		return nil, fmt.Errorf("list push devices: %w", err)
	}
	defer rows.Close()
	result := []DeviceEndpoint{}
	for rows.Next() {
		value, scanErr := repository.scanDevice(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

type notificationRow interface {
	Scan(...any) error
}

const deliverySelect = `SELECT id::text, tenant_id::text, country, subject_id, recipient_ref, channel,
	purpose, template_key, template_version, locale, rendered_subject, rendered_body,
	data, status, suppression_reason, provider_message_id, last_error_code, created_at, updated_at
	FROM notification.deliveries`

func deliveryByIdempotency(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenantID, key string) (Delivery, error) {
	return scanPostgresDelivery(querier.QueryRow(ctx, deliverySelect+` WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, key))
}

func scanPostgresDelivery(row notificationRow) (Delivery, error) {
	var value Delivery
	var data []byte
	var suppression, providerID, lastError *string
	if err := row.Scan(&value.ID, &value.TenantID, &value.Country, &value.SubjectID, &value.RecipientRef,
		&value.Channel, &value.Purpose, &value.TemplateKey, &value.TemplateVersion, &value.Locale,
		&value.RenderedSubject, &value.RenderedBody, &data, &value.Status, &suppression,
		&providerID, &lastError, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return Delivery{}, err
	}
	if err := json.Unmarshal(data, &value.Data); err != nil {
		return Delivery{}, errors.New("stored notification data is invalid")
	}
	if suppression != nil {
		value.SuppressionReason = *suppression
	}
	if providerID != nil {
		value.ProviderMessageID = *providerID
	}
	if lastError != nil {
		value.LastErrorCode = *lastError
	}
	return value, nil
}

const deviceSelect = `SELECT id, tenant_id::text, country, subject_id, device_reference,
	platform, locale, token_ciphertext, token_key_version, enabled, updated_at FROM notification.push_device`

func (repository *PostgresRepository) scanDevice(row notificationRow) (DeviceEndpoint, error) {
	var value DeviceEndpoint
	var ciphertext []byte
	var keyVersion int
	if err := row.Scan(&value.ID, &value.TenantID, &value.Country, &value.SubjectID,
		&value.DeviceReference, &value.Platform, &value.Locale, &ciphertext, &keyVersion,
		&value.Enabled, &value.UpdatedAt); err != nil {
		return DeviceEndpoint{}, err
	}
	token, err := repository.cipher.Decrypt(value.TenantID, value.ID, ciphertext, keyVersion)
	if err != nil {
		return DeviceEndpoint{}, err
	}
	value.Token = token
	return value, nil
}

func nullableNotificationString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func notificationConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

func validNotificationUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

var _ Repository = (*PostgresRepository)(nil)
