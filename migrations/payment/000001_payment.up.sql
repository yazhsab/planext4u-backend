CREATE SCHEMA IF NOT EXISTS payment AUTHORIZATION planext4u_payment_owner;
ALTER SCHEMA payment OWNER TO planext4u_payment_owner;
REVOKE ALL ON SCHEMA payment FROM PUBLIC;
SET ROLE planext4u_payment_owner;

CREATE TABLE payment.payments (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'), customer_identity_id uuid NOT NULL,
    order_reference text NOT NULL, method text NOT NULL CHECK (method IN ('RAZORPAY','PAYSTACK','COD','WALLET')),
    status text NOT NULL, amount_minor bigint NOT NULL CHECK (amount_minor >= 0), currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    refund_amount_minor bigint CHECK (refund_amount_minor > 0), provider_reference text, idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, customer_identity_id, idempotency_key)
);
CREATE UNIQUE INDEX payment_provider_reference_idx ON payment.payments (method, provider_reference) WHERE provider_reference IS NOT NULL;
CREATE TABLE payment.provider_events (
    provider text NOT NULL, event_id text NOT NULL, payment_id uuid NOT NULL REFERENCES payment.payments(id),
    body_digest char(64) NOT NULL, received_at timestamptz NOT NULL, PRIMARY KEY (provider, event_id)
);
CREATE TABLE payment.reconciliation_exceptions (
    id uuid PRIMARY KEY, payment_id uuid NOT NULL REFERENCES payment.payments(id), reason_code text NOT NULL,
    provider_payload jsonb NOT NULL, resolved_at timestamptz, created_at timestamptz NOT NULL
);
GRANT USAGE ON SCHEMA payment TO planext4u_payment_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA payment TO planext4u_payment_runtime;
RESET ROLE;
