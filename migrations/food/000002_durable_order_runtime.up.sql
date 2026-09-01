SET ROLE planext4u_food_owner;

ALTER TABLE food.option_groups ADD COLUMN instructions text NOT NULL DEFAULT '';

CREATE TABLE food.policies (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    version text NOT NULL,
    cart_ttl_seconds integer NOT NULL CHECK (cart_ttl_seconds BETWEEN 60 AND 86400),
    acceptance_ttl_seconds integer NOT NULL CHECK (acceptance_ttl_seconds BETWEEN 30 AND 3600),
    tax_basis_points integer NOT NULL CHECK (tax_basis_points BETWEEN 0 AND 10000),
    wallet_point_value_minor bigint NOT NULL CHECK (wallet_point_value_minor > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country)
);

ALTER TABLE food.orders
    ADD COLUMN source_cart_id uuid NOT NULL REFERENCES food.carts(id),
    ADD COLUMN postal_code text NOT NULL,
    ADD COLUMN payment_method text NOT NULL,
    ADD COLUMN refund_state text,
    ADD COLUMN wallet_debit_entry_id uuid;
CREATE UNIQUE INDEX food_order_source_cart_idx ON food.orders (source_cart_id);
CREATE INDEX food_pending_acceptance_idx ON food.orders (accept_by) WHERE status='PENDING_RESTAURANT';

GRANT SELECT, INSERT, UPDATE, DELETE ON food.policies TO planext4u_food_runtime;
RESET ROLE;
