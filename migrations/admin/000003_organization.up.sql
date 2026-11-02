BEGIN;
SET ROLE planext4u_admin_owner;

CREATE TABLE admin.organization_nodes (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    id uuid NOT NULL,
    parent_id uuid,
    kind text NOT NULL CHECK (kind IN ('ORGANIZATION','TERRITORY')),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    active boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    PRIMARY KEY (tenant_id,country,id),
    FOREIGN KEY (tenant_id,country,parent_id) REFERENCES admin.organization_nodes(tenant_id,country,id),
    CHECK (id <> parent_id)
);
CREATE INDEX organization_parent_idx ON admin.organization_nodes(tenant_id,country,parent_id);

CREATE TABLE admin.job_roles (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    capabilities text[] NOT NULL CHECK (cardinality(capabilities) BETWEEN 1 AND 32),
    PRIMARY KEY (tenant_id,country,id)
);

CREATE TABLE admin.staff_assignments (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    subject_id uuid NOT NULL,
    node_id uuid NOT NULL,
    role_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('ACTIVE','SUSPENDED','LEFT')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    PRIMARY KEY (tenant_id,country,subject_id,node_id,role_id),
    FOREIGN KEY (tenant_id,country,node_id) REFERENCES admin.organization_nodes(tenant_id,country,id),
    FOREIGN KEY (tenant_id,country,role_id) REFERENCES admin.job_roles(tenant_id,country,id)
);
CREATE INDEX staff_subject_idx ON admin.staff_assignments(tenant_id,country,subject_id,status);

CREATE TABLE admin.staff_invitations (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    id uuid NOT NULL,
    token_digest char(64) NOT NULL UNIQUE,
    subject_id uuid NOT NULL,
    node_id uuid NOT NULL,
    role_id uuid NOT NULL,
    invited_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    PRIMARY KEY (tenant_id,country,id),
    FOREIGN KEY (tenant_id,country,node_id) REFERENCES admin.organization_nodes(tenant_id,country,id),
    FOREIGN KEY (tenant_id,country,role_id) REFERENCES admin.job_roles(tenant_id,country,id),
    CHECK (subject_id <> invited_by)
);

CREATE TABLE admin.organization_events (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    actor_id uuid NOT NULL,
    action text NOT NULL,
    target_id uuid NOT NULL,
    occurred_at timestamptz NOT NULL
);
CREATE INDEX organization_events_scope_idx ON admin.organization_events(tenant_id,country,sequence_id);

GRANT SELECT,INSERT,UPDATE ON admin.organization_nodes,admin.staff_assignments,admin.staff_invitations TO planext4u_admin_runtime;
GRANT SELECT,INSERT ON admin.job_roles,admin.organization_events TO planext4u_admin_runtime;
GRANT USAGE,SELECT ON SEQUENCE admin.organization_events_sequence_id_seq TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
