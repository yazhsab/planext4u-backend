package social

import (
	"sort"
	"strings"
	"time"
)

func (service *Service) CreateMedia(actor Actor, key string, request CreateMediaRequest) (MediaJob, bool, error) {
	kind := strings.ToUpper(strings.TrimSpace(request.Kind))
	if !validCustomer(actor) || !validKey(key) || !safeID(request.AssetID) || !map[string]bool{"IMAGE": true, "VIDEO": true, "VOICE": true}[kind] {
		return MediaJob{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	scope := idempotencyScope(actor, "social-media", key)
	fingerprint := digest(request)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return MediaJob{}, false, ErrIdempotencyConflict
		}
		return cloneMedia(*service.mediaJobs[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	value := &MediaJob{
		ID: "social-media-" + sequenceID(service.next()), OwnerID: actor.Subject, AssetID: request.AssetID, Kind: kind,
		State: MediaQuarantined, ScanStatus: "PENDING", BlurStatus: "PENDING", TranscodeStatus: "PENDING",
		RetentionUntil: now.Add(30 * 24 * time.Hour), CreatedAt: now, tenantID: actor.TenantID, country: actor.Country,
	}
	if kind == "VOICE" {
		value.BlurStatus = "NOT_APPLICABLE"
	}
	service.mediaJobs[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneMedia(*value), false, nil
}

func (service *Service) ProcessMedia(actor Actor, key, mediaID string, request ProcessMediaRequest) (MediaJob, bool, error) {
	if !validModerator(actor) || !validKey(key) || !safeID(mediaID) {
		if !actor.MFAVerified {
			return MediaJob{}, false, ErrMFARequired
		}
		return MediaJob{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.mediaJobs[mediaID]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return MediaJob{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "process-media:"+mediaID, key)
	fingerprint := digest(request)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return MediaJob{}, false, ErrIdempotencyConflict
		}
		return cloneMedia(*value), true, nil
	}
	if value.State != MediaQuarantined {
		return MediaJob{}, false, ErrConflict
	}
	value.ModeratedBy = actor.Subject
	if request.Clean {
		value.State, value.ScanStatus = MediaReady, "CLEAN"
		value.BlurStatus = "COMPLETE"
		value.TranscodeStatus = "COMPLETE"
	} else {
		value.State, value.ScanStatus = MediaRejected, "REJECTED"
		value.BlurStatus, value.TranscodeStatus = "NOT_RUN", "NOT_RUN"
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneMedia(*value), false, nil
}

func (service *Service) AppealMedia(actor Actor, key, mediaID string) (MediaJob, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(mediaID) {
		return MediaJob{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.mediaJobs[mediaID]
	if value == nil || value.OwnerID != actor.Subject || value.tenantID != actor.TenantID || value.country != actor.Country {
		return MediaJob{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "appeal-media:"+mediaID, key)
	if previous, exists := service.idempotency[scope]; exists {
		return cloneMedia(*service.mediaJobs[previous.resourceID]), true, nil
	}
	if value.State != MediaRejected || value.AppealStatus != "" {
		return MediaJob{}, false, ErrConflict
	}
	value.AppealStatus = "PENDING"
	service.idempotency[scope] = idempotentResult{fingerprint: digest(mediaID), resourceID: value.ID}
	return cloneMedia(*value), false, nil
}

func (service *Service) DecideMediaAppeal(actor Actor, key, mediaID string, request AppealDecisionRequest) (MediaJob, bool, error) {
	note := strings.TrimSpace(request.Note)
	if !validModerator(actor) || !validKey(key) || !safeID(mediaID) || len(note) < 8 || len(note) > 500 {
		if !actor.MFAVerified {
			return MediaJob{}, false, ErrMFARequired
		}
		return MediaJob{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.mediaJobs[mediaID]
	if value == nil || value.tenantID != actor.TenantID || value.country != actor.Country {
		return MediaJob{}, false, ErrNotFound
	}
	if value.AppealStatus != "PENDING" || value.ModeratedBy == actor.Subject {
		return MediaJob{}, false, ErrForbidden
	}
	scope := idempotencyScope(actor, "decide-media-appeal:"+mediaID, key)
	fingerprint := digest(request)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return MediaJob{}, false, ErrIdempotencyConflict
		}
		return cloneMedia(*value), true, nil
	}
	value.AppealStatus = "REJECTED"
	if request.Approve {
		value.AppealStatus, value.State = "APPROVED", MediaReady
		value.ScanStatus, value.BlurStatus, value.TranscodeStatus = "CLEAN", "COMPLETE", "COMPLETE"
	}
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneMedia(*value), false, nil
}

func (service *Service) CreateEphemeral(actor Actor, key string, request CreateEphemeralRequest) (EphemeralContent, bool, error) {
	kind, caption := strings.ToUpper(strings.TrimSpace(request.Kind)), strings.TrimSpace(request.Caption)
	if !validCustomer(actor) || !validKey(key) || !safeID(request.MediaJobID) || !map[string]bool{"STORY": true, "REEL": true}[kind] || len([]rune(caption)) > 1000 {
		return EphemeralContent{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	media := service.mediaJobs[request.MediaJobID]
	profile := service.profiles[actor.Subject]
	if profile == nil || media == nil || media.OwnerID != actor.Subject || media.State != MediaReady || media.tenantID != actor.TenantID || media.country != actor.Country {
		return EphemeralContent{}, false, ErrForbidden
	}
	scope := idempotencyScope(actor, "ephemeral", key)
	fingerprint := digest(request)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return EphemeralContent{}, false, ErrIdempotencyConflict
		}
		return cloneEphemeral(*service.ephemeral[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	ttl := 24 * time.Hour
	if kind == "REEL" {
		ttl = 30 * 24 * time.Hour
	}
	value := &EphemeralContent{
		ID: "social-content-" + sequenceID(service.next()), Author: cloneProfile(*profile), Kind: kind,
		MediaJobID: request.MediaJobID, MediaAssetID: media.AssetID, Caption: caption, Status: "PUBLISHED", ExpiresAt: now.Add(ttl),
		AllowedActions: []string{"HIGHLIGHT", "DELETE"}, CreatedAt: now, tenantID: actor.TenantID, country: actor.Country,
	}
	service.ephemeral[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneEphemeral(*value), false, nil
}

func (service *Service) Ephemeral(actor Actor) ([]EphemeralContent, error) {
	if !validCustomer(actor) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.clock().UTC()
	values := []EphemeralContent{}
	for id, value := range service.ephemeral {
		if value.Status != "PUBLISHED" || (!value.Highlighted && !now.Before(value.ExpiresAt)) {
			if value.Status == "PUBLISHED" && !value.Highlighted {
				value.Status = "EXPIRED"
				service.tombstones[id] = now
			}
			continue
		}
		if service.isBlockedLocked(actor.Subject, value.Author.ID) || (value.Author.Private && value.Author.ID != actor.Subject && !service.followAcceptedLocked(actor.Subject, value.Author.ID)) {
			continue
		}
		clone := cloneEphemeral(*value)
		if value.Author.ID != actor.Subject {
			clone.AllowedActions = []string{"REPORT"}
		}
		values = append(values, clone)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) SetHighlight(actor Actor, key, contentID string, active bool) (EphemeralContent, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(contentID) {
		return EphemeralContent{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.ephemeral[contentID]
	if value == nil || value.Author.ID != actor.Subject || value.Status != "PUBLISHED" {
		return EphemeralContent{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "highlight:"+contentID, key)
	fingerprint := digest(active)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return EphemeralContent{}, false, ErrIdempotencyConflict
		}
		return cloneEphemeral(*value), true, nil
	}
	value.Highlighted = active
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneEphemeral(*value), false, nil
}

func (service *Service) PurgeExpired(actor Actor) (int, error) {
	if !validModerator(actor) {
		return 0, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now, count := service.clock().UTC(), 0
	for id, content := range service.ephemeral {
		if !content.Highlighted && !now.Before(content.ExpiresAt) && content.Status != "TOMBSTONED" {
			content.Status = "TOMBSTONED"
			service.tombstones[id], count = now, count+1
			if media := service.mediaJobs[content.MediaJobID]; media != nil {
				media.State = MediaTombstoned
			}
		}
	}
	return count, nil
}

func (service *Service) CreateCollection(actor Actor, key, name string) (Collection, bool, error) {
	name = strings.TrimSpace(name)
	if !validCustomer(actor) || !validKey(key) || len([]rune(name)) < 2 || len([]rune(name)) > 80 {
		return Collection{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	scope := idempotencyScope(actor, "collection", key)
	fingerprint := digest(name)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Collection{}, false, ErrIdempotencyConflict
		}
		return cloneCollection(*service.collections[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	value := &Collection{ID: "social-collection-" + sequenceID(service.next()), Name: name, PostIDs: []string{}, CreatedAt: now, UpdatedAt: now, ownerID: actor.Subject, tenantID: actor.TenantID, country: actor.Country}
	service.collections[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneCollection(*value), false, nil
}

func (service *Service) SetCollectionPost(actor Actor, key, collectionID, postID string, active bool) (Collection, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(collectionID) || !safeID(postID) {
		return Collection{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	collection, post := service.collections[collectionID], service.posts[postID]
	if collection == nil || collection.ownerID != actor.Subject || post == nil || !service.canViewPostLocked(actor, post) {
		return Collection{}, false, ErrNotFound
	}
	scope := idempotencyScope(actor, "collection-post:"+collectionID, key)
	fingerprint := digest(CollectionPostRequest{PostID: postID, Active: active})
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Collection{}, false, ErrIdempotencyConflict
		}
		return cloneCollection(*collection), true, nil
	}
	found := false
	for _, id := range collection.PostIDs {
		found = found || id == postID
	}
	if active && !found {
		collection.PostIDs = append(collection.PostIDs, postID)
	}
	if !active && found {
		filtered := []string{}
		for _, id := range collection.PostIDs {
			if id != postID {
				filtered = append(filtered, id)
			}
		}
		collection.PostIDs = filtered
	}
	collection.UpdatedAt = service.clock().UTC()
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: collection.ID}
	return cloneCollection(*collection), false, nil
}

func (service *Service) OpenConversation(actor Actor, key, profileID string) (Conversation, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(profileID) || profileID == actor.Subject {
		return Conversation{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.profiles[profileID] == nil || service.isBlockedLocked(actor.Subject, profileID) {
		return Conversation{}, false, ErrNotFound
	}
	pair := participantKey(actor.Subject, profileID)
	for _, value := range service.conversations {
		if participantKey(value.ParticipantIDs[0], value.ParticipantIDs[1]) == pair {
			return presentConversation(actor.Subject, *value), true, nil
		}
	}
	scope := idempotencyScope(actor, "conversation:"+profileID, key)
	fingerprint := digest(profileID)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return Conversation{}, false, ErrIdempotencyConflict
		}
		return presentConversation(actor.Subject, *service.conversations[previous.resourceID]), true, nil
	}
	now := service.clock().UTC()
	status := "REQUESTED"
	if service.followAcceptedLocked(actor.Subject, profileID) && service.followAcceptedLocked(profileID, actor.Subject) {
		status = "ACCEPTED"
	}
	value := &Conversation{ID: "social-conversation-" + sequenceID(service.next()), ParticipantIDs: []string{actor.Subject, profileID}, Status: status, RequestedBy: actor.Subject, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.conversations[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return presentConversation(actor.Subject, *value), false, nil
}

func (service *Service) Conversations(actor Actor) ([]Conversation, error) {
	if !validCustomer(actor) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []Conversation{}
	for _, value := range service.conversations {
		if member(actor.Subject, value.ParticipantIDs) && value.tenantID == actor.TenantID && value.country == actor.Country {
			values = append(values, presentConversation(actor.Subject, *value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].UpdatedAt.After(values[j].UpdatedAt) })
	return values, nil
}

func (service *Service) AcceptConversation(actor Actor, key, conversationID string) (Conversation, bool, error) {
	if !validCustomer(actor) || !validKey(key) || !safeID(conversationID) {
		return Conversation{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.conversations[conversationID]
	if value == nil || value.Status != "REQUESTED" || value.RequestedBy == actor.Subject || !member(actor.Subject, value.ParticipantIDs) {
		return Conversation{}, false, ErrNotFound
	}
	other := otherParticipant(actor.Subject, value.ParticipantIDs)
	if service.isBlockedLocked(actor.Subject, other) {
		return Conversation{}, false, ErrForbidden
	}
	scope := idempotencyScope(actor, "accept-conversation:"+conversationID, key)
	if _, exists := service.idempotency[scope]; exists {
		return presentConversation(actor.Subject, *value), true, nil
	}
	value.Status, value.UpdatedAt = "ACCEPTED", service.clock().UTC()
	service.idempotency[scope] = idempotentResult{fingerprint: digest(conversationID), resourceID: value.ID}
	return presentConversation(actor.Subject, *value), false, nil
}

func (service *Service) SendMessage(actor Actor, key, conversationID string, request SendMessageRequest) (DirectMessage, bool, error) {
	body := strings.TrimSpace(request.Body)
	if !validCustomer(actor) || !validKey(key) || !safeID(conversationID) || len([]rune(body)) > 4000 || (body == "" && request.VoiceMediaID == "") {
		return DirectMessage{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	conversation := service.conversations[conversationID]
	if conversation == nil || conversation.Status != "ACCEPTED" || !member(actor.Subject, conversation.ParticipantIDs) || service.isBlockedLocked(conversation.ParticipantIDs[0], conversation.ParticipantIDs[1]) {
		return DirectMessage{}, false, ErrForbidden
	}
	if request.VoiceMediaID != "" {
		media := service.mediaJobs[request.VoiceMediaID]
		if media == nil || media.OwnerID != actor.Subject || media.Kind != "VOICE" || media.State != MediaReady {
			return DirectMessage{}, false, ErrForbidden
		}
	}
	scope := idempotencyScope(actor, "message:"+conversationID, key)
	fingerprint := digest(request)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return DirectMessage{}, false, ErrIdempotencyConflict
		}
		for _, value := range service.messages[conversationID] {
			if value.ID == previous.resourceID {
				return value, true, nil
			}
		}
	}
	value := DirectMessage{ID: "social-message-" + sequenceID(service.next()), ConversationID: conversationID, SenderID: actor.Subject, Body: body, VoiceMediaID: request.VoiceMediaID, Status: "DELIVERED", CreatedAt: service.clock().UTC(), tenantID: actor.TenantID, country: actor.Country}
	service.messages[conversationID] = append(service.messages[conversationID], value)
	conversation.UpdatedAt = value.CreatedAt
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return value, false, nil
}

func (service *Service) Messages(actor Actor, conversationID string) ([]DirectMessage, error) {
	if !validCustomer(actor) || !safeID(conversationID) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	conversation := service.conversations[conversationID]
	if conversation == nil || conversation.Status != "ACCEPTED" || !member(actor.Subject, conversation.ParticipantIDs) {
		return nil, ErrNotFound
	}
	return append([]DirectMessage(nil), service.messages[conversationID]...), nil
}

func (service *Service) SetPresence(actor Actor, request PresenceRequest) (Presence, error) {
	state := strings.ToUpper(strings.TrimSpace(request.State))
	if !validCustomer(actor) || !map[string]bool{"ONLINE": true, "AWAY": true, "OFFLINE": true}[state] {
		return Presence{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	ttl := 2 * time.Minute
	if state == "OFFLINE" {
		ttl = time.Second
	}
	value := Presence{ProfileID: actor.Subject, State: state, ExpiresAt: service.clock().UTC().Add(ttl)}
	service.presence[actor.Subject] = value
	return value, nil
}

func (service *Service) Presence(actor Actor, profileID string) (Presence, error) {
	if !validCustomer(actor) || !safeID(profileID) {
		return Presence{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.isBlockedLocked(actor.Subject, profileID) {
		return Presence{}, ErrForbidden
	}
	value, exists := service.presence[profileID]
	if !exists || !service.clock().UTC().Before(value.ExpiresAt) {
		return Presence{ProfileID: profileID, State: "OFFLINE", ExpiresAt: service.clock().UTC()}, nil
	}
	return value, nil
}

func (service *Service) CreateCall(actor Actor, key, conversationID, kind string) (CallSession, bool, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if !validCustomer(actor) || !validKey(key) || !safeID(conversationID) || !map[string]bool{"AUDIO": true, "VIDEO": true}[kind] {
		return CallSession{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	conversation := service.conversations[conversationID]
	if conversation == nil || conversation.Status != "ACCEPTED" || !member(actor.Subject, conversation.ParticipantIDs) || service.isBlockedLocked(conversation.ParticipantIDs[0], conversation.ParticipantIDs[1]) {
		return CallSession{}, false, ErrForbidden
	}
	scope := idempotencyScope(actor, "call:"+conversationID, key)
	fingerprint := digest(kind)
	if previous, exists := service.idempotency[scope]; exists {
		if previous.fingerprint != fingerprint {
			return CallSession{}, false, ErrIdempotencyConflict
		}
		return *service.calls[previous.resourceID], true, nil
	}
	now := service.clock().UTC()
	value := &CallSession{ID: "social-call-" + sequenceID(service.next()), ConversationID: conversationID, InitiatorID: actor.Subject, Kind: kind, Status: "RINGING", ExpiresAt: now.Add(2 * time.Minute), CreatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.calls[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return *value, false, nil
}

func (service *Service) SignalCall(actor Actor, callID string, request SignalRequest) (CallSession, error) {
	typeName, payload := strings.ToUpper(strings.TrimSpace(request.Type)), strings.TrimSpace(request.Payload)
	if !validCustomer(actor) || !safeID(callID) || !map[string]bool{"OFFER": true, "ANSWER": true, "ICE": true, "END": true}[typeName] || len(payload) > 16*1024 || strings.ContainsAny(payload, "\r\n") {
		return CallSession{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.calls[callID]
	conversation := (*Conversation)(nil)
	if value != nil {
		conversation = service.conversations[value.ConversationID]
	}
	if value == nil || conversation == nil || !member(actor.Subject, conversation.ParticipantIDs) || !service.clock().UTC().Before(value.ExpiresAt) {
		return CallSession{}, ErrForbidden
	}
	value.SignalCount++
	if typeName == "ANSWER" {
		value.Status = "CONNECTED"
	}
	if typeName == "END" {
		value.Status = "ENDED"
	}
	return *value, nil
}

func cloneMedia(value MediaJob) MediaJob { return value }

func cloneEphemeral(value EphemeralContent) EphemeralContent {
	value.Author = cloneProfile(value.Author)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}

func cloneCollection(value Collection) Collection {
	value.PostIDs = append([]string(nil), value.PostIDs...)
	return value
}

func presentConversation(actor string, value Conversation) Conversation {
	value.ParticipantIDs = append([]string(nil), value.ParticipantIDs...)
	value.AllowedActions = []string{"VIEW"}
	if value.Status == "REQUESTED" && value.RequestedBy != actor {
		value.AllowedActions = []string{"ACCEPT", "DECLINE", "BLOCK"}
	} else if value.Status == "ACCEPTED" {
		value.AllowedActions = []string{"MESSAGE", "VOICE_NOTE", "AUDIO_CALL", "VIDEO_CALL", "BLOCK"}
	}
	return value
}

func participantKey(left, right string) string {
	if left < right {
		return relationKey(left, right)
	}
	return relationKey(right, left)
}

func member(id string, values []string) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}

func otherParticipant(id string, values []string) string {
	for _, value := range values {
		if value != id {
			return value
		}
	}
	return ""
}
