CREATE SCHEMA IF NOT EXISTS inventory AUTHORIZATION planext4u_inventory_owner;
ALTER SCHEMA inventory OWNER TO planext4u_inventory_owner;
REVOKE ALL ON SCHEMA inventory FROM PUBLIC;
SET ROLE planext4u_inventory_owner;

CREATE TABLE inventory.stock (
    tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'), variant_id uuid NOT NULL,
    available_quantity integer NOT NULL CHECK (available_quantity >= 0), revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL, PRIMARY KEY (tenant_id, country, variant_id)
);
CREATE TABLE inventory.reservations (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    order_reference text NOT NULL, state text NOT NULL CHECK (state IN ('RESERVED','COMMITTED','RELEASED','REJECTED_INSUFFICIENT')),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128), request_fingerprint char(64) NOT NULL,
    created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL CHECK (expires_at > created_at), updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, idempotency_key)
);
CREATE TABLE inventory.reservation_lines (
    reservation_id uuid NOT NULL REFERENCES inventory.reservations(id) ON DELETE CASCADE, variant_id uuid NOT NULL,
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 999), PRIMARY KEY (reservation_id, variant_id)
);
CREATE INDEX inventory_reservation_expiry_idx ON inventory.reservations (state, expires_at);
GRANT USAGE ON SCHEMA inventory TO planext4u_inventory_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA inventory TO planext4u_inventory_runtime;
RESET ROLE;
