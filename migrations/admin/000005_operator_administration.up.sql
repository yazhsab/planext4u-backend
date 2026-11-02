BEGIN;
SET ROLE planext4u_admin_owner;
ALTER TABLE admin.sessions ADD COLUMN operator_revision bigint NOT NULL DEFAULT 0 CHECK (operator_revision >= 0);
CREATE TABLE admin.operator_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    action text NOT NULL CHECK (action IN ('CREATED','UPDATED')),
    revision bigint NOT NULL CHECK (revision > 0),
    roles text[] NOT NULL,
    allowed_countries text[] NOT NULL,
    active boolean NOT NULL,
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 8 AND 512),
    occurred_at timestamptz NOT NULL,
    UNIQUE (tenant_id,subject_id,revision)
);
CREATE INDEX operator_events_scope_idx ON admin.operator_events(tenant_id,subject_id,revision DESC);
GRANT INSERT,UPDATE ON admin.operator_accounts TO planext4u_admin_runtime;
-- Runtime audit evidence is append-only, including for access changes.
GRANT SELECT,INSERT ON admin.operator_events TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
