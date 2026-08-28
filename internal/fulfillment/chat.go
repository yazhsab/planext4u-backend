package fulfillment

import (
	"regexp"
	"strings"
)

func (service *Service) Conversation(actor Actor, orderID string) (Conversation, error) {
	if !validActor(actor) {
		return Conversation{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.conversations[service.conversationOrder[orderID]]
	if value == nil {
		return Conversation{}, ErrNotFound
	}
	if err := authorizeConversation(actor, value); err != nil {
		return Conversation{}, err
	}
	return cloneConversation(*value), nil
}

func (service *Service) SendMessage(actor Actor, key, conversationID, body string) (ChatMessage, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "CUSTOMER", "RIDER", "RESTAURANT_VENDOR") || !validKey(key) || len(strings.TrimSpace(body)) == 0 || len(body) > 1000 {
		return ChatMessage{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	conversation := service.conversations[conversationID]
	if conversation == nil {
		return ChatMessage{}, false, ErrNotFound
	}
	if err := authorizeConversation(actor, conversation); err != nil {
		return ChatMessage{}, false, err
	}
	if conversation.Blocked {
		return ChatMessage{}, false, ErrForbidden
	}
	if !conversation.ExpiresAt.After(service.clock().UTC()) {
		return ChatMessage{}, false, ErrChatExpired
	}
	fingerprint := digest(body)
	scope := idempotencyScope(actor, "chat:"+conversationID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return ChatMessage{}, false, ErrIdempotencyConflict
		}
		for _, message := range conversation.Messages {
			if message.ID == previous.resourceID {
				return cloneMessage(message), true, nil
			}
		}
	}
	cleaned, redacted := redactContact(strings.TrimSpace(body))
	service.sequence++
	message := ChatMessage{ID: "chat-message-" + sequenceID(service.sequence), SenderID: actor.Subject, Body: cleaned, Redacted: redacted, CreatedAt: service.clock().UTC(), Receipts: []MessageReceipt{{ActorID: actor.Subject, State: "SENT", At: service.clock().UTC()}}}
	conversation.Messages = append(conversation.Messages, message)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: message.ID}
	return cloneMessage(message), false, nil
}

func (service *Service) MessageReceipt(actor Actor, key, conversationID, messageID, state string) (ChatMessage, bool, error) {
	if !validActor(actor) || !validKey(key) || (state != "DELIVERED" && state != "READ") {
		return ChatMessage{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	conversation := service.conversations[conversationID]
	if conversation == nil {
		return ChatMessage{}, false, ErrNotFound
	}
	if err := authorizeConversation(actor, conversation); err != nil {
		return ChatMessage{}, false, err
	}
	if !conversation.ExpiresAt.After(service.clock().UTC()) {
		return ChatMessage{}, false, ErrChatExpired
	}
	scope := idempotencyScope(actor, "receipt:"+messageID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != digest(state) {
			return ChatMessage{}, false, ErrIdempotencyConflict
		}
		for _, message := range conversation.Messages {
			if message.ID == messageID {
				return cloneMessage(message), true, nil
			}
		}
	}
	for index := range conversation.Messages {
		message := &conversation.Messages[index]
		if message.ID != messageID {
			continue
		}
		for receiptIndex := range message.Receipts {
			if message.Receipts[receiptIndex].ActorID == actor.Subject {
				message.Receipts[receiptIndex].State, message.Receipts[receiptIndex].At = state, service.clock().UTC()
				service.idempotency[scope] = idempotentResult{fingerprint: digest(state), resourceID: messageID}
				return cloneMessage(*message), false, nil
			}
		}
		message.Receipts = append(message.Receipts, MessageReceipt{ActorID: actor.Subject, State: state, At: service.clock().UTC()})
		service.idempotency[scope] = idempotentResult{fingerprint: digest(state), resourceID: messageID}
		return cloneMessage(*message), false, nil
	}
	return ChatMessage{}, false, ErrNotFound
}

func (service *Service) BlockConversation(actor Actor, key, conversationID, reason string) (Conversation, bool, error) {
	if !validActor(actor) || !validKey(key) || len(strings.TrimSpace(reason)) < 8 {
		return Conversation{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.conversations[conversationID]
	if value == nil {
		return Conversation{}, false, ErrNotFound
	}
	if err := authorizeConversation(actor, value); err != nil {
		return Conversation{}, false, err
	}
	scope := idempotencyScope(actor, "chat-block:"+conversationID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != digest(reason) {
			return Conversation{}, false, ErrIdempotencyConflict
		}
		return cloneConversation(*value), true, nil
	}
	value.Blocked = true
	service.recordAuditLocked(actor, "CONVERSATION_BLOCKED", "CONVERSATION", conversationID, reason)
	service.idempotency[scope] = idempotentResult{fingerprint: digest(reason), resourceID: conversationID}
	return cloneConversation(*value), false, nil
}

func authorizeConversation(actor Actor, value *Conversation) error {
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return ErrForbidden
	}
	if contains(value.ParticipantIDs, actor.Subject) || hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
		return nil
	}
	return ErrForbidden
}

func redactContact(value string) (string, bool) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}`),
		regexp.MustCompile(`(?:\+?\d[\d -]{7,}\d)`),
		regexp.MustCompile(`(?i)(?:https?://|www\.)\S+`),
	}
	redacted := false
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			value, redacted = pattern.ReplaceAllString(value, "[contact redacted]"), true
		}
	}
	return value, redacted
}

func cloneMessage(value ChatMessage) ChatMessage {
	value.Receipts = append([]MessageReceipt(nil), value.Receipts...)
	return value
}

func cloneConversation(value Conversation) Conversation {
	value.ParticipantIDs = append([]string(nil), value.ParticipantIDs...)
	value.Messages = append([]ChatMessage(nil), value.Messages...)
	for index := range value.Messages {
		value.Messages[index] = cloneMessage(value.Messages[index])
	}
	return value
}
