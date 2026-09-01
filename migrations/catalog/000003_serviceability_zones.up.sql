SET ROLE planext4u_catalog_owner;

CREATE TABLE catalog.serviceability_zones (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    id text NOT NULL CHECK (id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    locality text NOT NULL CHECK (char_length(locality) BETWEEN 1 AND 160),
    minimum_latitude double precision NOT NULL CHECK (minimum_latitude BETWEEN -90 AND 90),
    maximum_latitude double precision NOT NULL CHECK (maximum_latitude BETWEEN -90 AND 90),
    minimum_longitude double precision NOT NULL CHECK (minimum_longitude BETWEEN -180 AND 180),
    maximum_longitude double precision NOT NULL CHECK (maximum_longitude BETWEEN -180 AND 180),
    postal_codes text[] NOT NULL DEFAULT '{}',
    enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL CHECK (revision >= 1),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, id),
    CHECK (minimum_latitude <= maximum_latitude),
    CHECK (minimum_longitude <= maximum_longitude),
    CHECK (cardinality(postal_codes) <= 1000)
);

CREATE INDEX serviceability_zones_active_idx
    ON catalog.serviceability_zones (tenant_id, country, id)
    WHERE enabled;

GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.serviceability_zones TO planext4u_catalog_runtime;
RESET ROLE;
