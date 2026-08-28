CREATE SCHEMA IF NOT EXISTS supply AUTHORIZATION planext4u_supply_owner;
ALTER SCHEMA supply OWNER TO planext4u_supply_owner;
REVOKE ALL ON SCHEMA supply FROM PUBLIC;
SET ROLE planext4u_supply_owner;

CREATE TABLE supply.vendor_applications (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    vendor_identity_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    status text NOT NULL,
    business_name text NOT NULL,
    business_type text NOT NULL,
    contact_name text NOT NULL,
    bank_reference text,
    bank_last4 char(4),
    bank_status text,
    verified boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, vendor_identity_id)
);
CREATE INDEX supply_application_review_idx ON supply.vendor_applications (tenant_id, country, status, updated_at);

CREATE TABLE supply.vendor_documents (
    id uuid PRIMARY KEY,
    application_id uuid NOT NULL REFERENCES supply.vendor_applications(id) ON DELETE CASCADE,
    kind text NOT NULL,
    private_asset_id uuid NOT NULL,
    ocr_status text NOT NULL,
    extracted_fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    review_reason text,
    created_at timestamptz NOT NULL
);

CREATE TABLE supply.field_visits (
    id uuid PRIMARY KEY,
    application_id uuid NOT NULL REFERENCES supply.vendor_applications(id),
    officer_identity_id uuid,
    scheduled_at timestamptz NOT NULL,
    latitude double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    allowed_radius_m double precision NOT NULL CHECK (allowed_radius_m BETWEEN 20 AND 2000),
    checked_in_at timestamptz,
    check_in_distance_m double precision
);

CREATE TABLE supply.service_zones (
    id uuid PRIMARY KEY,
    vendor_identity_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    postal_codes text[] NOT NULL,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    radius_km double precision NOT NULL CHECK (radius_km > 0),
    policy_version text NOT NULL
);

CREATE TABLE supply.catalog_items (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    vendor_identity_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    kind text NOT NULL CHECK (kind IN ('PRODUCT', 'SERVICE', 'FOOD')),
    name text NOT NULL,
    description text NOT NULL,
    sku text NOT NULL,
    price_minor bigint NOT NULL CHECK (price_minor >= 0),
    currency char(3) NOT NULL,
    stock integer NOT NULL DEFAULT 0 CHECK (stock >= 0),
    approval_status text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, vendor_identity_id, sku)
);
CREATE INDEX supply_catalog_owner_idx ON supply.catalog_items (tenant_id, country, vendor_identity_id, updated_at DESC);

CREATE TABLE supply.schedule_windows (
    id bigserial PRIMARY KEY,
    catalog_item_id uuid NOT NULL REFERENCES supply.catalog_items(id) ON DELETE CASCADE,
    weekday smallint NOT NULL CHECK (weekday BETWEEN 1 AND 7),
    starts_minute smallint NOT NULL CHECK (starts_minute BETWEEN 0 AND 1439),
    ends_minute smallint NOT NULL CHECK (ends_minute BETWEEN 1 AND 1440),
    timezone text NOT NULL,
    capacity integer NOT NULL CHECK (capacity > 0),
    buffer_minutes integer NOT NULL CHECK (buffer_minutes >= 0),
    CHECK (ends_minute > starts_minute)
);

CREATE TABLE supply.work_items (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    vendor_identity_id uuid NOT NULL,
    reference_type text NOT NULL,
    reference_id uuid NOT NULL,
    status text NOT NULL,
    total_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    customer_label text NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX supply_work_queue_idx ON supply.work_items (tenant_id, country, vendor_identity_id, status, updated_at);

CREATE TABLE supply.promotions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    vendor_identity_id uuid NOT NULL,
    title text NOT NULL,
    kind text NOT NULL,
    budget_minor bigint NOT NULL CHECK (budget_minor >= 0),
    currency char(3) NOT NULL,
    status text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL CHECK (ends_at > starts_at)
);

CREATE TABLE supply.idempotency_records (
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

GRANT USAGE ON SCHEMA supply TO planext4u_supply_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA supply TO planext4u_supply_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA supply TO planext4u_supply_runtime;
RESET ROLE;
