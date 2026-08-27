CREATE SCHEMA IF NOT EXISTS audit AUTHORIZATION planext4u_audit_owner;
ALTER SCHEMA audit OWNER TO planext4u_audit_owner;
REVOKE ALL ON SCHEMA audit FROM PUBLIC;
SET ROLE planext4u_audit_owner;

CREATE TABLE audit.events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id uuid NOT NULL UNIQUE,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    actor jsonb NOT NULL,
    action text NOT NULL,
    target jsonb NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('SUCCEEDED', 'DENIED', 'FAILED')),
    reason_code text NOT NULL,
    correlation_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    before_value jsonb,
    after_value jsonb,
    previous_hash char(64),
    hash char(64) NOT NULL CHECK (hash ~ '^[a-f0-9]{64}$')
);
CREATE INDEX audit_events_search_idx ON audit.events (tenant_id, country, recorded_at DESC, sequence DESC);
CREATE INDEX audit_events_action_idx ON audit.events (tenant_id, action, recorded_at DESC);

CREATE FUNCTION audit.reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit events are append-only';
END;
$$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit.events FOR EACH ROW EXECUTE FUNCTION audit.reject_mutation();

GRANT USAGE ON SCHEMA audit TO planext4u_audit_runtime;
GRANT SELECT, INSERT ON audit.events TO planext4u_audit_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA audit TO planext4u_audit_runtime;
RESET ROLE;
