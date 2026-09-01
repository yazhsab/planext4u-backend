SET ROLE planext4u_audit_owner;

ALTER TABLE audit.events ADD COLUMN tenant_sequence bigint;

WITH ranked AS (
    SELECT sequence, row_number() OVER (PARTITION BY tenant_id ORDER BY sequence) AS tenant_sequence
    FROM audit.events
)
UPDATE audit.events AS event
SET tenant_sequence = ranked.tenant_sequence
FROM ranked
WHERE event.sequence = ranked.sequence;

ALTER TABLE audit.events ADD CONSTRAINT audit_events_tenant_sequence_positive CHECK (tenant_sequence >= 1);
ALTER TABLE audit.events ADD CONSTRAINT audit_events_tenant_sequence_present
    CHECK (tenant_sequence IS NOT NULL) NOT VALID;
ALTER TABLE audit.events VALIDATE CONSTRAINT audit_events_tenant_sequence_present;
ALTER TABLE audit.events ADD CONSTRAINT audit_events_tenant_sequence_unique UNIQUE (tenant_id, tenant_sequence);

CREATE INDEX audit_events_tenant_chain_idx
    ON audit.events (tenant_id, tenant_sequence DESC);

RESET ROLE;
