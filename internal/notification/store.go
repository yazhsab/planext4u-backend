package notification

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/yazhsab/planext4u-backend/internal/messaging"
)

var (
	ErrInvalidRequest    = errors.New("invalid notification request")
	ErrNotFound          = errors.New("notification resource not found")
	ErrConflict          = errors.New("notification resource conflict")
	ErrProviderRetryable = errors.New("notification provider temporarily unavailable")
)

type Repository interface {
	Preference(context.Context, string, string, Purpose, Channel) (Preference, bool, error)
	SavePreference(context.Context, Preference, int64) error
	Consent(context.Context, string, string, Purpose) (Consent, bool, error)
	Template(context.Context, string, string, string, int64, string, Channel) (Template, error)
	PublishTemplate(context.Context, Template) error
	Queue(context.Context, Delivery, messaging.Message, string) (Delivery, bool, error)
	Delivery(context.Context, string, string) (Delivery, error)
	UpdateDelivery(context.Context, Delivery) error
	RecordReceipt(context.Context, string, string, ProviderReceipt) (bool, error)
}

type MemoryRepository struct {
	mu          sync.Mutex
	preferences map[string]Preference
	consents    map[string]Consent
	templates   map[string]Template
	deliveries  map[string]Delivery
	idempotency map[string]string
	receipts    map[string]bool
	messages    []messaging.Message
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{preferences: map[string]Preference{}, consents: map[string]Consent{}, templates: map[string]Template{}, deliveries: map[string]Delivery{}, idempotency: map[string]string{}, receipts: map[string]bool{}}
}

func (repository *MemoryRepository) PutPreference(value Preference) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.preferences[preferenceKey(value.TenantID, value.SubjectID, value.Purpose, value.Channel)] = value
}

func (repository *MemoryRepository) PutConsent(value Consent) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.consents[consentKey(value.TenantID, value.SubjectID, value.Purpose)] = value
}

func (repository *MemoryRepository) PutTemplate(value Template) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.templates[templateKey(value.TenantID, value.Country, value.Key, value.Version, value.Locale, value.Channel)] = cloneTemplate(value)
}

func (repository *MemoryRepository) Preference(_ context.Context, tenantID, subjectID string, purpose Purpose, channel Channel) (Preference, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.preferences[preferenceKey(tenantID, subjectID, purpose, channel)]
	return value, exists, nil
}

func (repository *MemoryRepository) SavePreference(_ context.Context, value Preference, expectedVersion int64) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := preferenceKey(value.TenantID, value.SubjectID, value.Purpose, value.Channel)
	current, exists := repository.preferences[key]
	if (!exists && expectedVersion != 0) || (exists && current.Version != expectedVersion) {
		return ErrConflict
	}
	repository.preferences[key] = value
	return nil
}

func (repository *MemoryRepository) Consent(_ context.Context, tenantID, subjectID string, purpose Purpose) (Consent, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.consents[consentKey(tenantID, subjectID, purpose)]
	return value, exists, nil
}

func (repository *MemoryRepository) Template(_ context.Context, tenantID, country, key string, version int64, locale string, channel Channel) (Template, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.templates[templateKey(tenantID, country, key, version, locale, channel)]
	if !exists && locale != "en" {
		value, exists = repository.templates[templateKey(tenantID, country, key, version, "en", channel)]
	}
	if !exists {
		return Template{}, ErrNotFound
	}
	return cloneTemplate(value), nil
}

func (repository *MemoryRepository) PublishTemplate(_ context.Context, value Template) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := templateKey(value.TenantID, value.Country, value.Key, value.Version, value.Locale, value.Channel)
	if _, exists := repository.templates[key]; exists {
		return ErrConflict
	}
	repository.templates[key] = cloneTemplate(value)
	return nil
}

// Queue is the in-memory equivalent of the production transaction that inserts
// both the delivery and its outbox message before committing.
func (repository *MemoryRepository) Queue(_ context.Context, delivery Delivery, message messaging.Message, idempotencyKey string) (Delivery, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := delivery.TenantID + "\x00" + idempotencyKey
	if id, exists := repository.idempotency[key]; exists {
		return repository.deliveries[id], false, nil
	}
	if _, exists := repository.deliveries[delivery.ID]; exists {
		return Delivery{}, false, ErrConflict
	}
	repository.deliveries[delivery.ID], repository.idempotency[key] = delivery, delivery.ID
	if delivery.Status == DeliveryQueued {
		repository.messages = append(repository.messages, message)
	}
	return delivery, true, nil
}

func (repository *MemoryRepository) Delivery(_ context.Context, tenantID, id string) (Delivery, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.deliveries[id]
	if !exists || value.TenantID != tenantID {
		return Delivery{}, ErrNotFound
	}
	return value, nil
}

func (repository *MemoryRepository) UpdateDelivery(_ context.Context, value Delivery) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, exists := repository.deliveries[value.ID]
	if !exists || current.TenantID != value.TenantID {
		return ErrNotFound
	}
	repository.deliveries[value.ID] = value
	return nil
}

func (repository *MemoryRepository) RecordReceipt(_ context.Context, tenantID, deliveryID string, receipt ProviderReceipt) (bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	delivery, exists := repository.deliveries[deliveryID]
	if !exists || delivery.TenantID != tenantID {
		return false, ErrNotFound
	}
	key := tenantID + "\x00" + receipt.ReceiptID
	if repository.receipts[key] {
		return false, nil
	}
	if delivery.Status == DeliveryDelivered || delivery.Status == DeliveryFailed || delivery.Status == DeliverySuppressed {
		return false, nil
	}
	repository.receipts[key] = true
	delivery.Status, delivery.ProviderMessageID, delivery.LastErrorCode, delivery.UpdatedAt = receipt.Status, receipt.ProviderMessageID, receipt.ErrorCode, receipt.OccurredAt.UTC()
	repository.deliveries[deliveryID] = delivery
	return true, nil
}

func (repository *MemoryRepository) PendingMessages() []messaging.Message {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]messaging.Message, len(repository.messages))
	copy(result, repository.messages)
	return result
}

func preferenceKey(tenantID, subjectID string, purpose Purpose, channel Channel) string {
	return tenantID + "\x00" + subjectID + "\x00" + string(purpose) + "\x00" + string(channel)
}
func consentKey(tenantID, subjectID string, purpose Purpose) string {
	return tenantID + "\x00" + subjectID + "\x00" + string(purpose)
}
func templateKey(tenantID, country, key string, version int64, locale string, channel Channel) string {
	return tenantID + "\x00" + country + "\x00" + key + "\x00" + strconv.FormatInt(version, 10) + "\x00" + locale + "\x00" + string(channel)
}
func cloneTemplate(value Template) Template {
	value.Variables = append([]string(nil), value.Variables...)
	return value
}
