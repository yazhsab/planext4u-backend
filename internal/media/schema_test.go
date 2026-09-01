package media

import (
	"os"
	"strings"
	"testing"
)

func TestMediaLifecycleMigrationExpandsTheV1SchemaSafely(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../migrations/media/000002_media_lifecycle.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"ADD COLUMN purpose",
		"ADD COLUMN lifecycle_state",
		"ADD COLUMN upload_expires_at",
		"CHECK (purpose IS NOT NULL AND lifecycle_state IS NOT NULL AND upload_expires_at IS NOT NULL AND version IS NOT NULL) NOT VALID",
		"VALIDATE CONSTRAINT media_assets_lifecycle_fields_present",
		"media_assets_expiry_idx",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("media lifecycle migration is missing %q", required)
		}
	}
}
