package social

import "net/http"

func (handler *Handler) createMedia(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreateMediaRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateMedia(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusAccepted, value, replay)
}

func (handler *Handler) processMedia(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input ProcessMediaRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.ProcessMedia(actor, request.Header.Get("Idempotency-Key"), request.PathValue("media_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) appealMedia(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.AppealMedia(actor, request.Header.Get("Idempotency-Key"), request.PathValue("media_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusAccepted, value, replay)
}

func (handler *Handler) decideMediaAppeal(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input AppealDecisionRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.DecideMediaAppeal(actor, request.Header.Get("Idempotency-Key"), request.PathValue("media_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) ephemeral(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Ephemeral(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createEphemeral(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreateEphemeralRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateEphemeral(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) highlight(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input HighlightRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetHighlight(actor, request.Header.Get("Idempotency-Key"), request.PathValue("content_id"), input.Active)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) purgeExpired(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	count, err := handler.service.PurgeExpired(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, map[string]any{"purged": count})
}

func (handler *Handler) createCollection(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreateCollectionRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateCollection(actor, request.Header.Get("Idempotency-Key"), input.Name)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) collectionPost(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CollectionPostRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetCollectionPost(actor, request.Header.Get("Idempotency-Key"), request.PathValue("collection_id"), input.PostID, input.Active)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) conversations(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Conversations(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) openConversation(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreateConversationRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.OpenConversation(actor, request.Header.Get("Idempotency-Key"), input.ProfileID)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) acceptConversation(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, replay, err := handler.service.AcceptConversation(actor, request.Header.Get("Idempotency-Key"), request.PathValue("conversation_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusOK, value, replay)
}

func (handler *Handler) messages(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Messages(actor, request.PathValue("conversation_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) sendMessage(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input SendMessageRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SendMessage(actor, request.Header.Get("Idempotency-Key"), request.PathValue("conversation_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) setPresence(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input PresenceRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, err := handler.service.SetPresence(actor, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, value)
}

func (handler *Handler) presence(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Presence(actor, request.PathValue("profile_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, value)
}

func (handler *Handler) createCall(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input CreateCallRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateCall(actor, request.Header.Get("Idempotency-Key"), request.PathValue("conversation_id"), input.Kind)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) signalCall(writer http.ResponseWriter, request *http.Request) {
	actor, ok := socialActor(writer, request)
	if !ok {
		return
	}
	var input SignalRequest
	if !decodeSocialJSON(writer, request, &input) {
		return
	}
	value, err := handler.service.SignalCall(actor, request.PathValue("call_id"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeSocialJSON(writer, http.StatusOK, value)
}
