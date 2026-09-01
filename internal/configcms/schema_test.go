package configcms

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestConfigurationMigrationSupportsAtomicAuditedPublication(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../migrations/configuration/000001_configuration.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(contents)
	for _, required := range []string{
		"document jsonb NOT NULL",
		"PRIMARY KEY (tenant_id, country)",
		"UNIQUE (tenant_id, country, new_revision)",
		"CHECK (length(reason) BETWEEN 1 AND 240)",
	} {
		if !strings.Contains(schema, required) {
			t.Errorf("configuration migration is missing %q", required)
		}
	}
	if _, err := NewPostgresRepository(nil); !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("nil PostgreSQL pool error=%v", err)
	}
	draftContents, err := os.ReadFile("../../migrations/configuration/000002_page_drafts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	draftSchema := string(draftContents)
	for _, required := range []string{"CREATE TABLE configuration.page_drafts", "document jsonb NOT NULL", "PRIMARY KEY (tenant_id, country, page_id)", "updated_by text NOT NULL"} {
		if !strings.Contains(draftSchema, required) {
			t.Errorf("configuration draft migration is missing %q", required)
		}
	}
	workspaceContents, err := os.ReadFile("../../migrations/configuration/000003_workspace_drafts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	workspaceSchema := string(workspaceContents)
	for _, required := range []string{"CREATE TABLE configuration.workspace_drafts", "PRIMARY KEY (tenant_id,country)", "updated_by text NOT NULL"} {
		if !strings.Contains(workspaceSchema, required) {
			t.Errorf("configuration workspace migration is missing %q", required)
		}
	}
}
