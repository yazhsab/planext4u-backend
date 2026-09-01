package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type JetStreamPublisher struct {
	connection    *nats.Conn
	jetStream     jetstream.JetStream
	streamName    string
	subjectPrefix string
}

func NewJetStreamPublisher(connection *nats.Conn, streamName, subjectPrefix string) (*JetStreamPublisher, error) {
	if connection == nil || connection.IsClosed() || !safeNATSName(streamName) || !safeSubjectPrefix(subjectPrefix) {
		return nil, ErrInvalidRequest
	}
	js, err := jetstream.New(connection)
	if err != nil {
		return nil, fmt.Errorf("configure JetStream publisher: %w", err)
	}
	return &JetStreamPublisher{connection: connection, jetStream: js, streamName: streamName, subjectPrefix: subjectPrefix}, nil
}

// EnsureJetStream creates or reconciles the stream for explicit local/bootstrap
// workflows. Production environments provision this resource independently.
func EnsureJetStream(ctx context.Context, connection *nats.Conn, streamName, subjectPrefix string) error {
	if connection == nil || connection.IsClosed() || !safeNATSName(streamName) || !safeSubjectPrefix(subjectPrefix) {
		return ErrInvalidRequest
	}
	js, err := jetstream.New(connection)
	if err != nil {
		return fmt.Errorf("configure JetStream: %w", err)
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamName, Subjects: []string{subjectPrefix + ".>"}, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, MaxAge: 7 * 24 * time.Hour,
		Duplicates: 2 * time.Minute, Replicas: 1,
	})
	if err != nil {
		return fmt.Errorf("ensure JetStream stream: %w", err)
	}
	return nil
}

func (publisher *JetStreamPublisher) Ready(ctx context.Context) error {
	if publisher.connection.IsClosed() || !publisher.connection.IsConnected() {
		return errors.New("NATS connection is unavailable")
	}
	if err := publisher.connection.FlushWithContext(ctx); err != nil {
		return fmt.Errorf("flush NATS connection: %w", err)
	}
	if _, err := publisher.jetStream.Stream(ctx, publisher.streamName); err != nil {
		return fmt.Errorf("resolve JetStream stream: %w", err)
	}
	return nil
}

func (publisher *JetStreamPublisher) Publish(ctx context.Context, message Message) error {
	if !validMessage(message) {
		return ErrInvalidRequest
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return ErrInvalidRequest
	}
	request := nats.NewMsg(publisher.subjectPrefix + "." + message.EventType)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Nats-Msg-Id", message.ID)
	request.Header.Set("X-Planext4u-Schema-Version", fmt.Sprintf("%d", message.SchemaVersion))
	request.Data = payload
	ack, err := publisher.jetStream.PublishMsg(ctx, request)
	if err != nil {
		return fmt.Errorf("publish JetStream message: %w", err)
	}
	if ack.Stream != publisher.streamName {
		return errors.New("JetStream acknowledged an unexpected stream")
	}
	return nil
}

func safeNATSName(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(value)
}

func safeSubjectPrefix(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, token := range strings.Split(value, ".") {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(token) {
			return false
		}
	}
	return true
}

var _ Publisher = (*JetStreamPublisher)(nil)
