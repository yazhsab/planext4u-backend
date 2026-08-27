package notification

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/messaging"
)

type Provider interface {
	Send(context.Context, ProviderMessage) (ProviderReceipt, error)
}

type Service struct {
	repository Repository
	providers  map[Channel]Provider
	clock      func() time.Time
}

func NewService(repository Repository, providers map[Channel]Provider, clock func() time.Time) (*Service, error) {
	if repository == nil || providers == nil || clock == nil {
		return nil, errors.New("invalid notification service")
	}
	return &Service{repository: repository, providers: providers, clock: clock}, nil
}

func (service *Service) UpdatePreference(ctx context.Context, tenantID, subjectID string, purpose Purpose, channel Channel, enabled bool, expectedVersion int64) (Preference, error) {
	if !uuidPattern.MatchString(tenantID) || !safeID(subjectID) || !validPurpose(purpose) || !validChannel(channel) || expectedVersion < 0 {
		return Preference{}, ErrInvalidRequest
	}
	preference := Preference{TenantID: tenantID, SubjectID: subjectID, Purpose: purpose, Channel: channel, Enabled: enabled, Version: expectedVersion + 1, UpdatedAt: service.clock().UTC()}
	if err := service.repository.SavePreference(ctx, preference, expectedVersion); err != nil {
		return Preference{}, err
	}
	return preference, nil
}

func (service *Service) PublishTemplate(ctx context.Context, template Template) (Template, error) {
	if !validTemplate(template) {
		return Template{}, ErrInvalidRequest
	}
	now := service.clock().UTC()
	template.Status, template.PublishedAt = TemplatePublished, &now
	if err := service.repository.PublishTemplate(ctx, template); err != nil {
		return Template{}, err
	}
	return template, nil
}

func (service *Service) Queue(ctx context.Context, command QueueCommand) (Delivery, error) {
	if !validCommand(command) {
		return Delivery{}, ErrInvalidRequest
	}
	template, err := service.repository.Template(ctx, command.TenantID, command.Country, command.TemplateKey, command.TemplateVersion, command.Locale, command.Channel)
	if err != nil || template.Status != TemplatePublished {
		return Delivery{}, ErrNotFound
	}
	subject, body, err := render(template, command.Variables)
	if err != nil {
		return Delivery{}, err
	}
	now := service.clock().UTC()
	delivery := Delivery{ID: newUUID(), TenantID: command.TenantID, Country: command.Country, SubjectID: command.SubjectID, RecipientRef: command.RecipientRef,
		Channel: command.Channel, Purpose: command.Purpose, TemplateKey: command.TemplateKey, TemplateVersion: template.Version, Locale: template.Locale,
		RenderedSubject: subject, RenderedBody: body, Status: DeliveryQueued, CreatedAt: now, UpdatedAt: now}
	allowed, reason, err := service.allowed(ctx, command)
	if err != nil {
		return Delivery{}, err
	}
	if !allowed {
		delivery.Status, delivery.SuppressionReason = DeliverySuppressed, reason
	}
	message := messaging.Message{ID: newUUID(), TenantID: command.TenantID, Country: command.Country, Producer: "notification-service",
		EventType: "planext4u.notification.delivery.requested.v1", SchemaVersion: 1, AggregateType: "notification-delivery", AggregateID: delivery.ID,
		AggregateVersion: 1, CorrelationID: command.CorrelationID, CausationID: command.CausationID, Traceparent: command.Traceparent,
		Classification: "CONFIDENTIAL", OccurredAt: now, Data: map[string]any{"delivery_id": delivery.ID, "channel": string(delivery.Channel)}}
	stored, _, err := service.repository.Queue(ctx, delivery, message, command.IdempotencyKey)
	return stored, err
}

func (service *Service) Process(ctx context.Context, tenantID, deliveryID string) error {
	delivery, err := service.repository.Delivery(ctx, tenantID, deliveryID)
	if err != nil {
		return err
	}
	if delivery.Status == DeliverySent || delivery.Status == DeliveryDelivered || delivery.Status == DeliverySuppressed {
		return nil
	}
	if delivery.Status != DeliveryQueued {
		return ErrConflict
	}
	provider := service.providers[delivery.Channel]
	if provider == nil {
		return ErrInvalidRequest
	}
	delivery.Status, delivery.UpdatedAt = DeliverySending, service.clock().UTC()
	if err := service.repository.UpdateDelivery(ctx, delivery); err != nil {
		return err
	}
	receipt, sendErr := provider.Send(ctx, ProviderMessage{DeliveryID: delivery.ID, RecipientRef: delivery.RecipientRef, Subject: delivery.RenderedSubject, Body: delivery.RenderedBody})
	if sendErr != nil {
		providerErr := &ProviderError{}
		if errors.As(sendErr, &providerErr) {
			delivery.LastErrorCode = safeErrorCode(providerErr.Code)
			if providerErr.Retryable {
				delivery.Status, delivery.UpdatedAt = DeliveryQueued, service.clock().UTC()
				_ = service.repository.UpdateDelivery(ctx, delivery)
				return ErrProviderRetryable
			}
		} else {
			delivery.LastErrorCode = "PROVIDER_FAILED"
		}
		delivery.Status, delivery.UpdatedAt = DeliveryFailed, service.clock().UTC()
		return service.repository.UpdateDelivery(ctx, delivery)
	}
	if receipt.Status != DeliverySent && receipt.Status != DeliveryDelivered {
		return ErrInvalidRequest
	}
	delivery.Status, delivery.ProviderMessageID, delivery.LastErrorCode, delivery.UpdatedAt = receipt.Status, receipt.ProviderMessageID, "", service.clock().UTC()
	return service.repository.UpdateDelivery(ctx, delivery)
}

