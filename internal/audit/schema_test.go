package audit

import (
	"os"
	"strings"
	"testing"
)

func TestAuditTenantSequenceMigrationPreservesPerTenantChainInvariants(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../migrations/audit/000002_tenant_chain_sequence.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"row_number() OVER (PARTITION BY tenant_id ORDER BY sequence)",
		"CHECK (tenant_sequence IS NOT NULL) NOT VALID",
		"VALIDATE CONSTRAINT audit_events_tenant_sequence_present",
		"UNIQUE (tenant_id, tenant_sequence)",
		"audit_events_tenant_chain_idx",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("audit migration is missing %q", required)
		}
	}
}
