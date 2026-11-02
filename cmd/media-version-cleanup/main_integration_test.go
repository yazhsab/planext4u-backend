//go:build integration

package main

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDedicatedMediaCleanupLoginDisposablePostgres(t *testing.T) {
	raw := os.Getenv("MEDIA_DATABASE_TEST_URL")
	if os.Getenv("MEDIA_PURGE_TEST_DISPOSABLE") != "1" || raw == "" {
		t.Skip("dedicated cleanup login is tested only in disposable PostgreSQL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path != "/planext4u_phase0_verification" || parsed.Hostname() != "postgres" {
		t.Fatal("dedicated cleanup login test requires the disposable verification database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	if _, err := owner.Exec(ctx, `DO $$ BEGIN
	 IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='planext4u_media_cleanup') THEN
	  CREATE ROLE planext4u_media_cleanup NOLOGIN;
	 END IF;
	 END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE ROLE planext4u_media_cleanup_login LOGIN PASSWORD 'synthetic-cleanup-only'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = owner.Exec(cleanupCtx, `DROP ROLE IF EXISTS planext4u_media_cleanup_login`)
	})
	if _, err := owner.Exec(ctx, `GRANT planext4u_media_cleanup TO planext4u_media_cleanup_login`); err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(cleanupLogin, "synthetic-cleanup-only")
	config, err := pgxpool.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns, config.MaxConns = 0, 1
	config.AfterConnect = assumeCleanupRole
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("dedicated restricted login could not assume cleanup role: %v", err)
	}
	var session, current string
	if err := pool.QueryRow(ctx, `SELECT session_user,current_user`).Scan(&session, &current); err != nil ||
		session != cleanupLogin || current != "planext4u_media_cleanup" {
		pool.Close()
		t.Fatalf("cleanup role boundary was not established: session=%q current=%q err=%v", session, current, err)
	}
	pool.Close()
	// A privileged/ordinary login must fail the same callback even if it can
	// technically SET ROLE through the migration owner's role membership.
	shared, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	shared.MinConns, shared.MaxConns = 0, 1
	shared.AfterConnect = assumeCleanupRole
	wrongPool, err := pgxpool.NewWithConfig(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongPool.Close()
	if err := wrongPool.Ping(ctx); err == nil {
		t.Fatal("privileged verification login was accepted as the cleanup identity")
	}
}
