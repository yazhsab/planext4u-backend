CREATE SCHEMA IF NOT EXISTS commerce AUTHORIZATION planext4u_commerce_owner;
ALTER SCHEMA commerce OWNER TO planext4u_commerce_owner;
REVOKE ALL ON SCHEMA commerce FROM PUBLIC;
SET ROLE planext4u_commerce_owner;

CREATE TABLE commerce.carts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    subtotal_minor bigint NOT NULL DEFAULT 0 CHECK (subtotal_minor >= 0),
    discount_minor bigint NOT NULL DEFAULT 0 CHECK (discount_minor >= 0),
    tax_minor bigint NOT NULL DEFAULT 0 CHECK (tax_minor >= 0),
    fees_minor bigint NOT NULL DEFAULT 0 CHECK (fees_minor >= 0),
    total_minor bigint NOT NULL DEFAULT 0 CHECK (total_minor >= 0),
    pricing_status text NOT NULL CHECK (pricing_status IN ('CURRENT', 'REPRICED')),
    allowed_actions text[] NOT NULL DEFAULT ARRAY['BROWSE']::text[],
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, customer_identity_id)
);

CREATE TABLE commerce.cart_items (
    cart_id uuid NOT NULL REFERENCES commerce.carts(id) ON DELETE CASCADE,
    variant_id uuid NOT NULL,
    item_id uuid NOT NULL,
    item_name text NOT NULL,
    variant_name text NOT NULL,
    media_asset_id uuid,
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 999),
    unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
    line_total_minor bigint NOT NULL CHECK (line_total_minor >= 0),
    available boolean NOT NULL,
    price_changed boolean NOT NULL DEFAULT false,
    PRIMARY KEY (cart_id, variant_id)
);

CREATE TABLE commerce.idempotency_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    PRIMARY KEY (tenant_id, country, customer_identity_id, idempotency_key)
);
CREATE INDEX commerce_idempotency_expiry_idx ON commerce.idempotency_records (expires_at);

GRANT USAGE ON SCHEMA commerce TO planext4u_commerce_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA commerce TO planext4u_commerce_runtime;
RESET ROLE;
