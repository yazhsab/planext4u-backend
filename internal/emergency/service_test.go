package emergency

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBEP5009AtomicAssignmentLocationTTLTransitionsAndSLA(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	service, err := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN", AssignmentSLA: 5 * time.Minute, LocationMaxAge: 2 * time.Minute}, clock)
	if err != nil {
		t.Fatal(err)
	}
	requester := emergencyActorFor("customer-requester-001", "CUSTOMER", false)
	created, _, err := service.Create(requester, "emergency-create-001", CreateRequest{Category: "MEDICAL", Description: "Neighbour needs urgent assistance", Priority: "CRITICAL", LocationConsent: true, Location: Location{Latitude: 13.03, Longitude: 80.27, AccuracyM: 12}})
	if err != nil || created.CurrentLocation == nil {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	responders := []Actor{emergencyActorFor("responder-001", "EMERGENCY_RESPONDER", true), emergencyActorFor("responder-002", "EMERGENCY_RESPONDER", true)}
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	for index, actor := range responders {
		go func(index int, actor Actor) {
			defer wait.Done()
			_, _, err := service.Accept(actor, "emergency-accept-00"+string(rune('1'+index)), created.ID)
			results <- err
		}(index, actor)
	}
	wait.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	value, err := service.Get(requester, created.ID)
	if err != nil || value.AssignedResponder == "" {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	assigned := emergencyActorFor(value.AssignedResponder, "EMERGENCY_RESPONDER", true)
	value, _, err = service.Transition(assigned, "emergency-route-001", value.ID, value.Revision, TransitionRequest{Status: "EN_ROUTE", Note: "Responder travelling"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	value, err = service.Get(requester, created.ID)
	if err != nil || value.CurrentLocation != nil {
		t.Fatalf("stale location leaked=%#v err=%v", value.CurrentLocation, err)
	}
	value, err = service.UpdateLocation(requester, created.ID, LocationRequest{Consent: false})
	if err != nil || value.LocationConsent {
		t.Fatalf("revocation=%#v err=%v", value, err)
	}
}

func TestBEP5009EscalationAndAggregateOnlyReporting(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service, err := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN", AssignmentSLA: time.Minute}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	requester := emergencyActorFor("customer-requester-001", "CUSTOMER", false)
	created, _, err := service.Create(requester, "emergency-create-002", CreateRequest{Category: "FIRE", Description: "Smoke visible from nearby building", Priority: "CRITICAL", LocationConsent: true, Location: Location{Latitude: 13.03, Longitude: 80.27, AccuracyM: 20}})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	adminNoMFA := emergencyActorFor("emergency-admin-001", "EMERGENCY_ADMIN", false)
	if _, err = service.RunEscalations(adminNoMFA); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("MFA error=%v", err)
	}
	admin := emergencyActorFor("emergency-admin-001", "EMERGENCY_ADMIN", true)
	count, err := service.RunEscalations(admin)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	value, err := service.Get(admin, created.ID)
	if err != nil || value.EscalationLevel != 1 {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	report, err := service.SLA(admin)
	if err != nil || report.Breached != 1 || report.LocationPrecision != "aggregate_only" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestBEP5009RequesterAndAssignedResponderCanCommunicate(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service, err := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	requester := emergencyActorFor("customer-requester-001", "CUSTOMER", false)
	created, _, err := service.Create(requester, "emergency-create-003", CreateRequest{Category: "SAFETY", Description: "Need verified assistance at the location", Priority: "HIGH", LocationConsent: true, Location: Location{Latitude: 13.03, Longitude: 80.27, AccuracyM: 10}})
	if err != nil {
		t.Fatal(err)
	}
	responder := emergencyActorFor("responder-001", "EMERGENCY_RESPONDER", true)
	if _, _, err = service.Accept(responder, "emergency-accept-003", created.ID); err != nil {
		t.Fatal(err)
	}
	message, replay, err := service.SendMessage(requester, "emergency-message-001", created.ID, MessageRequest{Body: "I am beside the main entrance"})
	if err != nil || replay || message.Status != "DELIVERED" {
		t.Fatalf("message=%#v replay=%v err=%v", message, replay, err)
	}
	if _, _, err = service.SendMessage(responder, "emergency-message-002", created.ID, MessageRequest{Body: "I will arrive in two minutes"}); err != nil {
		t.Fatal(err)
	}
	messages, err := service.Messages(requester, created.ID)
	if err != nil || len(messages) != 2 || messages[1].SenderID != responder.Subject {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func emergencyActorFor(subject, role string, mfa bool) Actor {
	return Actor{TenantID: "tenant-synthetic-001", Country: "IN", Subject: subject, Roles: []string{role}, MFAVerified: mfa}
}
