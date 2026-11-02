SET ROLE planext4u_ordering_owner;

-- This is an ordering-owned handoff. A vendor worker may replay a claim after
-- a crash, but there is only one placed-order event per order and vendor.
CREATE TABLE ordering.vendor_work_intents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    order_id uuid NOT NULL REFERENCES ordering.orders(id) ON DELETE CASCADE,
    vendor_id text NOT NULL CHECK (length(vendor_id) BETWEEN 1 AND 128),
    order_revision bigint NOT NULL CHECK (order_revision > 0),
    lines jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array' AND jsonb_array_length(lines) > 0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    product_tax_treatment text NOT NULL CHECK (product_tax_treatment IN ('INCLUSIVE', 'EXCLUSIVE')),
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DELIVERED', 'DEAD_LETTER')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 20),
    next_attempt_at timestamptz NOT NULL,
    claim_token uuid,
    claim_until timestamptz,
    delivered_at timestamptz,
    dead_letter_at timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (order_id, vendor_id),
    CHECK ((claim_token IS NULL) = (claim_until IS NULL)),
    CHECK (status <> 'DELIVERED' OR delivered_at IS NOT NULL),
    CHECK (status <> 'DEAD_LETTER' OR dead_letter_at IS NOT NULL)
);
CREATE INDEX ordering_vendor_work_due_idx
    ON ordering.vendor_work_intents (next_attempt_at, created_at, id)
    WHERE status = 'PENDING';
CREATE INDEX ordering_vendor_work_reconcile_idx
    ON ordering.vendor_work_intents (tenant_id, country, status, created_at);

GRANT SELECT, INSERT, UPDATE ON ordering.vendor_work_intents TO planext4u_ordering_runtime;
RESET ROLE;
