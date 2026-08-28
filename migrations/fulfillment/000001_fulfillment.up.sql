CREATE SCHEMA IF NOT EXISTS fulfillment AUTHORIZATION planext4u_fulfillment_owner;
ALTER SCHEMA fulfillment OWNER TO planext4u_fulfillment_owner;
REVOKE ALL ON SCHEMA fulfillment FROM PUBLIC;
SET ROLE planext4u_fulfillment_owner;

CREATE TABLE fulfillment.rider_profiles (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    rider_identity_id uuid NOT NULL,
    revision bigint NOT NULL,
    status text NOT NULL,
    full_name text NOT NULL,
    phone_masked text NOT NULL,
    vehicle_type text NOT NULL,
    vehicle_number text NOT NULL,
    bank_reference text NOT NULL,
    bank_status text NOT NULL,
    zones text[] NOT NULL,
    max_concurrent smallint NOT NULL CHECK (max_concurrent BETWEEN 1 AND 10),
    documents jsonb NOT NULL CHECK (jsonb_typeof(documents) = 'array'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, rider_identity_id)
);

CREATE TABLE fulfillment.duty_sessions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    rider_identity_id uuid NOT NULL,
    revision bigint NOT NULL,
    status text NOT NULL,
    zone_id text NOT NULL,
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    last_seen_at timestamptz NOT NULL
);
CREATE INDEX fulfillment_active_duty_idx ON fulfillment.duty_sessions (tenant_id, country, zone_id, status, last_seen_at);

CREATE TABLE fulfillment.delivery_tasks (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    order_id uuid NOT NULL,
    order_type text NOT NULL,
    region_id text NOT NULL,
    territory_id text NOT NULL,
    zone_id text NOT NULL,
    revision bigint NOT NULL,
    status text NOT NULL,
    assigned_rider_identity_id uuid,
    pickup jsonb NOT NULL,
    dropoff jsonb NOT NULL,
    distance_meters integer NOT NULL CHECK (distance_meters > 0),
    earning_minor bigint NOT NULL CHECK (earning_minor > 0),
    currency char(3) NOT NULL,
    offer_expires_at timestamptz,
    accepted_at timestamptz,
    picked_up_at timestamptz,
    delivered_at timestamptz,
    delivery_otp_digest bytea NOT NULL,
    pod_blurred_asset_id uuid,
    pod_signature_asset_id uuid,
    reassignment_count integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL
);
CREATE INDEX fulfillment_offer_idx ON fulfillment.delivery_tasks (tenant_id, country, zone_id, status, offer_expires_at);
CREATE INDEX fulfillment_rider_task_idx ON fulfillment.delivery_tasks (tenant_id, country, assigned_rider_identity_id, status, updated_at);

CREATE TABLE fulfillment.rider_locations (
    rider_identity_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    sequence bigint NOT NULL,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    accuracy_m double precision NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE fulfillment.offline_commands (
    rider_identity_id uuid NOT NULL,
    device_sequence bigint NOT NULL,
    command_id text NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    result jsonb NOT NULL,
    processed_at timestamptz NOT NULL,
    PRIMARY KEY (rider_identity_id, device_sequence),
    UNIQUE (rider_identity_id, command_id)
);

CREATE TABLE fulfillment.conversations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    order_id uuid NOT NULL UNIQUE,
    participant_identity_ids uuid[] NOT NULL,
    expires_at timestamptz NOT NULL,
    blocked boolean NOT NULL DEFAULT false
);

CREATE TABLE fulfillment.chat_messages (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES fulfillment.conversations(id) ON DELETE CASCADE,
    sender_identity_id uuid NOT NULL,
    body text NOT NULL,
    redacted boolean NOT NULL,
    receipts jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL
);

CREATE TABLE fulfillment.ledger_entries (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    account_identity_id uuid NOT NULL,
    reference_id uuid NOT NULL,
    kind text NOT NULL,
    gross_minor bigint NOT NULL,
    commission_minor bigint NOT NULL,
    tax_minor bigint NOT NULL,
    net_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    calculation_version text NOT NULL,
    available_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (account_identity_id, reference_id, kind)
);

CREATE TABLE fulfillment.payouts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    account_identity_id uuid NOT NULL,
    revision bigint NOT NULL,
    amount_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    status text NOT NULL,
    entry_ids uuid[] NOT NULL,
    first_approver_identity_id uuid,
    second_approver_identity_id uuid,
    provider_reference text,
    attempt_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE fulfillment.attendance_entries (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    subject_identity_id uuid NOT NULL,
    session_id uuid NOT NULL,
    kind text NOT NULL,
    latitude double precision,
    longitude double precision,
    source text NOT NULL,
    recorded_at timestamptz NOT NULL
);

CREATE TABLE fulfillment.territories (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    region_id text NOT NULL,
    franchise_identity_id uuid NOT NULL,
    name text NOT NULL,
    center_latitude double precision NOT NULL,
    center_longitude double precision NOT NULL,
    radius_km double precision NOT NULL,
    postal_codes text[] NOT NULL
);

CREATE TABLE fulfillment.audit_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    actor_identity_id uuid NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    reason text,
    created_at timestamptz NOT NULL
);

CREATE TABLE fulfillment.idempotency_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    subject_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, subject_id, operation, idempotency_key)
);

GRANT USAGE ON SCHEMA fulfillment TO planext4u_fulfillment_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA fulfillment TO planext4u_fulfillment_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA fulfillment TO planext4u_fulfillment_runtime;
RESET ROLE;
