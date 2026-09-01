package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (service *PostgresService) Conversation(actor Actor, orderID string) (Conversation, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(orderID) {
		return Conversation{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	value, err := loadPostgresConversation(ctx, service.pool, actor.TenantID, actor.Country, orderID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if err := authorizeConversation(actor, &value); err != nil {
		return Conversation{}, err
	}
	return value, nil
}

func (service *PostgresService) SendMessage(actor Actor, key, conversationID, body string) (ChatMessage, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "CUSTOMER", "RIDER", "RESTAURANT_VENDOR") || !validKey(key) || !fulfillmentIsUUID(conversationID) || len(strings.TrimSpace(body)) == 0 || len(body) > 1000 {
		return ChatMessage{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ChatMessage{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "chat:" + conversationID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return ChatMessage{}, false, err
	}
	fingerprint := digest(body)
	var replay ChatMessage
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return ChatMessage{}, false, err
	} else if found {
		return replay, true, nil
	}
	conversation, err := loadPostgresConversationByID(ctx, tx, actor.TenantID, actor.Country, conversationID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatMessage{}, false, ErrNotFound
	}
	if err != nil {
		return ChatMessage{}, false, err
	}
	if err := authorizeConversation(actor, &conversation); err != nil {
		return ChatMessage{}, false, err
	}
	if conversation.Blocked {
		return ChatMessage{}, false, ErrForbidden
	}
	now := service.Now()
	if !conversation.ExpiresAt.After(now) {
		return ChatMessage{}, false, ErrChatExpired
	}
	cleaned, redacted := redactContact(strings.TrimSpace(body))
	value := ChatMessage{ID: uuid.NewString(), SenderID: actor.Subject, Body: cleaned, Redacted: redacted, CreatedAt: now, Receipts: []MessageReceipt{{ActorID: actor.Subject, State: "SENT", At: now}}}
	receipts, _ := json.Marshal(value.Receipts)
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.chat_messages (id,conversation_id,sender_identity_id,body,redacted,receipts,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, value.ID, conversationID, actor.Subject, value.Body, value.Redacted, receipts, now)
	if err != nil {
		return ChatMessage{}, false, mapFulfillmentError(err)
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return ChatMessage{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ChatMessage{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) MessageReceipt(actor Actor, key, conversationID, messageID, state string) (ChatMessage, bool, error) {
	if !postgresFulfillmentActor(actor) || !validKey(key) || !fulfillmentIsUUID(conversationID) || !fulfillmentIsUUID(messageID) || state != "DELIVERED" && state != "READ" {
		return ChatMessage{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ChatMessage{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "receipt:" + messageID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return ChatMessage{}, false, err
	}
	fingerprint := digest(state)
	var replay ChatMessage
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return ChatMessage{}, false, err
	} else if found {
		return replay, true, nil
	}
	conversation, err := loadPostgresConversationByID(ctx, tx, actor.TenantID, actor.Country, conversationID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatMessage{}, false, ErrNotFound
	}
	if err != nil {
		return ChatMessage{}, false, err
	}
	if err := authorizeConversation(actor, &conversation); err != nil {
		return ChatMessage{}, false, err
	}
	now := service.Now()
	if !conversation.ExpiresAt.After(now) {
		return ChatMessage{}, false, ErrChatExpired
	}
	value, err := loadPostgresMessage(ctx, tx, conversationID, messageID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatMessage{}, false, ErrNotFound
	}
	if err != nil {
		return ChatMessage{}, false, err
	}
	updated := false
	for index := range value.Receipts {
		if value.Receipts[index].ActorID == actor.Subject {
			value.Receipts[index].State, value.Receipts[index].At, updated = state, now, true
		}
	}
	if !updated {
		value.Receipts = append(value.Receipts, MessageReceipt{ActorID: actor.Subject, State: state, At: now})
	}
	receipts, _ := json.Marshal(value.Receipts)
	if _, err := tx.Exec(ctx, `UPDATE fulfillment.chat_messages SET receipts=$2 WHERE id=$1`, messageID, receipts); err != nil {
		return ChatMessage{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return ChatMessage{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ChatMessage{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) BlockConversation(actor Actor, key, conversationID, reason string) (Conversation, bool, error) {
	if !postgresFulfillmentActor(actor) || !validKey(key) || !fulfillmentIsUUID(conversationID) || len(strings.TrimSpace(reason)) < 8 {
		return Conversation{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Conversation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "chat-block:" + conversationID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Conversation{}, false, err
	}
	fingerprint := digest(reason)
	var replay Conversation
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Conversation{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadPostgresConversationByID(ctx, tx, actor.TenantID, actor.Country, conversationID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, false, ErrNotFound
	}
	if err != nil {
		return Conversation{}, false, err
	}
	if err := authorizeConversation(actor, &value); err != nil {
		return Conversation{}, false, err
	}
	value.Blocked = true
	now := service.Now()
	if _, err := tx.Exec(ctx, `UPDATE fulfillment.conversations SET blocked=true WHERE id=$1`, conversationID); err != nil {
		return Conversation{}, false, err
	}
	if err := insertFulfillmentAudit(ctx, tx, actor, "CONVERSATION_BLOCKED", "CONVERSATION", conversationID, reason, now); err != nil {
		return Conversation{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Conversation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Conversation{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func loadPostgresConversation(ctx context.Context, query fulfillmentQuerier, tenantID, country, orderID string, lock bool) (Conversation, error) {
	return loadPostgresConversationWhere(ctx, query, tenantID, country, "order_id", orderID, lock)
}

func loadPostgresConversationByID(ctx context.Context, query fulfillmentQuerier, tenantID, country, id string, lock bool) (Conversation, error) {
	return loadPostgresConversationWhere(ctx, query, tenantID, country, "id", id, lock)
}

func loadPostgresConversationWhere(ctx context.Context, query fulfillmentQuerier, tenantID, country, column, id string, lock bool) (Conversation, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var value Conversation
	err := query.QueryRow(ctx, `SELECT id::text,order_id::text,participant_identity_ids::text[],expires_at,blocked,tenant_id::text,country FROM fulfillment.conversations WHERE tenant_id=$1 AND country=$2 AND `+column+`=$3`+suffix, tenantID, country, id).Scan(&value.ID, &value.OrderID, &value.ParticipantIDs, &value.ExpiresAt, &value.Blocked, &value.tenantID, &value.country)
	if err != nil {
		return Conversation{}, err
	}
	rows, err := queryMessages(ctx, query, value.ID)
	if err != nil {
		return Conversation{}, err
	}
	value.Messages = rows
	return value, nil
}

func queryMessages(ctx context.Context, query fulfillmentQuerier, conversationID string) ([]ChatMessage, error) {
	// pgx.Tx and pgxpool.Pool expose Query, but the narrow querier above does not.
	var rows pgx.Rows
	var err error
	switch typed := query.(type) {
	case pgx.Tx:
		rows, err = typed.Query(ctx, `SELECT id::text,sender_identity_id::text,body,redacted,created_at,receipts FROM fulfillment.chat_messages WHERE conversation_id=$1 ORDER BY created_at`, conversationID)
	case *pgxpool.Pool:
		rows, err = typed.Query(ctx, `SELECT id::text,sender_identity_id::text,body,redacted,created_at,receipts FROM fulfillment.chat_messages WHERE conversation_id=$1 ORDER BY created_at`, conversationID)
	default:
		return nil, errors.New("unsupported fulfillment query source")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ChatMessage{}
	for rows.Next() {
		var value ChatMessage
		var receipts []byte
		if err := rows.Scan(&value.ID, &value.SenderID, &value.Body, &value.Redacted, &value.CreatedAt, &receipts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(receipts, &value.Receipts); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadPostgresMessage(ctx context.Context, query fulfillmentQuerier, conversationID, messageID string, lock bool) (ChatMessage, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var value ChatMessage
	var receipts []byte
	err := query.QueryRow(ctx, `SELECT id::text,sender_identity_id::text,body,redacted,created_at,receipts FROM fulfillment.chat_messages WHERE conversation_id=$1 AND id=$2`+suffix, conversationID, messageID).Scan(&value.ID, &value.SenderID, &value.Body, &value.Redacted, &value.CreatedAt, &receipts)
	if err != nil {
		return ChatMessage{}, err
	}
	if err := json.Unmarshal(receipts, &value.Receipts); err != nil {
		return ChatMessage{}, err
	}
	return value, nil
}
