SET ROLE planext4u_ordering_owner;

-- One immutable cancellation event per order revision and vendor. A pending
-- payment cancellation creates a tombstone even when no work intent exists.
CREATE TABLE ordering.vendor_work_cancellation_intents (
    id uuid PRIMARY KEY,
    source_work_intent_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    order_id uuid NOT NULL REFERENCES ordering.orders(id) ON DELETE CASCADE,
    vendor_id text NOT NULL CHECK (length(vendor_id) BETWEEN 1 AND 128),
    order_revision bigint NOT NULL CHECK (order_revision > 0),
    order_status text NOT NULL CHECK (order_status IN ('CANCEL_REQUESTED','CANCELLED')),
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','DELIVERED','DEAD_LETTER')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 20),
    next_attempt_at timestamptz NOT NULL,
    claim_token uuid,
    claim_until timestamptz,
    delivered_at timestamptz,
    dead_letter_at timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (order_id, vendor_id, order_revision),
    CHECK ((claim_token IS NULL) = (claim_until IS NULL)),
    CHECK (status <> 'DELIVERED' OR delivered_at IS NOT NULL),
    CHECK (status <> 'DEAD_LETTER' OR dead_letter_at IS NOT NULL)
);
CREATE INDEX ordering_vendor_cancel_due_idx
    ON ordering.vendor_work_cancellation_intents (next_attempt_at,created_at,id)
    WHERE status='PENDING';
CREATE INDEX ordering_vendor_cancel_reconcile_idx
    ON ordering.vendor_work_cancellation_intents (tenant_id,country,order_id,vendor_id,created_at);

GRANT SELECT, INSERT, UPDATE ON ordering.vendor_work_cancellation_intents TO planext4u_ordering_runtime;
RESET ROLE;
