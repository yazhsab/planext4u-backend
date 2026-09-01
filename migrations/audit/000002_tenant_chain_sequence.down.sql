SET ROLE planext4u_audit_owner;
DROP INDEX IF EXISTS audit.audit_events_tenant_chain_idx;
ALTER TABLE audit.events DROP CONSTRAINT IF EXISTS audit_events_tenant_sequence_unique;
ALTER TABLE audit.events DROP CONSTRAINT IF EXISTS audit_events_tenant_sequence_present;
ALTER TABLE audit.events DROP CONSTRAINT IF EXISTS audit_events_tenant_sequence_positive;
ALTER TABLE audit.events DROP COLUMN IF EXISTS tenant_sequence;
RESET ROLE;
