SET ROLE planext4u_emergency_owner;

CREATE TABLE emergency.policies (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    version text NOT NULL,
    assignment_sla_seconds integer NOT NULL CHECK (assignment_sla_seconds BETWEEN 30 AND 86400),
    location_max_age_seconds integer NOT NULL CHECK (location_max_age_seconds BETWEEN 15 AND 3600),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country)
);

CREATE TABLE emergency.idempotency_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    subject_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country,subject_id,operation,idempotency_key)
);

GRANT SELECT, INSERT, UPDATE, DELETE ON emergency.policies,emergency.idempotency_records TO planext4u_emergency_runtime;
RESET ROLE;
