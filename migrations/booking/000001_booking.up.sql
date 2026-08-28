CREATE SCHEMA IF NOT EXISTS booking AUTHORIZATION planext4u_booking_owner;
ALTER SCHEMA booking OWNER TO planext4u_booking_owner;
REVOKE ALL ON SCHEMA booking FROM PUBLIC;
SET ROLE planext4u_booking_owner;

CREATE TABLE booking.offerings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    provider_id uuid NOT NULL,
    category_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 2 AND 160),
    summary text NOT NULL CHECK (char_length(summary) BETWEEN 2 AND 1000),
    duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 15 AND 1440),
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    advance_minor bigint NOT NULL CHECK (advance_minor > 0 AND advance_minor <= price_minor),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_mode text NOT NULL CHECK (payment_mode IN ('FULL', 'ADVANCE')),
    cancellation_policy_ref text NOT NULL,
    reschedule_policy_ref text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX booking_offering_discovery_idx ON booking.offerings (tenant_id, country, category_id, active, updated_at DESC);

CREATE TABLE booking.service_zones (
    offering_id uuid NOT NULL REFERENCES booking.offerings(id) ON DELETE CASCADE,
    postal_code text NOT NULL CHECK (char_length(postal_code) BETWEEN 3 AND 12),
    PRIMARY KEY (offering_id, postal_code)
);

CREATE TABLE booking.slots (
    id uuid PRIMARY KEY,
    offering_id uuid NOT NULL REFERENCES booking.offerings(id),
    provider_id uuid NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL CHECK (ends_at > starts_at),
    timezone text NOT NULL CHECK (char_length(timezone) BETWEEN 3 AND 64),
    capacity integer NOT NULL CHECK (capacity BETWEEN 1 AND 100),
    buffer_minutes integer NOT NULL CHECK (buffer_minutes BETWEEN 0 AND 240),
    policy_version text NOT NULL,
    provider_revision bigint NOT NULL CHECK (provider_revision > 0),
    UNIQUE (provider_id, starts_at, ends_at)
);
CREATE INDEX booking_slot_availability_idx ON booking.slots (offering_id, starts_at, ends_at);

CREATE TABLE booking.slot_holds (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    slot_id uuid NOT NULL REFERENCES booking.slots(id),
    status text NOT NULL CHECK (status IN ('HELD', 'CONSUMED', 'EXPIRED', 'RELEASED')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX booking_active_hold_idx ON booking.slot_holds (slot_id, expires_at) WHERE status = 'HELD';

CREATE TABLE booking.bookings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    offering_id uuid NOT NULL REFERENCES booking.offerings(id),
    slot_id uuid NOT NULL REFERENCES booking.slots(id),
    revision bigint NOT NULL CHECK (revision > 0),
    status text NOT NULL,
    offering_snapshot jsonb NOT NULL CHECK (jsonb_typeof(offering_snapshot) = 'object'),
    slot_snapshot jsonb NOT NULL CHECK (jsonb_typeof(slot_snapshot) = 'object'),
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    amount_due_minor bigint NOT NULL CHECK (amount_due_minor > 0 AND amount_due_minor <= price_minor),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_id uuid NOT NULL,
    wallet_debit_entry_id uuid,
    reschedule_count integer NOT NULL DEFAULT 0 CHECK (reschedule_count >= 0),
    start_otp_digest bytea,
    start_otp_expires_at timestamptz,
    allowed_actions text[] NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX booking_customer_history_idx ON booking.bookings (tenant_id, country, customer_identity_id, created_at DESC);
CREATE INDEX booking_provider_queue_idx ON booking.bookings (tenant_id, country, provider_id, status, updated_at);

CREATE TABLE booking.timeline_events (
    id bigserial PRIMARY KEY,
    booking_id uuid NOT NULL REFERENCES booking.bookings(id) ON DELETE CASCADE,
    status text NOT NULL,
    actor_type text NOT NULL,
    actor_subject_id uuid,
    reason text,
    created_at timestamptz NOT NULL
);

CREATE TABLE booking.completion_evidence (
    booking_id uuid PRIMARY KEY REFERENCES booking.bookings(id),
    photo_asset_id uuid NOT NULL,
    submitted_by uuid NOT NULL,
    captured_at timestamptz NOT NULL
);

CREATE TABLE booking.disputes (
    id uuid PRIMARY KEY,
    booking_id uuid NOT NULL REFERENCES booking.bookings(id),
    opened_by uuid NOT NULL,
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 3 AND 500),
    status text NOT NULL CHECK (status IN ('OPEN', 'UNDER_REVIEW', 'RESOLVED', 'REJECTED')),
    resolution text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE booking.idempotency_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    subject_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, subject_id, idempotency_key)
);

GRANT USAGE ON SCHEMA booking TO planext4u_booking_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA booking TO planext4u_booking_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA booking TO planext4u_booking_runtime;
RESET ROLE;
