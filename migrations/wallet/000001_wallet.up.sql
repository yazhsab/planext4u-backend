CREATE SCHEMA IF NOT EXISTS wallet AUTHORIZATION planext4u_wallet_owner;
ALTER SCHEMA wallet OWNER TO planext4u_wallet_owner;
REVOKE ALL ON SCHEMA wallet FROM PUBLIC;
SET ROLE planext4u_wallet_owner;

CREATE TABLE wallet.accounts (
    tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'), customer_identity_id uuid NOT NULL,
    balance_points bigint NOT NULL DEFAULT 0 CHECK (balance_points >= 0), revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_at timestamptz NOT NULL, PRIMARY KEY (tenant_id, country, customer_identity_id)
);
CREATE TABLE wallet.ledger_entries (
    id uuid PRIMARY KEY, tenant_id uuid NOT NULL, country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'), customer_identity_id uuid NOT NULL,
    entry_type text NOT NULL CHECK (entry_type IN ('CREDIT','DEBIT','EXPIRY','REVERSAL')), category text NOT NULL, source_reference text NOT NULL,
    reverses_entry_id uuid REFERENCES wallet.ledger_entries(id), delta_points bigint NOT NULL CHECK (delta_points <> 0), balance_after bigint NOT NULL CHECK (balance_after >= 0),
    original_expiry_at timestamptz, idempotency_key text NOT NULL, request_fingerprint char(64) NOT NULL, created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, customer_identity_id, idempotency_key)
);
CREATE INDEX wallet_ledger_owner_time_idx ON wallet.ledger_entries (tenant_id, country, customer_identity_id, created_at DESC);
CREATE TABLE wallet.credit_lots (
    ledger_entry_id uuid PRIMARY KEY REFERENCES wallet.ledger_entries(id), remaining_points bigint NOT NULL CHECK (remaining_points >= 0),
    expires_at timestamptz NOT NULL
);
CREATE TABLE wallet.reward_claims (
    tenant_id uuid NOT NULL, country char(2) NOT NULL, claim_type text NOT NULL, claim_reference text NOT NULL,
    customer_identity_id uuid NOT NULL, device_reference_hash char(64), awarded_points bigint NOT NULL CHECK (awarded_points > 0), created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, claim_type, claim_reference)
);
GRANT USAGE ON SCHEMA wallet TO planext4u_wallet_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA wallet TO planext4u_wallet_runtime;
RESET ROLE;
