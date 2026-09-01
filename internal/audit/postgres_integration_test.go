//go:build integration

package audit

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresAuditTenantChainsAreDurableIsolatedAndAppendOnly(t *testing.T) {
	databaseURL := os.Getenv("AUDIT_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("AUDIT_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS audit CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS audit CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/audit/000001_audit.up.sql",
		"../../migrations/audit/000002_tenant_chain_sequence.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 123456000, time.UTC)
	principal := Principal{
		TenantID:  "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f",
		SubjectID: "admin-postgres",
		Capabilities: map[string]bool{
			"audit.write":  true,
			"audit.read":   true,
			"audit.export": true,
		},
		AuthenticatedAt: now,
	}
	services := make([]*Service, 4)
	for index := range services {
		services[index], err = NewService(repository, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan Entry, len(services))
	errorsChannel := make(chan error, len(services))
	var wait sync.WaitGroup
	for index, service := range services {
		wait.Add(1)
		go func(index int, service *Service) {
			defer wait.Done()
			<-start
			request := postgresRecordRequest(now, "admin.setting.updated", "setting-postgres-"+string(rune('1'+index)))
			entry, recordErr := service.Record(ctx, principal, request)
			if recordErr != nil {
				errorsChannel <- recordErr
				return
			}
			results <- entry
		}(index, service)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for recordErr := range errorsChannel {
		t.Fatalf("concurrent audit append: %v", recordErr)
	}
	sequences := make([]int, 0, len(services))
	for entry := range results {
		sequences = append(sequences, int(entry.Sequence))
	}
	sort.Ints(sequences)
	if len(sequences) != 4 || sequences[0] != 1 || sequences[3] != 4 {
		t.Fatalf("tenant sequences=%v", sequences)
	}
	page, err := services[0].Search(ctx, principal, SearchFilter{Limit: 10})
	if err != nil || len(page.Entries) != 4 || !verify(page.Entries) {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	other := principal
	other.TenantID = "23bf7434-3643-49df-9928-c9169011f69d"
	otherEntry, err := services[0].Record(ctx, other, postgresRecordRequest(now, "admin.setting.updated", "setting-other"))
	if err != nil || otherEntry.Sequence != 1 {
		t.Fatalf("other tenant entry=%#v err=%v", otherEntry, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit.events SET action = 'tampered' WHERE tenant_id = $1`, principal.TenantID); err == nil {
		t.Fatal("append-only audit row accepted an update")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit.events WHERE tenant_id = $1`, principal.TenantID); err == nil {
		t.Fatal("append-only audit row accepted a delete")
	}
	bad := page.Entries[len(page.Entries)-1]
	bad.ID = "8beed658-ab32-4787-9c3c-ae02a59192c5"
	bad.Hash = hashEntry(bad)
	if err := repository.Append(ctx, bad); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale chain append error=%v", err)
	}
}

func postgresRecordRequest(now time.Time, action, targetID string) RecordRequest {
	return RecordRequest{
		Country:       "IN",
		Actor:         Actor{SubjectID: "admin-postgres", ActorType: "USER"},
		Action:        action,
		Target:        Target{Type: "configuration", ID: targetID},
		Outcome:       OutcomeSucceeded,
		ReasonCode:    "APPROVED_CHANGE",
		CorrelationID: "correlation-postgres",
		OccurredAt:    now.Add(-time.Minute),
		Before:        map[string]any{"token": "secret"},
		After:         map[string]any{"enabled": true},
	}
}
