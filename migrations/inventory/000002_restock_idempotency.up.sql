SET ROLE planext4u_inventory_owner;

ALTER TABLE inventory.reservation_lines
    ADD COLUMN restocked_quantity integer NOT NULL DEFAULT 0
        CHECK (restocked_quantity >= 0 AND restocked_quantity <= quantity);

ALTER TABLE inventory.reservations
    ADD COLUMN response_payload jsonb,
    ADD CONSTRAINT inventory_reservation_response_object CHECK (
        response_payload IS NULL OR jsonb_typeof(response_payload) = 'object'
    );

CREATE TABLE inventory.restock_requests (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    reservation_id uuid NOT NULL REFERENCES inventory.reservations(id) ON DELETE CASCADE,
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, idempotency_key)
);

GRANT SELECT, INSERT, UPDATE, DELETE ON inventory.restock_requests TO planext4u_inventory_runtime;
RESET ROLE;
