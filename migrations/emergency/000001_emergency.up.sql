CREATE SCHEMA IF NOT EXISTS emergency AUTHORIZATION planext4u_emergency_owner;
ALTER SCHEMA emergency OWNER TO planext4u_emergency_owner;
REVOKE ALL ON SCHEMA emergency FROM PUBLIC;
SET ROLE planext4u_emergency_owner;

CREATE TABLE emergency.requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    requester_identity_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    category text NOT NULL,
    description text NOT NULL,
    priority text NOT NULL CHECK (priority IN ('HIGH', 'CRITICAL')),
    status text NOT NULL CHECK (status IN ('OPEN', 'ASSIGNED', 'EN_ROUTE', 'ON_SCENE', 'RESOLVED')),
    assigned_responder_identity_id uuid,
    location_consent boolean NOT NULL,
    encrypted_location bytea,
    location_captured_at timestamptz,
    escalation_level integer NOT NULL DEFAULT 0,
    sla_deadline timestamptz NOT NULL,
    accepted_at timestamptz,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX emergency_dispatch_queue_idx ON emergency.requests (tenant_id, country, status, priority, sla_deadline);

CREATE TABLE emergency.timeline (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    request_id uuid NOT NULL REFERENCES emergency.requests(id) ON DELETE CASCADE,
    actor_identity_id uuid NOT NULL,
    event_type text NOT NULL,
    detail jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX emergency_timeline_idx ON emergency.timeline (request_id, created_at);

CREATE TABLE emergency.communications (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    request_id uuid NOT NULL REFERENCES emergency.requests(id) ON DELETE CASCADE,
    sender_identity_id uuid NOT NULL,
    body_ciphertext bytea NOT NULL,
    delivery_status text NOT NULL CHECK (delivery_status IN ('DELIVERED', 'FAILED')),
    created_at timestamptz NOT NULL
);
CREATE INDEX emergency_communications_request_idx ON emergency.communications (request_id, created_at);

GRANT USAGE ON SCHEMA emergency TO planext4u_emergency_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA emergency TO planext4u_emergency_runtime;
RESET ROLE;
