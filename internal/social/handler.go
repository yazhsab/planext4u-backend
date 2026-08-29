package social

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/social/feed", handler.feed)
	mux.HandleFunc("POST /v1/social/posts", handler.createPost)
	mux.HandleFunc("GET /v1/social/posts/{post_id}", handler.post)
	mux.HandleFunc("PUT /v1/social/posts/{post_id}/like", handler.like)
	mux.HandleFunc("PUT /v1/social/posts/{post_id}/save", handler.save)
	mux.HandleFunc("GET /v1/social/posts/{post_id}/comments", handler.comments)
	mux.HandleFunc("POST /v1/social/posts/{post_id}/comments", handler.createComment)
	mux.HandleFunc("POST /v1/social/posts/{post_id}/reports", handler.report)
	mux.HandleFunc("GET /v1/social/profiles/{profile_id}", handler.profile)
	mux.HandleFunc("POST /v1/social/profiles/{profile_id}/follow", handler.follow)
	mux.HandleFunc("PUT /v1/social/profiles/{profile_id}/relationship", handler.relationship)
	mux.HandleFunc("POST /v1/social/follow-requests/{follower_id}/accept", handler.acceptFollow)
	mux.HandleFunc("POST /v1/social/media", handler.createMedia)
	mux.HandleFunc("POST /v1/social/media/{media_id}/appeals", handler.appealMedia)
	mux.HandleFunc("GET /v1/social/ephemeral", handler.ephemeral)
	mux.HandleFunc("POST /v1/social/ephemeral", handler.createEphemeral)
	mux.HandleFunc("PUT /v1/social/ephemeral/{content_id}/highlight", handler.highlight)
	mux.HandleFunc("POST /v1/social/collections", handler.createCollection)
	mux.HandleFunc("PUT /v1/social/collections/{collection_id}/posts", handler.collectionPost)
	mux.HandleFunc("GET /v1/social/conversations", handler.conversations)
	mux.HandleFunc("POST /v1/social/conversations", handler.openConversation)
	mux.HandleFunc("POST /v1/social/conversations/{conversation_id}/accept", handler.acceptConversation)
	mux.HandleFunc("GET /v1/social/conversations/{conversation_id}/messages", handler.messages)
	mux.HandleFunc("POST /v1/social/conversations/{conversation_id}/messages", handler.sendMessage)
	mux.HandleFunc("PUT /v1/social/presence", handler.setPresence)
	mux.HandleFunc("GET /v1/social/presence/{profile_id}", handler.presence)
	mux.HandleFunc("POST /v1/social/conversations/{conversation_id}/calls", handler.createCall)
	mux.HandleFunc("POST /v1/social/calls/{call_id}/signals", handler.signalCall)
	mux.HandleFunc("GET /v1/moderation/reports", handler.moderationQueue)
	mux.HandleFunc("POST /v1/moderation/reports/{report_id}/decision", handler.moderate)
	mux.HandleFunc("POST /v1/moderation/media/{media_id}/process", handler.processMedia)
	mux.HandleFunc("POST /v1/moderation/media/{media_id}/appeal-decision", handler.decideMediaAppeal)
	mux.HandleFunc("POST /v1/moderation/retention/purge", handler.purgeExpired)
	return mux, nil
}

func (handler *Handler) feed(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	limit := 0
	if raw := request.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			handler.problem(writer, request, ErrInvalidRequest)
			return
		}
	}
	value, err := handler.service.Feed(actor, request.URL.Query().Get("cursor"), limit)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, value)
}

