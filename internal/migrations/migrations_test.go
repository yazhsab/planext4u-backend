package migrations

import (
	"strings"
	"testing"
)

func TestBEMigrate001FromZeroManifestIsComplete(t *testing.T) {
	t.Parallel()
	migrations, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(migrations); err != nil {
		t.Fatal(err)
	}
	if len(migrations) < len(serviceOrder) {
		t.Fatalf("migration count = %d, want at least %d", len(migrations), len(serviceOrder))
	}
	for _, service := range serviceOrder {
		found := false
		for _, migration := range migrations {
			found = found || migration.Service == service
		}
		if !found {
			t.Errorf("missing from-zero migration for %s", service)
		}
	}
}

func TestBEMigrate001RejectsCrossSchemaAndDestructiveUpgrade(t *testing.T) {
	t.Parallel()
	migrations, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	crossSchema := append([]Migration(nil), migrations...)
	crossSchema = append(crossSchema, Migration{Service: "catalog", Version: 2, Name: "illegal_write", Up: "INSERT INTO identity.identities(id) VALUES ('x');", Down: "SELECT 1", Checksum: strings.Repeat("a", 64)})
	if err := Validate(crossSchema); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
		t.Fatalf("cross-schema validation error = %v", err)
	}
	destructive := append([]Migration(nil), migrations...)
	destructive = append(destructive, Migration{Service: "catalog", Version: 2, Name: "breaking", Up: "ALTER TABLE catalog.items DROP COLUMN title;", Down: "SELECT 1", Checksum: strings.Repeat("b", 64)})
	if err := Validate(destructive); err == nil || !strings.Contains(err.Error(), "backward-incompatible") {
		t.Fatalf("destructive validation error = %v", err)
	}
}

func TestBEMigrate001DownOrderDropsPlatformRolesLast(t *testing.T) {
	t.Parallel()
	migrations, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	items := selected(migrations, "all", true)
	if len(items) == 0 || items[len(items)-1].Service != "platform" {
		t.Fatalf("down order = %#v", items)
	}
}
