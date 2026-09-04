BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SET ROLE planext4u_admin_owner;

CREATE TABLE admin.report_exports (
    id text PRIMARY KEY CHECK (id ~ '^report-export-[a-f0-9]{32}$'),
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    report_id text NOT NULL,
    requested_by uuid NOT NULL,
    operation_change_id text NOT NULL REFERENCES admin.changes(id),
    format text NOT NULL CHECK (format = 'CSV'),
    status text NOT NULL CHECK (status IN ('PROCESSING','READY','FAILED')),
    content_type text NOT NULL,
    file_name text NOT NULL,
    checksum_sha256 char(64) NOT NULL,
    artifact bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX admin_report_exports_scope_idx ON admin.report_exports (tenant_id,country,created_at DESC,id);

GRANT SELECT,INSERT,UPDATE,DELETE ON admin.report_exports TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
