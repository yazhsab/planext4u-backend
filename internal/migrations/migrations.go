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

var serviceOrder = []string{"platform", "audit", "catalog", "commerce", "inventory", "payment", "ordering", "booking", "supply", "food", "fulfillment", "social", "governance", "local_verticals", "emergency", "wallet", "support", "configuration", "admin", "customer_web", "identity", "bankverification", "media", "messaging", "notification"}

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
	up := migration.Up
	// The identity governance commands have exactly one dependency on the
	// admin-owned live, fresh-MFA operator lock. It is accepted only inside
	// the named private verifier function in this migration; foreign writes
	// and any additional admin references remain prohibited.
	if migration.Service == "identity" && migration.Version == 18 && migration.Name == "deletion_governance_commands" &&
		strings.Count(up, "CREATE FUNCTION identity.require_deletion_governor(") == 1 && strings.Contains(up, "SECURITY DEFINER") {
		call := regexp.MustCompile(`(?m)^[ \t]*IF admin\.lock_deletion_governor\(t,c,a,s,rev\) IS DISTINCT FROM true THEN[ \t]*\r?$`)
		if len(call.FindAllStringIndex(up, -1)) == 1 {
			up = call.ReplaceAllString(up, "")
		}
	}
	// The two privacy owner exports have one audited read-only dependency on
	// identity's exact live-session authority. The exception is bound to the
	// migration identity, function declaration and one complete IF line; all
	// other foreign-schema references, including writes in the same file, are
	// still rejected below.
	var exportFunction string
	switch {
	case migration.Service == "supply" && migration.Version == 6 && migration.Name == "privacy_application_export":
		exportFunction = "supply.privacy_export_vendor_applications"
	case migration.Service == "fulfillment" && migration.Version == 10 && migration.Name == "privacy_rider_export":
		exportFunction = "fulfillment.privacy_export_rider_profiles"
	}
	if exportFunction != "" && strings.Count(up, "CREATE FUNCTION "+exportFunction+"(") == 1 && strings.Contains(up, "SECURITY DEFINER") {
		call := regexp.MustCompile(`(?m)^[ \t]*IF NOT identity\.privacy_export_session\(t,c,s,sid\) THEN RETURN; END IF;[ \t]*\r?$`)
		if len(call.FindAllStringIndex(up, -1)) == 1 {
			up = call.ReplaceAllString(up, "")
		}
	}
	// The dedicated media cleanup owner needs three read-only capabilities:
	// one locked due-account scope and two legacy-document reference checks.
	// Permit only the exact calls inside the reviewed SECURITY DEFINER
	// function, including its narrow avatar update in migration 000009.
	mediaDuePurgeMigration := migration.Service == "media" &&
		((migration.Version == 8 && migration.Name == "due_version_cleanup" &&
			strings.Count(up, "CREATE FUNCTION media.prepare_due_version_purge(") == 1) ||
			(migration.Version == 9 && migration.Name == "due_avatar_version_cleanup" &&
				strings.Count(up, "CREATE OR REPLACE FUNCTION media.prepare_due_version_purge(") == 1))
	if mediaDuePurgeMigration && strings.Contains(up, "SECURITY DEFINER") {
		calls := []string{
			`(?m)^[ \t]*SELECT \* INTO scope FROM identity\.privacy_media_erasure_country_scope\(r\);[ \t]*\r?$`,
			`(?m)^[ \t]*OR supply\.private_identity_evidence_referenced\(asset\.tenant_id,asset\.country,asset\.owner_id,a\)[ \t]*\r?$`,
			`(?m)^[ \t]*OR fulfillment\.private_identity_evidence_referenced\(asset\.tenant_id,asset\.country,asset\.owner_id,a\)\)? THEN[ \t]*\r?$`,
		}
		for _, expression := range calls {
			call := regexp.MustCompile(expression)
			if len(call.FindAllStringIndex(up, -1)) == 1 {
				up = call.ReplaceAllString(up, "")
			}
		}
	}
	// The two owner-owned document triggers have one narrow, late-bound media
	// capability each. The supply trigger calls it for both current documents
	// and historical submissions; every other foreign-schema reference still
	// fails the ownership check below.
	if migration.Version == 14 && migration.Name == "media_cleanup_reference_check" {
		var expressions []string
		switch migration.Service {
		case "supply":
			if strings.Count(up, "CREATE FUNCTION supply.guard_new_private_media_reference()") == 1 && strings.Contains(up, "SECURITY DEFINER") {
				expressions = []string{`(?m)^[ \t]*IF NOT media\.identity_document_reference_allowed\([ \t]*\r?$`}
			}
		case "fulfillment":
			if strings.Count(up, "CREATE FUNCTION fulfillment.guard_new_private_media_reference()") == 1 && strings.Contains(up, "SECURITY DEFINER") {
				expressions = []string{`(?m)^[ \t]*IF NOT media\.identity_document_reference_allowed\([^\r\n]*\) THEN[ \t]*\r?$`}
			}
		}
		for _, expression := range expressions {
			call := regexp.MustCompile(expression)
			expected := 1
			if migration.Service == "supply" {
				expected = 2
			}
			if len(call.FindAllStringIndex(up, -1)) == expected {
				up = call.ReplaceAllString(up, "")
			}
		}
	}
	// The opt-in owner scrub has two equally narrow capabilities: obtain the
	// due request's identity-owned scope and record a RETAINED receipt after its
	// own onboarding scrub. No other foreign reference is suppressed.
	var scrubFunction, scopeCall, receiptCall string
	scopeCount := 1
	switch {
	case migration.Service == "supply" && migration.Version == 8 && migration.Name == "privacy_onboarding_scrub":
		scrubFunction = "supply.privacy_scrub_onboarding"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_supply_erasure_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_supply_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "supply" && migration.Version == 12 && migration.Name == "country_scoped_onboarding_scrub":
		scrubFunction = "supply.privacy_scrub_onboarding"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_supply_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_supply_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "supply" && migration.Version == 13 && migration.Name == "vendor_presence_privacy":
		scrubFunction = "supply.privacy_scrub_onboarding"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_supply_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_supply_record_retained\(r,policy,evidence\);[ \t]*\r?$`
		scopeCount = 2
	case migration.Service == "fulfillment" && migration.Version == 11 && migration.Name == "privacy_onboarding_scrub":
		scrubFunction = "fulfillment.privacy_scrub_onboarding"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_fulfillment_erasure_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_fulfillment_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "fulfillment" && migration.Version == 12 && migration.Name == "country_scoped_onboarding_scrub":
		scrubFunction = "fulfillment.privacy_scrub_onboarding"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_fulfillment_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_fulfillment_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "fulfillment" && migration.Version == 13 && migration.Name == "ephemeral_rider_location_scrub":
		scrubFunction = "fulfillment.privacy_scrub_onboarding"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_fulfillment_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_fulfillment_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "bankverification" && migration.Version == 2 && migration.Name == "privacy_link_scrub":
		scrubFunction = "bankverification.privacy_scrub_link_attempts"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_bankverification_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_bankverification_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "notification" && migration.Version == 6 && migration.Name == "privacy_push_token_scrub":
		scrubFunction = "notification.privacy_scrub_push_tokens"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_notification_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_notification_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "customer_web" && migration.Version == 2 && migration.Name == "privacy_session_scrub":
		scrubFunction = "customer_web.privacy_scrub_sessions"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_customer_web_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_customer_web_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "social" && migration.Version == 5 && migration.Name == "ephemeral_privacy_scrub":
		scrubFunction = "social.privacy_scrub_ephemeral"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_social_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_social_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	case migration.Service == "messaging" && migration.Version == 5 && migration.Name == "privacy_notification_outbox_scrub":
		scrubFunction = "messaging.privacy_scrub_notification_outbox"
		scopeCall = `(?m)^[ \t]*FROM identity\.privacy_messaging_erasure_country_scope\(r\) scope;[ \t]*\r?$`
		receiptCall = `(?m)^[ \t]*PERFORM identity\.privacy_messaging_record_retained\(r,policy,evidence\);[ \t]*\r?$`
	}
	declarations := strings.Count(up, "CREATE FUNCTION "+scrubFunction+"(") + strings.Count(up, "CREATE OR REPLACE FUNCTION "+scrubFunction+"(")
	if scrubFunction != "" && declarations == 1 && strings.Contains(up, "SECURITY DEFINER") {
		scope := regexp.MustCompile(scopeCall)
		receipt := regexp.MustCompile(receiptCall)
		if len(scope.FindAllStringIndex(up, -1)) == scopeCount && len(receipt.FindAllStringIndex(up, -1)) == 2 {
			up = scope.ReplaceAllString(up, "")
			up = receipt.ReplaceAllString(up, "")
		}
	}
	allSchemas := []string{"identity", "configuration", "admin", "customer_web", "catalog", "commerce", "inventory", "payment", "ordering", "booking", "supply", "food", "fulfillment", "social", "governance", "local_verticals", "emergency", "wallet", "support", "bankverification", "media", "audit", "messaging", "notification"}
	for _, schema := range allSchemas {
		if schema != migration.Service && regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(schema)+`\.`).MatchString(up) {
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
	if service == "all" {
		if err := runner.refusePrivacyEvidenceRollback(ctx); err != nil {
			return err
		}
	}
	if service == "identity" {
		var dependent bool
		if err := runner.connection.QueryRow(ctx, `SELECT EXISTS(
		 SELECT 1 FROM platform_migrations.applied
		 WHERE service IN ('supply','fulfillment','bankverification','notification','customer_web','social','messaging','media'))`).Scan(&dependent); err != nil {
			return err
		}
		if dependent {
			return errors.New("identity rollback requires dependent owner services to be reverted first")
		}
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

// A complete rollback is meant for empty disposable installations. Check
// immutable/partial privacy evidence before the first per-migration commit so
// an occupied owner cannot be discovered only after other services are gone.
// Owner migration guards remain the final authority if state changes later.
func (runner *Runner) refusePrivacyEvidenceRollback(ctx context.Context) error {
	for _, relation := range []string{
		"identity.deletion_owner_receipts",
		"supply.privacy_erasure_runs",
		"supply.privacy_presence_erasure_runs",
		"fulfillment.privacy_erasure_runs",
		"bankverification.privacy_link_scrub_runs",
		"notification.privacy_push_scrub_runs",
		"customer_web.privacy_session_scrub_runs",
		"social.privacy_ephemeral_erasure_runs",
		"media.object_purge_evidence",
		"messaging.privacy_outbox_scrub_runs",
		"messaging.privacy_outbox_scrubbed_events",
	} {
		var exists bool
		if err := runner.connection.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, relation).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		var occupied bool
		if err := runner.connection.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+relation+")").Scan(&occupied); err != nil {
			return err
		}
		if occupied {
			return fmt.Errorf("privacy evidence in %s blocks complete rollback", relation)
		}
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
		if reverse {
			// Owner routines depend on identity capabilities. Revert identity only
			// after the other services, while platform roles remain last.
			if service == "platform" {
				return -1
			}
			if service == "identity" {
				return 0
			}
		}
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
