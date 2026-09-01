SET ROLE planext4u_wallet_owner;

ALTER TABLE wallet.ledger_entries
    ADD COLUMN sequence_id bigint GENERATED ALWAYS AS IDENTITY;
CREATE UNIQUE INDEX wallet_ledger_sequence_idx ON wallet.ledger_entries (sequence_id);

CREATE TABLE wallet.debit_allocations (
    debit_entry_id uuid NOT NULL REFERENCES wallet.ledger_entries(id) ON DELETE CASCADE,
    credit_entry_id uuid NOT NULL REFERENCES wallet.credit_lots(ledger_entry_id),
    allocated_points bigint NOT NULL CHECK (allocated_points > 0),
    refunded_points bigint NOT NULL DEFAULT 0 CHECK (refunded_points >= 0 AND refunded_points <= allocated_points),
    PRIMARY KEY (debit_entry_id,credit_entry_id)
);

CREATE TABLE wallet.referral_codes (
    code text PRIMARY KEY CHECK (length(code) BETWEEN 8 AND 32),
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id,country,customer_identity_id)
);

CREATE TABLE wallet.referral_applications (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    referral_code text NOT NULL REFERENCES wallet.referral_codes(code),
    activated boolean NOT NULL DEFAULT false,
    purchase_reference text,
    applied_at timestamptz NOT NULL,
    activated_at timestamptz,
    PRIMARY KEY (tenant_id,country,customer_identity_id)
);

CREATE TABLE wallet.referral_requests (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL CHECK (jsonb_typeof(response_payload) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country,customer_identity_id,idempotency_key)
);

CREATE INDEX wallet_reward_device_time_idx
    ON wallet.reward_claims (tenant_id,country,device_reference_hash,created_at)
    WHERE device_reference_hash IS NOT NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON wallet.debit_allocations, wallet.referral_codes,
    wallet.referral_applications, wallet.referral_requests TO planext4u_wallet_runtime;
RESET ROLE;
