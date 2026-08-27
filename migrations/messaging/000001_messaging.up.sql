CREATE SCHEMA IF NOT EXISTS messaging AUTHORIZATION planext4u_messaging_owner;
ALTER SCHEMA messaging OWNER TO planext4u_messaging_owner;
REVOKE ALL ON SCHEMA messaging FROM PUBLIC;
SET ROLE planext4u_messaging_owner;

CREATE TABLE messaging.outbox (
    event_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version >= 1),
    envelope jsonb NOT NULL CHECK (jsonb_typeof(envelope) = 'object'),
    state text NOT NULL CHECK (state IN ('PENDING', 'LEASED', 'PUBLISHED', 'DEAD_LETTER')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    lease_owner text,
    lease_until timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL,
    published_at timestamptz,
    dead_at timestamptz
);
CREATE INDEX messaging_outbox_claim_idx ON messaging.outbox (state, next_attempt_at, event_id);
CREATE INDEX messaging_outbox_tenant_dead_idx ON messaging.outbox (tenant_id, dead_at DESC) WHERE state = 'DEAD_LETTER';

CREATE TABLE messaging.inbox (
    consumer text NOT NULL,
    event_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    state text NOT NULL CHECK (state IN ('PROCESSING', 'DONE')),
    lease_until timestamptz,
    processed_at timestamptz,
    PRIMARY KEY (consumer, event_id),
    UNIQUE (consumer, tenant_id, aggregate_type, aggregate_id, aggregate_version)
);

GRANT USAGE ON SCHEMA messaging TO planext4u_messaging_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA messaging TO planext4u_messaging_runtime;
RESET ROLE;
