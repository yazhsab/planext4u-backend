CREATE SCHEMA IF NOT EXISTS customer_web AUTHORIZATION planext4u_customer_web_owner;
ALTER SCHEMA customer_web OWNER TO planext4u_customer_web_owner;
REVOKE ALL ON SCHEMA customer_web FROM PUBLIC;
SET ROLE planext4u_customer_web_owner;

CREATE TABLE customer_web.sessions (
    token_digest char(64) PRIMARY KEY,
    session_id uuid NOT NULL UNIQUE,
    platform_session_id text NOT NULL CHECK (char_length(platform_session_id) BETWEEN 1 AND 128),
    identity_id text NOT NULL DEFAULT '' CHECK (char_length(identity_id) <= 128),
    tenant_id text NOT NULL CHECK (char_length(tenant_id) BETWEEN 1 AND 128),
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 128),
    roles text[] NOT NULL CHECK (cardinality(roles) BETWEEN 1 AND 8),
    is_guest boolean NOT NULL,
    access_token bytea NOT NULL CHECK (octet_length(access_token) BETWEEN 32 AND 20000),
    access_expires_at timestamptz NOT NULL,
    refresh_token bytea NOT NULL DEFAULT ''::bytea CHECK (octet_length(refresh_token) <= 512),
    refresh_expires_at timestamptz,
    csrf_token text NOT NULL CHECK (char_length(csrf_token) BETWEEN 32 AND 256),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    refresh_lease_until timestamptz,
    CHECK (expires_at >= updated_at),
    CHECK (access_expires_at >= updated_at),
    CHECK ((is_guest AND identity_id = '' AND octet_length(refresh_token) = 0 AND refresh_expires_at IS NULL AND roles = ARRAY['GUEST']::text[])
        OR (NOT is_guest AND identity_id <> '' AND octet_length(refresh_token) > 0 AND refresh_expires_at IS NOT NULL AND NOT ('GUEST' = ANY(roles))))
);
CREATE INDEX customer_web_sessions_expiry_idx ON customer_web.sessions (expires_at);

GRANT USAGE ON SCHEMA customer_web TO planext4u_customer_web_runtime;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA customer_web TO planext4u_customer_web_runtime;
RESET ROLE;
