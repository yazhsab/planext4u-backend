SET ROLE planext4u_local_verticals_owner;

CREATE TABLE local_verticals.policies (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    version text NOT NULL,
    currency char(3) NOT NULL,
    estimator_version text NOT NULL,
    review_terms text[] NOT NULL DEFAULT '{}',
    classified_lifetime_seconds integer NOT NULL CHECK (classified_lifetime_seconds BETWEEN 86400 AND 31536000),
    feature_lifetime_seconds integer NOT NULL CHECK (feature_lifetime_seconds BETWEEN 3600 AND 2592000),
    report_review_threshold integer NOT NULL CHECK (report_review_threshold BETWEEN 1 AND 100),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country)
);

CREATE TABLE local_verticals.owner_verifications (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('VERIFIED','REVOKED')),
    evidence_reference text NOT NULL,
    verified_by_identity_id uuid NOT NULL,
    verified_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country,owner_identity_id)
);

CREATE TABLE local_verticals.idempotency_records (
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

GRANT SELECT, INSERT, UPDATE, DELETE ON local_verticals.policies,local_verticals.owner_verifications,local_verticals.idempotency_records TO planext4u_local_verticals_runtime;
RESET ROLE;
