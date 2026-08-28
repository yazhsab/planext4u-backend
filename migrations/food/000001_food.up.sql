CREATE SCHEMA IF NOT EXISTS food AUTHORIZATION planext4u_food_owner;
ALTER SCHEMA food OWNER TO planext4u_food_owner;
REVOKE ALL ON SCHEMA food FROM PUBLIC;
SET ROLE planext4u_food_owner;

CREATE TABLE food.restaurants (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id uuid NOT NULL,
    name text NOT NULL,
    cuisine text[] NOT NULL,
    postal_codes text[] NOT NULL,
    rating numeric(3,2) NOT NULL CHECK (rating BETWEEN 0 AND 5),
    verified boolean NOT NULL DEFAULT false,
    open boolean NOT NULL DEFAULT false,
    accept_until_minute smallint NOT NULL CHECK (accept_until_minute BETWEEN 1 AND 1440),
    preparation_minutes smallint NOT NULL CHECK (preparation_minutes > 0),
    delivery_fee_minor bigint NOT NULL CHECK (delivery_fee_minor >= 0),
    minimum_order_minor bigint NOT NULL CHECK (minimum_order_minor >= 0),
    currency char(3) NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE food.menu_items (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL REFERENCES food.restaurants(id) ON DELETE CASCADE,
    name text NOT NULL,
    description text NOT NULL,
    category text NOT NULL,
    vegetarian boolean NOT NULL,
    base_price_minor bigint NOT NULL CHECK (base_price_minor >= 0),
    currency char(3) NOT NULL,
    available boolean NOT NULL,
    image_asset_id uuid
);

CREATE TABLE food.option_groups (
    id uuid PRIMARY KEY,
    menu_item_id uuid NOT NULL REFERENCES food.menu_items(id) ON DELETE CASCADE,
    name text NOT NULL,
    minimum smallint NOT NULL CHECK (minimum >= 0),
    maximum smallint NOT NULL CHECK (maximum >= minimum)
);

CREATE TABLE food.options (
    id uuid PRIMARY KEY,
    option_group_id uuid NOT NULL REFERENCES food.option_groups(id) ON DELETE CASCADE,
    name text NOT NULL,
    price_delta_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    available boolean NOT NULL
);

CREATE TABLE food.carts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    customer_identity_id uuid NOT NULL,
    restaurant_id uuid NOT NULL REFERENCES food.restaurants(id),
    revision bigint NOT NULL,
    postal_code text NOT NULL,
    lines jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array'),
    subtotal_minor bigint NOT NULL,
    delivery_fee_minor bigint NOT NULL,
    tax_minor bigint NOT NULL,
    total_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    pricing_version text NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE food.orders (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    customer_identity_id uuid NOT NULL,
    restaurant_id uuid NOT NULL REFERENCES food.restaurants(id),
    revision bigint NOT NULL,
    status text NOT NULL,
    lines jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array'),
    subtotal_minor bigint NOT NULL,
    delivery_fee_minor bigint NOT NULL,
    tax_minor bigint NOT NULL,
    total_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    pricing_version text NOT NULL,
    payment_reference text NOT NULL,
    payment_status text NOT NULL,
    accept_by timestamptz NOT NULL,
    estimated_ready_at timestamptz,
    rejection_reason text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX food_customer_order_idx ON food.orders (tenant_id, country, customer_identity_id, created_at DESC);
CREATE INDEX food_restaurant_queue_idx ON food.orders (tenant_id, country, restaurant_id, status, updated_at);

CREATE TABLE food.order_timeline (
    id bigserial PRIMARY KEY,
    order_id uuid NOT NULL REFERENCES food.orders(id) ON DELETE CASCADE,
    status text NOT NULL,
    actor_identity_id uuid,
    reason text,
    created_at timestamptz NOT NULL
);

CREATE TABLE food.idempotency_records (
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

GRANT USAGE ON SCHEMA food TO planext4u_food_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA food TO planext4u_food_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA food TO planext4u_food_runtime;
RESET ROLE;
