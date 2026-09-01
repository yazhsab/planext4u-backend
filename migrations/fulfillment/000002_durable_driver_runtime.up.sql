SET ROLE planext4u_fulfillment_owner;

CREATE TABLE fulfillment.policies (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    version text NOT NULL,
    offer_ttl_seconds integer NOT NULL CHECK (offer_ttl_seconds BETWEEN 15 AND 3600),
    location_ttl_seconds integer NOT NULL CHECK (location_ttl_seconds BETWEEN 15 AND 3600),
    chat_after_delivery_seconds integer NOT NULL CHECK (chat_after_delivery_seconds BETWEEN 60 AND 2592000),
    settlement_cooling_seconds integer NOT NULL CHECK (settlement_cooling_seconds BETWEEN 0 AND 2592000),
    commission_basis_points integer NOT NULL CHECK (commission_basis_points BETWEEN 0 AND 10000),
    tax_basis_points integer NOT NULL CHECK (tax_basis_points BETWEEN 0 AND 10000),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,country)
);

CREATE UNIQUE INDEX fulfillment_one_active_duty_per_rider
    ON fulfillment.duty_sessions (tenant_id,country,rider_identity_id)
    WHERE status='ACTIVE';

CREATE TABLE fulfillment.payout_entry_claims (
    ledger_entry_id uuid PRIMARY KEY REFERENCES fulfillment.ledger_entries(id),
    payout_id uuid NOT NULL REFERENCES fulfillment.payouts(id) ON DELETE CASCADE
);

CREATE TABLE fulfillment.field_check_ins (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    officer_identity_id uuid NOT NULL,
    territory_id uuid NOT NULL REFERENCES fulfillment.territories(id),
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    distance_m double precision NOT NULL CHECK (distance_m >= 0),
    recorded_at timestamptz NOT NULL
);
CREATE INDEX fulfillment_field_check_ins_lookup
    ON fulfillment.field_check_ins (tenant_id,country,territory_id,recorded_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON fulfillment.policies,fulfillment.payout_entry_claims,fulfillment.field_check_ins TO planext4u_fulfillment_runtime;
RESET ROLE;