func (handler *Handler) createPost(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreatePostRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreatePost(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialPost(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) post(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Post(actor, request.PathValue("post_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialPost(writer, http.StatusOK, value, false)
}

func (handler *Handler) like(writer http.ResponseWriter, request *http.Request) {
	handler.engagement(writer, request, handler.service.SetLike)
}

func (handler *Handler) save(writer http.ResponseWriter, request *http.Request) {
	handler.engagement(writer, request, handler.service.SetSave)
}

func (handler *Handler) engagement(writer http.ResponseWriter, request *http.Request, operation func(Actor, string, string, int64, bool) (Post, bool, error)) {
	actor, revision, ok := socialMutation(writer, request)
	if !ok {
		return
	}
	var input EngagementRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := operation(actor, request.Header.Get("Idempotency-Key"), request.PathValue("post_id"), revision, input.Active)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialPost(writer, http.StatusOK, value, replay)
}

func (handler *Handler) comments(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Comments(actor, request.PathValue("post_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createComment(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreateCommentRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateComment(actor, request.Header.Get("Idempotency-Key"), request.PathValue("post_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) profile(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Profile(actor, request.PathValue("profile_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, value)
}

func (handler *Handler) follow(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.Follow(actor, request.Header.Get("Idempotency-Key"), request.PathValue("profile_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) acceptFollow(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.AcceptFollow(actor, request.Header.Get("Idempotency-Key"), request.PathValue("follower_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) relationship(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input RelationshipRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetRelationship(actor, request.Header.Get("Idempotency-Key"), request.PathValue("profile_id"), input.Action)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) report(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input ReportRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ReportPost(actor, request.Header.Get("Idempotency-Key"), request.PathValue("post_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) moderationQueue(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.ModerationQueue(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) moderate(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input ModerationDecisionRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.Moderate(actor, request.Header.Get("Idempotency-Key"), request.PathValue("report_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "SOCIAL_INTERNAL", "The social request could not be completed."
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, detail = http.StatusUnprocessableEntity, "SOCIAL_REQUEST_INVALID", "The social request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, detail = http.StatusNotFound, "SOCIAL_NOT_FOUND", "The social resource was not found."
	case errors.Is(err, ErrForbidden):
		status, code, detail = http.StatusForbidden, "SOCIAL_FORBIDDEN", "The actor cannot access this social resource."
	case errors.Is(err, ErrMFARequired):
		status, code, detail = http.StatusForbidden, "SOCIAL_MFA_REQUIRED", "A verified MFA session is required for moderation."
	case errors.Is(err, ErrConflict):
		status, code, detail = http.StatusConflict, "SOCIAL_REVISION_CONFLICT", "The post changed; refresh before retrying."
	case errors.Is(err, ErrIdempotencyConflict):
		status, code, detail = http.StatusConflict, "SOCIAL_IDEMPOTENCY_CONFLICT", "The idempotency key was already used for another command."
	}
	writeSocialProblem(writer, request, status, code, detail)
}

func socialMutation(writer http.ResponseWriter, request *http.Request) (Actor, int64, bool) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return Actor{}, 0, false
	}
	revision, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(request.Header.Get("If-Match")), `"`), 10, 64)
	if err != nil || revision < 1 || !validKey(request.Header.Get("Idempotency-Key")) {
		writeSocialProblem(writer, request, http.StatusUnprocessableEntity, "SOCIAL_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return Actor{}, 0, false
	}
	return actor, revision, true
}

func socialActor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{
		TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")),
		Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Roles: strings.Split(request.Header.Get("X-Planext4u-Roles"), ","),
		MFAVerified: strings.EqualFold(request.Header.Get("X-Planext4u-MFA"), "verified"),
	}
	if !validActor(actor) {
		writeSocialProblem(writer, request, http.StatusForbidden, "SOCIAL_SCOPE_REQUIRED", "The authenticated social scope is incomplete.")
		return Actor{}, false
	}
	return actor, true
}

func decodeSocialJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeSocialProblem(writer, request, http.StatusUnprocessableEntity, "SOCIAL_REQUEST_INVALID", "The social request is invalid.")
		return false
	}
	return true
}

func writeSocialPost(writer http.ResponseWriter, status int, value Post, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeSocialReplay(writer, status, value, replay)
}

func writeSocialReplay(writer http.ResponseWriter, status int, value any, replay bool) {
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeSocialJSON(writer, status, value)
}

func writeSocialJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeSocialProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	writeSocialJSON(writer, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "correlation_id": request.Header.Get("X-Correlation-ID"), "retryable": false, "field_errors": []any{}, "details": map[string]any{},
	}})
}
