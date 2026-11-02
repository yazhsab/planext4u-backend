package messaging

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

func TestJetStreamPrivacyRetentionRejectsUnboundedAndCopiedEnvelopes(t *testing.T) {
	const name = "PLANEXT4U_EVENTS"
	const prefix = "planext4u.events"
	base := jetstream.StreamConfig{
		Name: name, Subjects: []string{prefix + ".>"},
		Storage: jetstream.FileStorage, Retention: jetstream.LimitsPolicy,
		Discard: jetstream.DiscardOld, MaxAge: maximumJetStreamMessageAge,
	}
	if err := validJetStreamStreamPolicy(base, name, prefix); err != nil {
		t.Fatalf("bounded owner stream rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*jetstream.StreamConfig)
	}{
		{"unbounded age", func(config *jetstream.StreamConfig) { config.MaxAge = 0 }},
		{"age above limit", func(config *jetstream.StreamConfig) { config.MaxAge = maximumJetStreamMessageAge + time.Second }},
		{"foreign subject", func(config *jetstream.StreamConfig) { config.Subjects = append(config.Subjects, "other.>") }},
		{"republished envelope", func(config *jetstream.StreamConfig) {
			config.RePublish = &jetstream.RePublish{Source: prefix + ".>", Destination: "external.>"}
		}},
		{"mirrored envelope", func(config *jetstream.StreamConfig) { config.Mirror = &jetstream.StreamSource{Name: "OTHER"} }},
		{"sourced envelope", func(config *jetstream.StreamConfig) { config.Sources = []*jetstream.StreamSource{{Name: "OTHER"}} }},
		{"transformed subject", func(config *jetstream.StreamConfig) {
			config.SubjectTransform = &jetstream.SubjectTransformConfig{Source: prefix + ".>", Destination: "external.>"}
		}},
		{"direct read", func(config *jetstream.StreamConfig) { config.AllowDirect = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			config.Subjects = append([]string(nil), base.Subjects...)
			test.mutate(&config)
			if err := validJetStreamStreamPolicy(config, name, prefix); err == nil {
				t.Fatal("unsafe broker policy accepted")
			}
		})
	}
}
