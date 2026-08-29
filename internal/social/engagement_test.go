package social

import (
	"errors"
	"testing"
	"time"
)

func TestBEP5005MediaStoriesHighlightsCollectionsAndRetention(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := socialServiceAt(t, func() time.Time { return now })
	owner := socialActorFor("customer-synthetic-001", "CUSTOMER", false)
	moderator := socialActorFor("moderator-001", "MODERATOR", true)

	media, replay, err := service.CreateMedia(owner, "media-create-key-001", CreateMediaRequest{AssetID: "asset-story-001", Kind: "VIDEO"})
	if err != nil || replay || media.State != MediaQuarantined {
		t.Fatalf("media=%#v replay=%v err=%v", media, replay, err)
	}
	ready, _, err := service.ProcessMedia(moderator, "media-process-key-001", media.ID, ProcessMediaRequest{Clean: true})
	if err != nil || ready.State != MediaReady || ready.TranscodeStatus != "COMPLETE" {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	story, _, err := service.CreateEphemeral(owner, "story-create-key-001", CreateEphemeralRequest{Kind: "STORY", MediaJobID: media.ID, Caption: "Community update"})
	if err != nil || story.ExpiresAt.Sub(story.CreatedAt) != 24*time.Hour {
		t.Fatalf("story=%#v err=%v", story, err)
	}
	highlighted, _, err := service.SetHighlight(owner, "story-highlight-key-001", story.ID, true)
	if err != nil || !highlighted.Highlighted {
		t.Fatalf("highlighted=%#v err=%v", highlighted, err)
	}
	collection, _, err := service.CreateCollection(owner, "collection-create-key-001", "Local favourites")
	if err != nil {
		t.Fatal(err)
	}
	collection, _, err = service.SetCollectionPost(owner, "collection-post-key-001", collection.ID, "social-post-public-001", true)
	if err != nil || len(collection.PostIDs) != 1 {
		t.Fatalf("collection=%#v err=%v", collection, err)
	}
	now = now.Add(25 * time.Hour)
	items, err := service.Ephemeral(owner)
	if err != nil || len(items) != 1 {
		t.Fatalf("highlight retention=%#v err=%v", items, err)
	}
	if _, _, err = service.SetHighlight(owner, "story-unhighlight-key-001", story.ID, false); err != nil {
		t.Fatal(err)
	}
	count, err := service.PurgeExpired(moderator)
	if err != nil || count != 1 {
		t.Fatalf("purged=%d err=%v", count, err)
	}
}

func TestBEP5006MediaAppealDMVoicePresenceAndCallSignalling(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := socialServiceAt(t, func() time.Time { return now })
	sender := socialActorFor("customer-synthetic-001", "CUSTOMER", false)
	receiver := socialActorFor("customer-public-001", "CUSTOMER", false)
	moderatorOne := socialActorFor("moderator-001", "MODERATOR", true)
	moderatorTwo := socialActorFor("moderator-002", "CONTENT_ADMIN", true)

	rejected, _, err := service.CreateMedia(sender, "media-create-key-002", CreateMediaRequest{AssetID: "asset-appeal-001", Kind: "IMAGE"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, _, err = service.ProcessMedia(moderatorOne, "media-process-key-002", rejected.ID, ProcessMediaRequest{Clean: false})
	if err != nil || rejected.State != MediaRejected {
		t.Fatalf("rejected=%#v err=%v", rejected, err)
	}
	if _, _, err = service.AppealMedia(sender, "media-appeal-key-001", rejected.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.DecideMediaAppeal(moderatorOne, "appeal-decision-key-001", rejected.ID, AppealDecisionRequest{Approve: true, Note: "Second review confirms safe media"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("four-eyes error=%v", err)
	}
	approved, _, err := service.DecideMediaAppeal(moderatorTwo, "appeal-decision-key-002", rejected.ID, AppealDecisionRequest{Approve: true, Note: "Independent review confirms safe media"})
	if err != nil || approved.State != MediaReady || approved.AppealStatus != "APPROVED" {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}

	conversation, _, err := service.OpenConversation(sender, "conversation-open-key-001", receiver.Subject)
	if err != nil || conversation.Status != "REQUESTED" {
		t.Fatalf("conversation=%#v err=%v", conversation, err)
	}
	conversation, _, err = service.AcceptConversation(receiver, "conversation-accept-key-001", conversation.ID)
	if err != nil || conversation.Status != "ACCEPTED" {
		t.Fatalf("accepted=%#v err=%v", conversation, err)
	}
	voice, _, err := service.CreateMedia(sender, "voice-create-key-001", CreateMediaRequest{AssetID: "asset-voice-001", Kind: "VOICE"})
	if err != nil {
		t.Fatal(err)
	}
	voice, _, err = service.ProcessMedia(moderatorOne, "voice-process-key-001", voice.ID, ProcessMediaRequest{Clean: true})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := service.SendMessage(sender, "message-send-key-001", conversation.ID, SendMessageRequest{Body: "Voice update", VoiceMediaID: voice.ID})
	if err != nil || message.VoiceMediaID != voice.ID {
		t.Fatalf("message=%#v err=%v", message, err)
	}
	if _, err = service.SetPresence(receiver, PresenceRequest{State: "ONLINE"}); err != nil {
		t.Fatal(err)
	}
	presence, err := service.Presence(sender, receiver.Subject)
	if err != nil || presence.State != "ONLINE" {
		t.Fatalf("presence=%#v err=%v", presence, err)
	}
	call, _, err := service.CreateCall(sender, "call-create-key-001", conversation.ID, "VIDEO")
	if err != nil {
		t.Fatal(err)
	}
	call, err = service.SignalCall(receiver, call.ID, SignalRequest{Type: "ANSWER", Payload: "opaque-sdp-answer"})
	if err != nil || call.Status != "CONNECTED" || call.SignalCount != 1 {
		t.Fatalf("call=%#v err=%v", call, err)
	}
	if _, _, err = service.SetRelationship(receiver, "message-block-key-001", sender.Subject, "BLOCK"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.SendMessage(sender, "message-send-key-002", conversation.ID, SendMessageRequest{Body: "must not deliver"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("blocked message error=%v", err)
	}
	now = now.Add(3 * time.Minute)
	presence, err = service.Presence(sender, receiver.Subject)
	if !errors.Is(err, ErrForbidden) || presence.State != "" {
		t.Fatalf("blocked presence=%#v err=%v", presence, err)
	}
}

func socialServiceAt(t *testing.T, clock func() time.Time) *Service {
	t.Helper()
	now := clock()
	profiles := []Profile{
		{ID: "customer-synthetic-001", Handle: "synthetic_user", DisplayName: "Synthetic Customer"},
		{ID: "customer-public-001", Handle: "public_user", DisplayName: "Public Neighbour", Verified: true},
		{ID: "customer-private-001", Handle: "private_user", DisplayName: "Private Neighbour", Private: true},
	}
	service, err := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN", Profiles: profiles, Posts: []Post{{ID: "social-post-public-001", Revision: 1, Author: profiles[1], Body: "Community market", Status: PostPublished, CreatedAt: now, UpdatedAt: now}}, RankingModel: "socio-feed-v1"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
