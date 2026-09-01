CREATE SCHEMA IF NOT EXISTS admin AUTHORIZATION planext4u_admin_owner;
ALTER SCHEMA admin OWNER TO planext4u_admin_owner;
REVOKE ALL ON SCHEMA admin FROM PUBLIC;
SET ROLE planext4u_admin_owner;

CREATE TABLE admin.sessions (
    token_digest char(64) PRIMARY KEY,
    session_id uuid NOT NULL UNIQUE,
    subject_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 128),
    roles text[] NOT NULL CHECK (cardinality(roles) BETWEEN 1 AND 8),
    allowed_countries char(2)[] NOT NULL CHECK (cardinality(allowed_countries) BETWEEN 1 AND 32),
    selected_country char(2) NOT NULL CHECK (selected_country ~ '^[A-Z]{2}$'),
    authenticated_at timestamptz NOT NULL,
    auth_methods text[] NOT NULL,
    csrf_token text NOT NULL CHECK (length(csrf_token) BETWEEN 32 AND 256),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (selected_country = ANY(allowed_countries))
);
CREATE INDEX admin_sessions_session_idx ON admin.sessions (session_id,expires_at);
CREATE INDEX admin_sessions_expiry_idx ON admin.sessions (expires_at);

CREATE TABLE admin.changes (
    id text PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
    revision bigint NOT NULL CHECK (revision > 0),
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    command jsonb NOT NULL CHECK (jsonb_typeof(command)='object'),
    risk text NOT NULL CHECK (risk IN ('STANDARD','HIGH')),
    status text NOT NULL CHECK (status IN ('PENDING_APPROVAL','EXECUTED','REJECTED')),
    requested_by uuid NOT NULL,
    approved_by uuid,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX admin_changes_scope_idx ON admin.changes (tenant_id,country,created_at DESC,id);

CREATE TABLE admin.operation_events (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    actor_id uuid NOT NULL,
    action text NOT NULL,
    target_id text NOT NULL,
    outcome text NOT NULL,
    reason text NOT NULL,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX admin_operation_events_scope_idx ON admin.operation_events (tenant_id,country,sequence_id);

CREATE TABLE admin.domain_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    domain text NOT NULL,
    target_id text NOT NULL,
    state text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
    last_change text NOT NULL,
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country,domain,target_id)
);

CREATE TABLE admin.domain_executions (
    change_id text PRIMARY KEY,
    request_fingerprint char(64) NOT NULL,
    executed_at timestamptz NOT NULL
);

GRANT USAGE ON SCHEMA admin TO planext4u_admin_runtime;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA admin TO planext4u_admin_runtime;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA admin TO planext4u_admin_runtime;
RESET ROLE;
