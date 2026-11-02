BEGIN;
SET ROLE planext4u_admin_owner;
-- Provisioned by the server/deployment operator, never by identity assertions.
CREATE TABLE admin.operator_accounts (
    tenant_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    provider_subject text NOT NULL CHECK (length(provider_subject) BETWEEN 1 AND 128),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 128),
    roles text[] NOT NULL CHECK (cardinality(roles) BETWEEN 1 AND 8 AND roles <@ ARRAY['SUPER_ADMIN','COUNTRY_ADMIN','CONTENT_ADMIN','SUPPORT_ADMIN','AUDITOR','REGIONAL_STAFF']::text[]),
    allowed_countries text[] NOT NULL CHECK (cardinality(allowed_countries) BETWEEN 1 AND 32),
    active boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    PRIMARY KEY(tenant_id,subject_id),
    UNIQUE(tenant_id,provider_subject)
);
GRANT SELECT ON admin.operator_accounts TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
