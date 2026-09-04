package identity

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestIdentityMigrationStoresOnlyRefreshDigestsAndRequiredConstraints(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../migrations/identity/000001_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(contents)
	for _, required := range []string{
		"digest char(64) PRIMARY KEY",
		"refresh_tokens_one_active_per_session",
		"WHERE status = 'ACTIVE'",
		"identities_provider_identity_unique",
		"consent_evidence_identity_purpose_version_unique",
		"GENERATED ALWAYS AS IDENTITY",
	} {
		if !strings.Contains(schema, required) {
			t.Errorf("identity migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"refresh_token text",
		"provider_token",
		"access_token",
		"password_hash",
	} {
		if strings.Contains(strings.ToLower(schema), forbidden) {
			t.Errorf("identity migration contains prohibited secret column %q", forbidden)
		}
	}
}

func TestIdentityLocaleMigrationAllowsTheApprovedNine(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../migrations/identity/000003_profile_locales.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(contents)
	for _, locale := range []string{"'en'", "'ta'", "'hi'", "'te'", "'kn'", "'ml'", "'mr'", "'bn'", "'gu'"} {
		if !strings.Contains(schema, locale) {
			t.Errorf("identity locale migration is missing %s", locale)
		}
	}
}

func TestPersistenceAndHandlerRejectMissingDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewPostgresRepository(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil PostgreSQL pool error = %v", err)
	}
	if _, err := NewHandler(nil, nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil identity service error = %v", err)
	}
}
