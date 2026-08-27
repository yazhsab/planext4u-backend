CREATE SCHEMA IF NOT EXISTS ordering AUTHORIZATION planext4u_ordering_owner;
ALTER SCHEMA ordering OWNER TO planext4u_ordering_owner;
REVOKE ALL ON SCHEMA ordering FROM PUBLIC;
SET ROLE planext4u_ordering_owner;

CREATE TABLE ordering.orders (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'), customer_identity_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0), status text NOT NULL, checkout_snapshot jsonb NOT NULL CHECK (jsonb_typeof(checkout_snapshot) = 'object'),
    allowed_actions text[] NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE INDEX ordering_customer_history_idx ON ordering.orders (tenant_id, country, customer_identity_id, created_at DESC);
CREATE TABLE ordering.timeline_events (
    id bigserial PRIMARY KEY, order_id uuid NOT NULL REFERENCES ordering.orders(id) ON DELETE CASCADE,
    status text NOT NULL, actor text NOT NULL, reason text, created_at timestamptz NOT NULL
);
CREATE TABLE ordering.delivery_proofs (
    order_id uuid PRIMARY KEY REFERENCES ordering.orders(id) ON DELETE CASCADE, policy_version text NOT NULL,
    photo_asset_id uuid, recipient_name text, otp_verified boolean NOT NULL DEFAULT false, signed_at timestamptz
);
CREATE TABLE ordering.return_cases (
    id uuid PRIMARY KEY, order_id uuid NOT NULL REFERENCES ordering.orders(id), status text NOT NULL, lines jsonb NOT NULL,
    reason text NOT NULL, refund_amount_minor bigint NOT NULL CHECK (refund_amount_minor >= 0), currency char(3) NOT NULL,
    refund_reference text, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE ordering.ratings (
    order_id uuid PRIMARY KEY REFERENCES ordering.orders(id), score integer NOT NULL CHECK (score BETWEEN 1 AND 5),
    comment text, created_at timestamptz NOT NULL
);
CREATE TABLE ordering.idempotency_records (
    tenant_id uuid NOT NULL, country char(2) NOT NULL, customer_identity_id uuid NOT NULL, idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL, response_payload jsonb NOT NULL, created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, customer_identity_id, idempotency_key)
);
GRANT USAGE ON SCHEMA ordering TO planext4u_ordering_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ordering TO planext4u_ordering_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA ordering TO planext4u_ordering_runtime;
RESET ROLE;
