SET ROLE planext4u_commerce_owner;

CREATE TABLE commerce.checkout_commands (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    command_type text NOT NULL CHECK (command_type IN ('QUOTE','PLACE','ADDRESS_CREATE','ADDRESS_UPDATE','ADDRESS_DELETE','WALLET_REFILL')),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country,customer_identity_id,command_type,idempotency_key)
);

CREATE TABLE commerce.checkout_processes (
    payment_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    process_type text NOT NULL CHECK (process_type IN ('CHECKOUT','WALLET_REFILL')),
    reservation_id uuid,
    order_id uuid,
    wallet_debit_id uuid,
    refill_offer jsonb,
    created_at timestamptz NOT NULL,
    CHECK (
        (process_type='CHECKOUT' AND reservation_id IS NOT NULL AND order_id IS NOT NULL AND refill_offer IS NULL)
        OR
        (process_type='WALLET_REFILL' AND reservation_id IS NULL AND order_id IS NULL AND refill_offer IS NOT NULL AND jsonb_typeof(refill_offer)='object')
    )
);
CREATE INDEX commerce_checkout_process_owner_idx
    ON commerce.checkout_processes (tenant_id,country,customer_identity_id,created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON commerce.checkout_commands, commerce.checkout_processes TO planext4u_commerce_runtime;
RESET ROLE;
