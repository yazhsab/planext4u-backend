SET ROLE planext4u_ordering_owner;

ALTER TABLE ordering.orders
    ADD COLUMN state_snapshot jsonb,
    ADD CONSTRAINT ordering_state_snapshot_object CHECK (
        state_snapshot IS NULL OR jsonb_typeof(state_snapshot) = 'object'
    );

ALTER TABLE ordering.idempotency_records
    ADD CONSTRAINT ordering_idempotency_key_length CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    ADD CONSTRAINT ordering_idempotency_response_object CHECK (jsonb_typeof(response_payload) = 'object');

CREATE TABLE ordering.notification_intents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    order_id uuid NOT NULL REFERENCES ordering.orders(id) ON DELETE CASCADE,
    status text NOT NULL,
    order_revision bigint NOT NULL CHECK (order_revision > 0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    claim_until timestamptz,
    delivered_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL,
    UNIQUE (order_id, order_revision)
);
CREATE INDEX ordering_notification_due_idx
    ON ordering.notification_intents (next_attempt_at, created_at)
    WHERE delivered_at IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON ordering.notification_intents TO planext4u_ordering_runtime;
RESET ROLE;
