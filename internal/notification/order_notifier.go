package notification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/yazhsab/planext4u-backend/internal/order"
)

type OrderNotifier struct {
	service         *Service
	templateVersion int64
}

func NewOrderNotifier(service *Service, templateVersion int64) (*OrderNotifier, error) {
	if service == nil || templateVersion < 1 {
		return nil, ErrInvalidRequest
	}
	return &OrderNotifier{service: service, templateVersion: templateVersion}, nil
}

func (notifier *OrderNotifier) Send(ctx context.Context, value order.Notification) error {
	endpoints, err := notifier.service.DeviceEndpoints(ctx, value.TenantID, value.Country, value.CustomerID)
	if err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		seed := fmt.Sprintf("%s:%d:%s", value.OrderID, value.Revision, endpoint.ID)
		digest := sha256.Sum256([]byte(seed))
		trace := sha256.Sum256([]byte("trace:" + seed))
		span := sha256.Sum256([]byte("span:" + seed))
		delivery, queueErr := notifier.service.Queue(ctx, QueueCommand{
			TenantID: value.TenantID, Country: value.Country, SubjectID: value.CustomerID,
			RecipientRef: endpoint.ID, Channel: ChannelPush, Purpose: PurposeTransactional,
			TemplateKey: "order-status", TemplateVersion: notifier.templateVersion, Locale: endpoint.Locale,
			Variables:      map[string]string{"order_id": value.OrderID, "status": string(value.Status)},
			Data:           map[string]string{"deep_link": "/app/orders/" + value.OrderID},
			IdempotencyKey: "order-notify-" + hex.EncodeToString(digest[:8]),
			CorrelationID:  "order-" + hex.EncodeToString(digest[8:16]),
			CausationID:    value.ID,
			Traceparent:    "00-" + hex.EncodeToString(trace[:16]) + "-" + hex.EncodeToString(span[:8]) + "-01",
		})
		if queueErr != nil {
			return queueErr
		}
		if processErr := notifier.service.Process(ctx, value.TenantID, delivery.ID); processErr != nil {
			return processErr
		}
	}
	return nil
}
