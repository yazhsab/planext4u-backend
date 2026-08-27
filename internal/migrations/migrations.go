package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var serviceOrder = []string{"platform", "audit", "catalog", "commerce", "configuration", "identity", "media", "messaging", "notification"}

type Migration struct {
	Service  string
	Version  int64
	Name     string
	Up       string
	Down     string
	Checksum string
}

func Discover(root string) ([]Migration, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read migration root: %w", err)
	}
	var result []Migration
	namePattern := regexp.MustCompile(`^([0-9]{6})_([a-z0-9_]+)\.up\.sql$`)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		service := entry.Name()
		files, err := os.ReadDir(filepath.Join(root, service))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			match := namePattern.FindStringSubmatch(file.Name())
			if len(match) == 0 {
				continue
			}
			version, _ := strconv.ParseInt(match[1], 10, 64)
			upPath := filepath.Join(root, service, file.Name())
			downPath := filepath.Join(root, service, strings.TrimSuffix(file.Name(), ".up.sql")+".down.sql")
			up, readErr := os.ReadFile(upPath)
			if readErr != nil {
				return nil, readErr
			}
			down, readErr := os.ReadFile(downPath)
			if readErr != nil {
				return nil, fmt.Errorf("migration %s is missing its down pair: %w", upPath, readErr)
			}
			digest := sha256.Sum256(up)
			result = append(result, Migration{Service: service, Version: version, Name: match[2], Up: string(up), Down: string(down), Checksum: hex.EncodeToString(digest[:])})
		}
	}
	sortMigrations(result, false)
	return result, nil
}

func Validate(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("no migrations discovered")
	}
	services := map[string][]Migration{}
	for _, migration := range migrations {
		if !contains(serviceOrder, migration.Service) || migration.Version < 1 || strings.TrimSpace(migration.Up) == "" || strings.TrimSpace(migration.Down) == "" {
			return fmt.Errorf("invalid migration %s/%06d", migration.Service, migration.Version)
		}
		services[migration.Service] = append(services[migration.Service], migration)
		if migration.Service != "platform" {
			if err := validateOwnership(migration); err != nil {
				return err
			}
		}
		if regexp.MustCompile(`(?i)(postgres(?:ql)?://|\bpassword\s*=|\bsecret\s*=)`).MatchString(migration.Up) {
			return fmt.Errorf("migration %s/%06d contains credential material", migration.Service, migration.Version)
		}
		if migration.Version > 1 && regexp.MustCompile(`(?is)\b(DROP\s+(TABLE|COLUMN)|ALTER\s+TABLE.+RENAME|ALTER\s+TABLE.+SET\s+NOT\s+NULL)\b`).MatchString(migration.Up) {
			return fmt.Errorf("migration %s/%06d contains a backward-incompatible upgrade", migration.Service, migration.Version)
		}
	}
	for _, service := range serviceOrder {
		items := services[service]
		if len(items) == 0 {
			return fmt.Errorf("service %s has no migration", service)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
		for index, migration := range items {
			if migration.Version != int64(index+1) {
				return fmt.Errorf("service %s migration versions are not contiguous from one", service)
			}
		}
	}
	return nil
}

func validateOwnership(migration Migration) error {
	allSchemas := []string{"identity", "configuration", "catalog", "commerce", "media", "audit", "messaging", "notification"}
	for _, schema := range allSchemas {
		if schema != migration.Service && regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(schema)+`\.`).MatchString(migration.Up) {
			return fmt.Errorf("migration %s/%06d writes outside its owned schema via %s", migration.Service, migration.Version, schema)
		}
	}
	if migration.Version == 1 {
		required := []string{"AUTHORIZATION planext4u_" + migration.Service + "_owner", "REVOKE ALL ON SCHEMA " + migration.Service + " FROM PUBLIC", "GRANT USAGE ON SCHEMA " + migration.Service + " TO planext4u_" + migration.Service + "_runtime"}
		for _, marker := range required {
			if !strings.Contains(migration.Up, marker) {
				return fmt.Errorf("migration %s/%06d is missing ownership control %q", migration.Service, migration.Version, marker)
			}
		}
	}
	return nil
}

type Runner struct{ connection *pgx.Conn }

func NewRunner(connection *pgx.Conn) (*Runner, error) {
	if connection == nil {
		return nil, errors.New("migration connection is required")
	}
	return &Runner{connection: connection}, nil
}

func (runner *Runner) Up(ctx context.Context, migrations []Migration, service string) error {
	if err := runner.prepare(ctx); err != nil {
		return err
	}
	items := selected(migrations, service, false)
	for _, migration := range items {
		tx, err := runner.connection.Begin(ctx)
		if err != nil {
			return err
		}
		var checksum string
		err = tx.QueryRow(ctx, `SELECT checksum FROM platform_migrations.applied WHERE service = $1 AND version = $2`, migration.Service, migration.Version).Scan(&checksum)
		if err == nil {
			if checksum != migration.Checksum {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("migration checksum drift for %s/%06d", migration.Service, migration.Version)
			}
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return err
		}
		if _, err = tx.Exec(ctx, migration.Up); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO platform_migrations.applied(service, version, name, checksum) VALUES ($1, $2, $3, $4)`, migration.Service, migration.Version, migration.Name, migration.Checksum)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s/%06d: %w", migration.Service, migration.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) Down(ctx context.Context, migrations []Migration, service string, steps int) error {
	if steps < 1 {
		return errors.New("down migration steps must be positive")
	}
	if err := runner.prepare(ctx); err != nil {
		return err
	}
	items := selected(migrations, service, true)
	applied := 0
	for _, migration := range items {
		if applied >= steps && service != "all" {
			break
		}
		var exists bool
		if err := runner.connection.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform_migrations.applied WHERE service = $1 AND version = $2)`, migration.Service, migration.Version).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		tx, err := runner.connection.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, migration.Down); err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM platform_migrations.applied WHERE service = $1 AND version = $2`, migration.Service, migration.Version)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("revert migration %s/%06d: %w", migration.Service, migration.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		applied++
	}
	return nil
}

func (runner *Runner) prepare(ctx context.Context) error {
	_, err := runner.connection.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS platform_migrations;
REVOKE ALL ON SCHEMA platform_migrations FROM PUBLIC;
CREATE TABLE IF NOT EXISTS platform_migrations.applied (
  service text NOT NULL,
  version bigint NOT NULL,
  name text NOT NULL,
  checksum char(64) NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (service, version)
);`)
	return err
}

func selected(migrations []Migration, service string, reverse bool) []Migration {
	var result []Migration
	for _, migration := range migrations {
		if service == "" || service == "all" || migration.Service == service {
			result = append(result, migration)
		}
	}
	sortMigrations(result, reverse)
	return result
}

func sortMigrations(migrations []Migration, reverse bool) {
	order := func(service string) int {
		for index, candidate := range serviceOrder {
			if candidate == service {
				return index
			}
		}
		return len(serviceOrder)
	}
	sort.Slice(migrations, func(i, j int) bool {
		left, right := order(migrations[i].Service), order(migrations[j].Service)
		if reverse {
			if left == right {
				return migrations[i].Version > migrations[j].Version
			}
			return left > right
		}
		if left == right {
			return migrations[i].Version < migrations[j].Version
		}
		return left < right
	})
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
