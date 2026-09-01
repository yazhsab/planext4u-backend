SET ROLE planext4u_payment_owner;

ALTER TABLE payment.payments
    ADD COLUMN payer jsonb,
    ADD COLUMN operation_state text NOT NULL DEFAULT 'READY'
        CHECK (operation_state IN ('READY','INITIALIZING','REFUNDING'));

ALTER TABLE payment.provider_events
    ADD COLUMN response_payload jsonb,
    ADD CONSTRAINT payment_provider_event_response_object CHECK (
        response_payload IS NULL OR jsonb_typeof(response_payload) = 'object'
    );

CREATE TABLE payment.command_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    request_fingerprint char(64) NOT NULL,
    payment_id uuid NOT NULL REFERENCES payment.payments(id) ON DELETE CASCADE,
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country,customer_identity_id,idempotency_key)
);

GRANT SELECT, INSERT, UPDATE, DELETE ON payment.command_records TO planext4u_payment_runtime;
RESET ROLE;
