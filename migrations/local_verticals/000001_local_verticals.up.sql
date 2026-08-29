CREATE SCHEMA IF NOT EXISTS local_verticals AUTHORIZATION planext4u_local_verticals_owner;
ALTER SCHEMA local_verticals OWNER TO planext4u_local_verticals_owner;
REVOKE ALL ON SCHEMA local_verticals FROM PUBLIC;
SET ROLE planext4u_local_verticals_owner;

CREATE TABLE local_verticals.home_listings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    title text NOT NULL,
    property_type text NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('SALE', 'RENT')),
    locality text NOT NULL,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    area_sq_ft integer NOT NULL CHECK (area_sq_ft > 0),
    bedrooms integer NOT NULL CHECK (bedrooms >= 0),
    price_amount_minor bigint NOT NULL CHECK (price_amount_minor > 0),
    currency char(3) NOT NULL,
    amenities text[] NOT NULL DEFAULT '{}',
    media_asset_ids uuid[] NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'PAUSED', 'CLOSED')),
    kyc_verified boolean NOT NULL DEFAULT false,
    plan text NOT NULL CHECK (plan IN ('STANDARD', 'FEATURED', 'PREMIUM')),
    featured_until timestamptz,
    estimate jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX local_verticals_home_search_idx ON local_verticals.home_listings (tenant_id, country, status, locality, property_type, purpose);

CREATE TABLE local_verticals.home_inquiries (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    listing_id uuid NOT NULL REFERENCES local_verticals.home_listings(id),
    buyer_identity_id uuid NOT NULL,
    message text NOT NULL,
    status text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE local_verticals.home_visits (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    listing_id uuid NOT NULL REFERENCES local_verticals.home_listings(id),
    visitor_identity_id uuid NOT NULL,
    scheduled_at timestamptz NOT NULL,
    status text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE local_verticals.classified_listings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    category text NOT NULL,
    title text NOT NULL,
    description text NOT NULL,
    price_amount_minor bigint NOT NULL CHECK (price_amount_minor >= 0),
    currency char(3) NOT NULL,
    locality text NOT NULL,
    media_asset_ids uuid[] NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('PUBLISHED', 'PENDING_REVIEW', 'EXPIRED', 'REMOVED')),
    plan text NOT NULL CHECK (plan IN ('STANDARD', 'FEATURED')),
    encrypted_contact bytea NOT NULL,
    contact_last4 char(4) NOT NULL,
    whatsapp_enabled boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    featured_until timestamptz,
    report_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX local_verticals_classified_search_idx ON local_verticals.classified_listings (tenant_id, country, status, category, locality, updated_at DESC);

CREATE TABLE local_verticals.reports (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    listing_id uuid NOT NULL REFERENCES local_verticals.classified_listings(id),
    reporter_identity_id uuid NOT NULL,
    reason text NOT NULL,
    details text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, listing_id, reporter_identity_id)
);

GRANT USAGE ON SCHEMA local_verticals TO planext4u_local_verticals_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA local_verticals TO planext4u_local_verticals_runtime;
RESET ROLE;
