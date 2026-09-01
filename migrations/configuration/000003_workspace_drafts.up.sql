SET ROLE planext4u_configuration_owner;
CREATE TABLE configuration.workspace_drafts (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    revision bigint NOT NULL CHECK (revision > 0),
    document jsonb NOT NULL CHECK (jsonb_typeof(document)='object'),
    updated_by text NOT NULL CHECK (length(updated_by) BETWEEN 1 AND 128),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country)
);
GRANT SELECT,INSERT,UPDATE,DELETE ON configuration.workspace_drafts TO planext4u_configuration_runtime;
RESET ROLE;
