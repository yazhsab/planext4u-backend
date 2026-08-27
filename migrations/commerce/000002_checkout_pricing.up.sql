SET ROLE planext4u_commerce_owner;

ALTER TABLE commerce.cart_items ADD COLUMN IF NOT EXISTS vendor_id uuid;

CREATE TABLE commerce.customer_addresses (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL, label text NOT NULL, postal_code text NOT NULL, locality text NOT NULL,
    active boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE INDEX commerce_customer_addresses_owner_idx ON commerce.customer_addresses (tenant_id, country, customer_identity_id, active);

CREATE TABLE commerce.delivery_slots (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    window_start timestamptz NOT NULL, window_end timestamptz NOT NULL CHECK (window_end > window_start),
    fee_minor bigint NOT NULL CHECK (fee_minor >= 0), currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    capacity integer NOT NULL CHECK (capacity >= 0), revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);
CREATE INDEX commerce_delivery_slots_country_time_idx ON commerce.delivery_slots (tenant_id, country, window_start);

CREATE TABLE commerce.promotions (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    code text NOT NULL, minimum_subtotal_minor bigint NOT NULL CHECK (minimum_subtotal_minor >= 0),
    discount_basis_points integer NOT NULL CHECK (discount_basis_points BETWEEN 0 AND 10000),
    maximum_discount_minor bigint NOT NULL CHECK (maximum_discount_minor >= 0), starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL CHECK (ends_at > starts_at), active boolean NOT NULL DEFAULT true,
    UNIQUE (tenant_id, country, code)
);

CREATE TABLE commerce.checkout_quotes (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL, cart_id uuid NOT NULL, cart_revision bigint NOT NULL CHECK (cart_revision >= 0),
    pricing_policy_version text NOT NULL, request_fingerprint char(64) NOT NULL, snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL CHECK (expires_at > created_at)
);
CREATE INDEX commerce_checkout_quotes_owner_idx ON commerce.checkout_quotes (tenant_id, country, customer_identity_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON commerce.customer_addresses, commerce.delivery_slots, commerce.promotions, commerce.checkout_quotes TO planext4u_commerce_runtime;
RESET ROLE;
