//go:build integration

package messaging

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestJetStreamPublisherUsesBrokerDeduplicationAndDurableAcknowledgement(t *testing.T) {
	natsURL := os.Getenv("NATS_TEST_URL")
	if natsURL == "" {
		t.Skip("NATS_TEST_URL is not set")
	}
	connection, err := nats.Connect(natsURL, nats.Timeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	js, err := jetstream.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	streamName := "P4U_TEST_" + strings.ToUpper(suffix)
	subjectPrefix := "planext4u.test." + suffix
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamName, Subjects: []string{subjectPrefix + ".>"}, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld,
		MaxAge: maximumJetStreamMessageAge, Duplicates: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })
	publisher, err := NewJetStreamPublisher(connection, streamName, subjectPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	message := syntheticMessage("00000000-0000-4000-8000-000000000001", 1)
	if err := publisher.Publish(ctx, message); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(ctx, message); err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("deduplicated stream messages=%d", info.State.Msgs)
	}
	// An operator can change broker retention independently of the app. Neither
	// health nor a later publish may accept an unbounded durable copy.
	if _, err := js.UpdateStream(ctx, jetstream.StreamConfig{
		Name: streamName, Subjects: []string{subjectPrefix + ".>"}, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld,
		MaxAge: 0, Duplicates: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Ready(ctx); err == nil {
		t.Fatal("unbounded broker retention passed readiness")
	}
	other := syntheticMessage("00000000-0000-4000-8000-000000000002", 2)
	if err := publisher.Publish(ctx, other); err == nil {
		t.Fatal("outbox publisher sent an event after broker retention widened")
	}
	info, err = stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("unsafe broker mutation retained %d messages", info.State.Msgs)
	}
	if _, err := js.UpdateStream(ctx, jetstream.StreamConfig{
		Name: streamName, Subjects: []string{subjectPrefix + ".>"}, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld,
		MaxAge: maximumJetStreamMessageAge, Duplicates: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Ready(ctx); err != nil {
		t.Fatalf("bounded retention did not restore readiness: %v", err)
	}
	if err := publisher.Publish(ctx, other); err != nil {
		t.Fatalf("bounded retention did not restore publishing: %v", err)
	}
}
