//go:build integration

package emergency

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	emergencyTenantOne    = "11000000-0000-4000-8000-000000000001"
	emergencyTenantTwo    = "11000000-0000-4000-8000-000000000002"
	emergencyCustomer     = "21000000-0000-4000-8000-000000000001"
	emergencyResponderOne = "31000000-0000-4000-8000-000000000001"
	emergencyResponderTwo = "31000000-0000-4000-8000-000000000002"
	emergencyAdmin        = "41000000-0000-4000-8000-000000000001"
	emergencyRider        = "51000000-0000-4000-8000-000000000001"
)

func TestPostgresEmergencyEncryptionAssignmentRestartLifecycleAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("EMERGENCY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("EMERGENCY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS emergency CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS emergency CASCADE`)
	})
	for _, path := range []string{"../../migrations/platform/000006_phase5_roles.up.sql", "../../migrations/emergency/000001_emergency.up.sql", "../../migrations/emergency/000002_durable_emergency_runtime.up.sql"} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `INSERT INTO emergency.policies (tenant_id,country,version,assignment_sla_seconds,location_max_age_seconds,updated_at) VALUES ($1,'IN','emergency-v1',300,120,$2)`, emergencyTenantOne, now)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("emergency-encryption-key-32-byte")
	service, err := NewPostgresService(pool, func() time.Time { return now }, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	customer := Actor{TenantID: emergencyTenantOne, Country: "IN", Subject: emergencyCustomer, Roles: []string{"CUSTOMER"}}
	input := CreateRequest{Category: "MEDICAL", Description: "Urgent medical assistance required", Priority: "CRITICAL", LocationConsent: true, Location: Location{Latitude: 13.0827, Longitude: 80.2707, AccuracyM: 5}}
	created, replay, err := service.Create(customer, "emergency-create-postgres-0001", input)
	if err != nil || replay || created.Status != "OPEN" || created.CurrentLocation == nil {
		t.Fatalf("create=%#v replay=%t err=%v", created, replay, err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT encrypted_location FROM emergency.requests WHERE id=$1`, created.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("13.0827")) {
		t.Fatal("location was stored as plaintext")
	}

	responders := []Actor{
		{TenantID: emergencyTenantOne, Country: "IN", Subject: emergencyResponderOne, Roles: []string{"EMERGENCY_RESPONDER"}, MFAVerified: true},
		{TenantID: emergencyTenantOne, Country: "IN", Subject: emergencyResponderTwo, Roles: []string{"EMERGENCY_RESPONDER"}, MFAVerified: true},
	}
	type acceptResult struct {
		actor Actor
		value Request
		err   error
	}
	results := make(chan acceptResult, 2)
	var wait sync.WaitGroup
	for index, actor := range responders {
		wait.Add(1)
		go func(index int, actor Actor) {
			defer wait.Done()
			value, _, callErr := service.Accept(actor, "emergency-accept-postgres-000"+string(rune('1'+index)), created.ID)
			results <- acceptResult{actor: actor, value: value, err: callErr}
		}(index, actor)
	}
	wait.Wait()
	close(results)
	var winner Actor
	var assigned Request
	succeeded, conflicted := 0, 0
	for result := range results {
		if result.err == nil {
			succeeded++
			winner, assigned = result.actor, result.value
		} else if errors.Is(result.err, ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("accept error=%v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 || assigned.AssignedResponder != winner.Subject {
		t.Fatalf("assignment race success=%d conflict=%d value=%#v", succeeded, conflicted, assigned)
	}

	restarted, err := NewPostgresService(pool, func() time.Time { return now }, key)
	if err != nil {
		t.Fatal(err)
	}
	winnerIndex := 0
	if winner.Subject == emergencyResponderTwo {
		winnerIndex = 1
	}
	replayed, replay, err := restarted.Accept(winner, "emergency-accept-postgres-000"+string(rune('1'+winnerIndex)), created.ID)
	if err != nil || !replay || replayed.AssignedResponder != winner.Subject {
		t.Fatalf("restart replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	message, replay, err := restarted.SendMessage(customer, "emergency-message-postgres-001", created.ID, MessageRequest{Body: "Please reach the south entrance"})
	if err != nil || replay || message.Status != "DELIVERED" {
		t.Fatalf("message=%#v replay=%t err=%v", message, replay, err)
	}
	var bodyCiphertext []byte
	if err := pool.QueryRow(ctx, `SELECT body_ciphertext FROM emergency.communications WHERE id=$1`, message.ID).Scan(&bodyCiphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bodyCiphertext, []byte("south entrance")) {
		t.Fatal("communication was stored as plaintext")
	}
	messages, err := restarted.Messages(winner, created.ID)
	if err != nil || len(messages) != 1 || messages[0].Body != "Please reach the south entrance" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}

	current := assigned
	for index, status := range []string{"EN_ROUTE", "ON_SCENE", "RESOLVED"} {
		current, replay, err = restarted.Transition(winner, "emergency-transition-postgres-"+string(rune('1'+index))+"001", created.ID, current.Revision, TransitionRequest{Status: status, Note: "Responder lifecycle update"})
		if err != nil || replay || current.Status != status {
			t.Fatalf("transition %s value=%#v replay=%t err=%v", status, current, replay, err)
		}
	}
	if current.CurrentLocation != nil {
		t.Fatal("resolved request retained precise location")
	}
	var storedLocation []byte
	if err := pool.QueryRow(ctx, `SELECT encrypted_location FROM emergency.requests WHERE id=$1`, created.ID).Scan(&storedLocation); err != nil {
		t.Fatal(err)
	}
	if storedLocation != nil {
		t.Fatal("resolved location ciphertext was not erased")
	}
	admin := Actor{TenantID: emergencyTenantOne, Country: "IN", Subject: emergencyAdmin, Roles: []string{"EMERGENCY_ADMIN"}, MFAVerified: true}
	report, err := restarted.SLA(admin)
	if err != nil || report.Resolved != 1 || report.AverageAcceptSecs != 0 || report.LocationPrecision != "aggregate_only" {
		t.Fatalf("sla=%#v err=%v", report, err)
	}
	wrongTenant := Actor{TenantID: emergencyTenantTwo, Country: "IN", Subject: emergencyCustomer, Roles: []string{"CUSTOMER"}}
	if _, err := restarted.Get(wrongTenant, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant get error=%v", err)
	}
	riderHandler, err := NewRiderHandler(restarted, RiderDutyVerifierFunc(func(actor Actor) (bool, error) {
		return actor.Subject == emergencyRider, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	riderRequest := httptest.NewRequest(http.MethodPost, "/v1/rider/emergency-incidents", strings.NewReader(`{"category":"ACCIDENT","description":"Rider requires urgent assistance after an accident","priority":"CRITICAL","location_consent":true,"location":{"latitude":13.08,"longitude":80.27,"accuracy_m":9}}`))
	riderRequest.Header.Set("X-Planext4u-Tenant", emergencyTenantOne)
	riderRequest.Header.Set("X-Planext4u-Country", "IN")
	riderRequest.Header.Set("X-Planext4u-Subject", emergencyRider)
	riderRequest.Header.Set("X-Planext4u-Roles", "RIDER")
	riderRequest.Header.Set("Idempotency-Key", "rider-emergency-postgres-0001")
	riderResponse := httptest.NewRecorder()
	riderHandler.ServeHTTP(riderResponse, riderRequest)
	if riderResponse.Code != http.StatusCreated || !strings.Contains(riderResponse.Body.String(), `"status":"OPEN"`) {
		t.Fatalf("rider emergency status=%d body=%s", riderResponse.Code, riderResponse.Body.String())
	}
	var requests, timeline, communications, replays int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM emergency.requests),(SELECT count(*) FROM emergency.timeline),(SELECT count(*) FROM emergency.communications),(SELECT count(*) FROM emergency.idempotency_records)`).Scan(&requests, &timeline, &communications, &replays); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || timeline != 7 || communications != 1 || replays != 7 {
		t.Fatalf("rows requests=%d timeline=%d messages=%d replays=%d", requests, timeline, communications, replays)
	}
}
