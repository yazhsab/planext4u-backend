CREATE SCHEMA IF NOT EXISTS configuration AUTHORIZATION planext4u_configuration_owner;
ALTER SCHEMA configuration OWNER TO planext4u_configuration_owner;
REVOKE ALL ON SCHEMA configuration FROM PUBLIC;
SET ROLE planext4u_configuration_owner;

CREATE TABLE configuration.snapshots (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    revision bigint NOT NULL CHECK (revision >= 1),
    document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object'),
    published_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country)
);

CREATE TABLE configuration.publish_history (
    event_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    previous_revision bigint NOT NULL,
    new_revision bigint NOT NULL,
    actor_id text NOT NULL,
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 240),
    recorded_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, new_revision)
);

GRANT USAGE ON SCHEMA configuration TO planext4u_configuration_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA configuration TO planext4u_configuration_runtime;
RESET ROLE;
