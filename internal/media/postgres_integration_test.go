//go:build integration

package media

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMediaLifecycleIsDurableTenantScopedAndVersioned(t *testing.T) {
	databaseURL := os.Getenv("MEDIA_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("MEDIA_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS media CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS media CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/media/000001_media.up.sql",
		"../../migrations/media/000002_media_lifecycle.up.sql",
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
	now := time.Date(2026, 8, 31, 13, 0, 0, 123456000, time.UTC)
	objects := &postgresTestObjects{metadata: map[string]ObjectMetadata{}, deleted: map[string]bool{}}
	service, err := NewService(repository, postgresTestSigner{}, objects, postgresTestScanner{}, 10*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f"
	grant, err := service.Presign(ctx, tenantID, "IN", "owner-postgres", validRequest())
	if err != nil || grant.Asset.Version != 1 {
		t.Fatalf("grant=%#v err=%v", grant, err)
	}
	stored, err := repository.Get(ctx, tenantID, "owner-postgres", grant.Asset.ID)
	if err != nil || stored.Country != "IN" || stored.Purpose != PurposeCatalogImage || stored.State != StatePendingUpload {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if _, err := repository.Get(ctx, "23bf7434-3643-49df-9928-c9169011f69d", "owner-postgres", grant.Asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant read error=%v", err)
	}
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{
		ContentType: grant.Asset.ContentType, SizeBytes: grant.Asset.SizeBytes, SHA256: grant.Asset.SHA256,
	}
	ready, err := service.Complete(ctx, tenantID, "owner-postgres", grant.Asset.ID)
	if err != nil || ready.State != StateReady || ready.Version != 3 || ready.ReadyAt == nil {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	if err := service.Delete(ctx, tenantID, "owner-postgres", ready.ID); err != nil {
		t.Fatal(err)
	}
	stale := ready
	stale.State, stale.Version = StateDeleted, ready.Version+1
	if err := repository.Update(ctx, stale, ready.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if _, err := repository.Get(ctx, tenantID, "owner-postgres", ready.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted asset read error=%v", err)
	}
	var legacyStatus, lifecycle string
	var version int64
	var completedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT asset.status, asset.lifecycle_state, asset.version, session.completed_at
		FROM media.assets AS asset
		JOIN media.upload_sessions AS session ON session.asset_id = asset.id
		WHERE asset.id = $1`, ready.ID).Scan(&legacyStatus, &lifecycle, &version, &completedAt); err != nil {
		t.Fatal(err)
	}
	if legacyStatus != "DELETED" || lifecycle != string(StateDeleted) || version != 4 || completedAt == nil {
		t.Fatalf("legacy=%s lifecycle=%s version=%d completed=%v", legacyStatus, lifecycle, version, completedAt)
	}
}

type postgresTestSigner struct{}

func (postgresTestSigner) PresignPut(_ context.Context, key string, metadata ObjectMetadata, _ time.Time) (string, map[string]string, error) {
	return "https://uploads.example.test/" + key, map[string]string{
		"content-type": metadata.ContentType, "x-amz-checksum-sha256": metadata.SHA256,
	}, nil
}

type postgresTestObjects struct {
	metadata map[string]ObjectMetadata
	deleted  map[string]bool
}

func (objects *postgresTestObjects) Inspect(_ context.Context, key string) (ObjectMetadata, error) {
	value, exists := objects.metadata[key]
	if !exists {
		return ObjectMetadata{}, errors.New("object not found")
	}
	return value, nil
}

func (objects *postgresTestObjects) Delete(_ context.Context, key string) error {
	objects.deleted[key] = true
	return nil
}

type postgresTestScanner struct{}

func (postgresTestScanner) Scan(context.Context, string) (ScanResult, error) {
	return ScanResult{Clean: true}, nil
}
