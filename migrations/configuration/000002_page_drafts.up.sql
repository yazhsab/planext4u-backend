SET ROLE planext4u_configuration_owner;

CREATE TABLE configuration.page_drafts (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    page_id text NOT NULL CHECK (page_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    revision bigint NOT NULL CHECK (revision >= 1),
    document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object'),
    updated_by text NOT NULL CHECK (length(updated_by) BETWEEN 1 AND 128),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, page_id)
);

CREATE INDEX page_drafts_updated_at_idx
    ON configuration.page_drafts (tenant_id, country, updated_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON configuration.page_drafts TO planext4u_configuration_runtime;
RESET ROLE;
