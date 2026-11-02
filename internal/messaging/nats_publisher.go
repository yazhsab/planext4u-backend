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

// The broker has its own durable copy of every published envelope. A stream
// without a bounded age, or one that republishes the envelope elsewhere, is
// outside the local privacy retention boundary.
const maximumJetStreamMessageAge = 7 * 24 * time.Hour

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
		Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, MaxAge: maximumJetStreamMessageAge,
		Duplicates: 2 * time.Minute, Replicas: 1,
	})
	if err != nil {
		return fmt.Errorf("ensure JetStream stream: %w", err)
	}
	return nil
}

func (publisher *JetStreamPublisher) Ready(ctx context.Context) error {
	return publisher.checkStreamPolicy(ctx)
}

func (publisher *JetStreamPublisher) checkStreamPolicy(ctx context.Context) error {
	if publisher.connection.IsClosed() || !publisher.connection.IsConnected() {
		return errors.New("NATS connection is unavailable")
	}
	// Stream fetches current server info. A previously cached handle would miss
	// an operator changing retention after the last health probe.
	stream, err := publisher.jetStream.Stream(ctx, publisher.streamName)
	if err != nil {
		return fmt.Errorf("resolve JetStream stream: %w", err)
	}
	info := stream.CachedInfo()
	if info == nil {
		return errors.New("JetStream stream policy is unavailable")
	}
	if err := validJetStreamStreamPolicy(info.Config, publisher.streamName, publisher.subjectPrefix); err != nil {
		return err
	}
	return nil
}

func (publisher *JetStreamPublisher) Publish(ctx context.Context, message Message) error {
	if !validMessage(message) {
		return ErrInvalidRequest
	}
	// A readiness check alone does not stop publishing after a broker operator
	// changes a stream's retention or republish policy at runtime.
	if err := publisher.checkStreamPolicy(ctx); err != nil {
		return err
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

func validJetStreamStreamPolicy(config jetstream.StreamConfig, name, subjectPrefix string) error {
	if config.Name != name || len(config.Subjects) != 1 || config.Subjects[0] != subjectPrefix+".>" ||
		config.Retention != jetstream.LimitsPolicy || config.Discard != jetstream.DiscardOld ||
		config.Storage != jetstream.FileStorage || config.MaxAge <= 0 || config.MaxAge > maximumJetStreamMessageAge ||
		config.NoAck || config.Sealed || config.Mirror != nil || len(config.Sources) != 0 ||
		config.SubjectTransform != nil || config.RePublish != nil || config.AllowDirect || config.MirrorDirect {
		return errors.New("JetStream stream privacy retention policy is unavailable")
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