func (service *Service) RecordReceipt(ctx context.Context, tenantID, deliveryID string, receipt ProviderReceipt) (bool, error) {
	if !uuidPattern.MatchString(tenantID) || !uuidPattern.MatchString(deliveryID) || !safeID(receipt.ReceiptID) || !safeID(receipt.ProviderMessageID) ||
		(receipt.Status != DeliveryDelivered && receipt.Status != DeliveryFailed) || receipt.OccurredAt.IsZero() || (receipt.Status == DeliveryFailed && !safeID(receipt.ErrorCode)) {
		return false, ErrInvalidRequest
	}
	return service.repository.RecordReceipt(ctx, tenantID, deliveryID, receipt)
}

func (service *Service) allowed(ctx context.Context, command QueueCommand) (bool, string, error) {
	if command.Purpose == PurposeSecurity {
		return true, "", nil
	}
	preference, exists, err := service.repository.Preference(ctx, command.TenantID, command.SubjectID, command.Purpose, command.Channel)
	if err != nil {
		return false, "", err
	}
	if command.Purpose == PurposeMarketing {
		consent, consentExists, consentErr := service.repository.Consent(ctx, command.TenantID, command.SubjectID, command.Purpose)
		if consentErr != nil {
			return false, "", consentErr
		}
		if !consentExists || !consent.Granted {
			return false, "CONSENT_NOT_GRANTED", nil
		}
		if !exists || !preference.Enabled {
			return false, "PREFERENCE_DISABLED", nil
		}
		return true, "", nil
	}
	if exists && !preference.Enabled {
		return false, "PREFERENCE_DISABLED", nil
	}
	return true, "", nil
}

func render(template Template, values map[string]string) (string, string, error) {
	if len(values) != len(template.Variables) {
		return "", "", ErrInvalidRequest
	}
	subject, body := template.Subject, template.Body
	for _, name := range template.Variables {
		value, exists := values[name]
		if !exists || len(value) > 2_000 || strings.ContainsAny(value, "\x00") {
			return "", "", ErrInvalidRequest
		}
		subject = strings.ReplaceAll(subject, "{{"+name+"}}", value)
		body = strings.ReplaceAll(body, "{{"+name+"}}", value)
	}
	if placeholderPattern.MatchString(subject) || placeholderPattern.MatchString(body) || strings.ContainsAny(subject, "\r\n") || len(subject) > 240 || len(body) > 32_000 {
		return "", "", ErrInvalidRequest
	}
	return subject, body, nil
}

func validCommand(command QueueCommand) bool {
	return uuidPattern.MatchString(command.TenantID) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(command.Country) && safeID(command.SubjectID) && safeID(command.RecipientRef) &&
		validChannel(command.Channel) && validPurpose(command.Purpose) && safeID(command.TemplateKey) && command.TemplateVersion > 0 && command.TemplateVersion <= 1_000_000 &&
		regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`).MatchString(command.Locale) && safeID(command.IdempotencyKey) && safeID(command.CorrelationID) && safeID(command.CausationID) &&
		regexp.MustCompile(`^00-[a-f0-9]{32}-[a-f0-9]{16}-[0-9a-f]{2}$`).MatchString(command.Traceparent) && command.Variables != nil
}

func validTemplate(template Template) bool {
	if !uuidPattern.MatchString(template.TenantID) || !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(template.Country) || !safeID(template.Key) ||
		template.Version < 1 || template.Version > 1_000_000 || !regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`).MatchString(template.Locale) ||
		!validChannel(template.Channel) || (template.Status != "" && template.Status != TemplateDraft) || template.PublishedAt != nil || len(template.Subject) > 240 || len(template.Body) == 0 || len(template.Body) > 32_000 || strings.ContainsAny(template.Subject, "\r\n") {
		return false
	}
	allowed := make(map[string]bool, len(template.Variables))
	for _, variable := range template.Variables {
		if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`).MatchString(variable) || allowed[variable] {
			return false
		}
		allowed[variable] = true
	}
	content := template.Subject + "\n" + template.Body
	for _, match := range placeholderPattern.FindAllString(content, -1) {
		if !allowed[strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")] {
			return false
		}
	}
	withoutPlaceholders := placeholderPattern.ReplaceAllString(content, "")
	if strings.Contains(withoutPlaceholders, "{{") || strings.Contains(withoutPlaceholders, "}}") {
		return false
	}
	for variable := range allowed {
		if !strings.Contains(content, "{{"+variable+"}}") {
			return false
		}
	}
	return true
}

func validChannel(value Channel) bool {
	return value == ChannelEmail || value == ChannelPush || value == ChannelWhatsApp || value == ChannelInApp
}
func validPurpose(value Purpose) bool {
	return value == PurposeSecurity || value == PurposeTransactional || value == PurposeMarketing
}

func safeErrorCode(value string) string {
	if safeID(value) {
		return value
	}
	return "PROVIDER_FAILED"
}

func newUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("secure random source unavailable: %v", err))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

var (
	uuidPattern        = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	safeIDPattern      = regexp.MustCompile(`^[A-Za-z0-9._:@+-]{1,256}$`)
	placeholderPattern = regexp.MustCompile(`\{\{[A-Za-z][A-Za-z0-9_]{0,63}\}\}`)
)

func safeID(value string) bool { return safeIDPattern.MatchString(value) }
