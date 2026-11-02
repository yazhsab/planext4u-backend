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

func TestPrivacyExportAuthorityExceptionRemainsReadOnlyAndExact(t *testing.T) {
	t.Parallel()
	items, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, item := range items {
		if !((item.Service == "supply" && item.Version == 6) || (item.Service == "fulfillment" && item.Version == 10)) {
			continue
		}
		checked++
		if err := validateOwnership(item); err != nil {
			t.Fatalf("approved owner authority call rejected for %s/%d: %v", item.Service, item.Version, err)
		}
		crossSchemaWrite := item
		crossSchemaWrite.Up += "\nUPDATE identity.sessions SET revoked_at=clock_timestamp();"
		if err := validateOwnership(crossSchemaWrite); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("cross-schema write slipped through %s/%d: %v", item.Service, item.Version, err)
		}
		otherMigration := item
		otherMigration.Version++
		if err := validateOwnership(otherMigration); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("exception escaped its exact migration %s/%d: %v", item.Service, item.Version, err)
		}
		alteredCall := item
		alteredCall.Up = strings.Replace(alteredCall.Up, "IF NOT identity.privacy_export_session(t,c,s,sid) THEN RETURN; END IF;", "IF NOT identity.privacy_export_session(t,c,s,sid) THEN RETURN; UPDATE identity.sessions SET revoked_at=clock_timestamp(); END IF;", 1)
		if err := validateOwnership(alteredCall); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("modified authority line was allowed in %s/%d: %v", item.Service, item.Version, err)
		}
	}
	if checked != 2 {
		t.Fatalf("reviewed privacy migrations = %d, want 2", checked)
	}
}

func TestPrivacyScrubCapabilitiesDoNotPermitCrossOwnerWrites(t *testing.T) {
	t.Parallel()
	items, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, item := range items {
		if !((item.Service == "supply" && (item.Version == 8 || item.Version == 12 || item.Version == 13)) ||
			(item.Service == "fulfillment" && (item.Version == 11 || item.Version == 12 || item.Version == 13)) ||
			(item.Service == "bankverification" && item.Version == 2) ||
			(item.Service == "notification" && item.Version == 6) ||
			(item.Service == "customer_web" && item.Version == 2) ||
			(item.Service == "social" && item.Version == 5) ||
			(item.Service == "messaging" && item.Version == 5)) {
			continue
		}
		checked++
		if err := validateOwnership(item); err != nil {
			t.Fatalf("approved erasure capability rejected for %s/%d: %v", item.Service, item.Version, err)
		}
		foreignWrite := item
		foreignWrite.Up += "\nDELETE FROM identity.sessions;"
		if err := validateOwnership(foreignWrite); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("cross-owner mutation slipped through %s/%d: %v", item.Service, item.Version, err)
		}
		wrongVersion := item
		wrongVersion.Version++
		if err := validateOwnership(wrongVersion); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("erasure capability escaped its migration %s/%d: %v", item.Service, item.Version, err)
		}
		modifiedCall := item
		modifiedCall.Up = strings.Replace(modifiedCall.Up, "(r,policy,evidence);", "(r,policy,evidence); DELETE FROM identity.sessions;", 1)
		if err := validateOwnership(modifiedCall); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("modified receipt line was allowed in %s/%d: %v", item.Service, item.Version, err)
		}
	}
	if checked != 11 {
		t.Fatalf("reviewed owner scrub migrations = %d, want 11", checked)
	}
}

func TestMediaDuePurgeCapabilityCannotWriteOtherOwners(t *testing.T) {
	t.Parallel()
	items, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Service != "media" || item.Version != 8 || item.Name != "due_version_cleanup" {
			continue
		}
		if err := validateOwnership(item); err != nil {
			t.Fatalf("reviewed due-purge capability rejected: %v", err)
		}
		foreignWrite := item
		foreignWrite.Up += "\nDELETE FROM identity.sessions;"
		if err := validateOwnership(foreignWrite); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("foreign identity write escaped media migration: %v", err)
		}
		wrongVersion := item
		wrongVersion.Version++
		if err := validateOwnership(wrongVersion); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("media capability escaped exact migration: %v", err)
		}
		alteredCall := item
		alteredCall.Up = strings.Replace(alteredCall.Up,
			"OR supply.private_identity_evidence_referenced(asset.tenant_id,asset.country,asset.owner_id,a)",
			"OR supply.private_identity_evidence_referenced(asset.tenant_id,asset.country,asset.owner_id,a); DELETE FROM supply.vendor_applications;", 1)
		if err := validateOwnership(alteredCall); err == nil || !strings.Contains(err.Error(), "outside its owned schema") {
			t.Fatalf("modified supply capability escaped media migration: %v", err)
		}
		return
	}
	t.Fatal("media due-version-cleanup migration missing")
}

func TestDeletionGovernanceAuthorityExceptionIsExact(t *testing.T) {
	t.Parallel()
	items, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Service != "identity" || item.Version != 18 {
			continue
		}
		if err := validateOwnership(item); err != nil {
			t.Fatalf("reviewed admin authority call rejected: %v", err)
		}
		foreignWrite := item
		foreignWrite.Up += "\nUPDATE admin.sessions SET expires_at=clock_timestamp();"
		if err := validateOwnership(foreignWrite); err == nil {
			t.Fatal("foreign admin write escaped migration ownership")
		}
		wrongVersion := item
		wrongVersion.Version++
		if err := validateOwnership(wrongVersion); err == nil {
			t.Fatal("admin authority exception escaped exact migration")
		}
		alteredCall := item
		alteredCall.Up = strings.Replace(alteredCall.Up,
			"IF admin.lock_deletion_governor(t,c,a,s,rev) IS DISTINCT FROM true THEN",
			"IF admin.lock_deletion_governor(t,c,a,s,rev) IS DISTINCT FROM true THEN DELETE FROM admin.sessions;", 1)
		if err := validateOwnership(alteredCall); err == nil {
			t.Fatal("modified authority line escaped migration ownership")
		}
		return
	}
	t.Fatal("deletion governance migration missing")
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
	firstIdentity := -1
	lastOwner := -1
	for index, item := range items {
		if item.Service == "identity" && firstIdentity < 0 {
			firstIdentity = index
		}
		if item.Service == "supply" || item.Service == "fulfillment" || item.Service == "bankverification" || item.Service == "notification" || item.Service == "customer_web" || item.Service == "social" || item.Service == "messaging" || item.Service == "media" {
			lastOwner = index
		}
	}
	if firstIdentity <= lastOwner || firstIdentity >= len(items)-1 {
		t.Fatalf("identity capabilities roll back before their owners: identity=%d last owner=%d", firstIdentity, lastOwner)
	}
}
