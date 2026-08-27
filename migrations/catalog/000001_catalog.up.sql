CREATE SCHEMA IF NOT EXISTS catalog AUTHORIZATION planext4u_catalog_owner;
ALTER SCHEMA catalog OWNER TO planext4u_catalog_owner;
REVOKE ALL ON SCHEMA catalog FROM PUBLIC;
SET ROLE planext4u_catalog_owner;

CREATE TABLE catalog.items (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    kind text NOT NULL,
    title jsonb NOT NULL,
    image_asset_id uuid,
    status text NOT NULL CHECK (status IN ('DRAFT', 'PUBLISHED', 'ARCHIVED')),
    priority integer NOT NULL DEFAULT 0,
    version bigint NOT NULL CHECK (version >= 1),
    published_at timestamptz,
    updated_at timestamptz NOT NULL
);
CREATE INDEX catalog_items_projection_idx ON catalog.items (tenant_id, country, kind, status, priority, id);

CREATE TABLE catalog.home_projections (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    locale text NOT NULL,
    revision bigint NOT NULL CHECK (revision >= 1),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    generated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, locale)
);

GRANT USAGE ON SCHEMA catalog TO planext4u_catalog_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA catalog TO planext4u_catalog_runtime;
RESET ROLE;
