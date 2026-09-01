SET ROLE planext4u_commerce_owner;

CREATE TABLE commerce.pricing_policies (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    version text NOT NULL CHECK (length(version) BETWEEN 1 AND 128),
    product_tax_basis_points integer NOT NULL CHECK (product_tax_basis_points BETWEEN 0 AND 10000),
    product_tax_treatment text NOT NULL CHECK (product_tax_treatment IN ('INCLUSIVE','EXCLUSIVE')),
    platform_fee_minor bigint NOT NULL CHECK (platform_fee_minor >= 0),
    platform_fee_tax_basis_points integer NOT NULL CHECK (platform_fee_tax_basis_points BETWEEN 0 AND 10000),
    wallet_point_value_minor bigint NOT NULL CHECK (wallet_point_value_minor > 0),
    wallet_mode text NOT NULL CHECK (wallet_mode IN ('HYBRID_PAYMENT','POINTS_ONLY')),
    quote_ttl_seconds integer NOT NULL CHECK (quote_ttl_seconds BETWEEN 1 AND 1800),
    reservation_ttl_seconds integer NOT NULL CHECK (reservation_ttl_seconds BETWEEN 1 AND 1800),
    active boolean NOT NULL DEFAULT false,
    published_at timestamptz NOT NULL,
    published_by uuid NOT NULL,
    UNIQUE (tenant_id,country,version)
);
CREATE UNIQUE INDEX commerce_pricing_policy_active_idx
    ON commerce.pricing_policies (tenant_id,country) WHERE active;

CREATE TABLE commerce.postal_zones (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    postal_code text NOT NULL CHECK (length(postal_code) BETWEEN 3 AND 12),
    locality text NOT NULL CHECK (length(locality) BETWEEN 2 AND 100),
    active boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL,
    updated_by uuid NOT NULL,
    UNIQUE (tenant_id,country,postal_code,locality)
);
CREATE INDEX commerce_postal_zones_active_idx
    ON commerce.postal_zones (tenant_id,country,postal_code) WHERE active;

CREATE TABLE commerce.commercial_policies (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    version text NOT NULL CHECK (length(version) BETWEEN 1 AND 128),
    default_vendor_tier text NOT NULL CHECK (length(default_vendor_tier) BETWEEN 1 AND 128),
    default_commission_basis_points integer NOT NULL CHECK (default_commission_basis_points BETWEEN 0 AND 10000),
    default_wallet_basis_points integer NOT NULL CHECK (default_wallet_basis_points BETWEEN 0 AND 10000),
    active boolean NOT NULL DEFAULT false,
    published_at timestamptz NOT NULL,
    published_by uuid NOT NULL,
    UNIQUE (tenant_id,country,version)
);
CREATE UNIQUE INDEX commerce_commercial_policy_active_idx
    ON commerce.commercial_policies (tenant_id,country) WHERE active;

CREATE TABLE commerce.commercial_vendor_rules (
    id uuid PRIMARY KEY,
    policy_id uuid NOT NULL REFERENCES commerce.commercial_policies(id) ON DELETE CASCADE,
    vendor_id uuid NOT NULL,
    vendor_tier text NOT NULL CHECK (length(vendor_tier) BETWEEN 1 AND 128),
    commission_basis_points integer NOT NULL CHECK (commission_basis_points BETWEEN 0 AND 10000),
    wallet_basis_points integer NOT NULL CHECK (wallet_basis_points BETWEEN 0 AND 10000),
    UNIQUE (policy_id,vendor_id)
);

CREATE TABLE commerce.commercial_product_rules (
    id uuid PRIMARY KEY,
    policy_id uuid NOT NULL REFERENCES commerce.commercial_policies(id) ON DELETE CASCADE,
    vendor_id uuid NOT NULL,
    item_id uuid,
    variant_id uuid,
    vendor_tier text NOT NULL CHECK (length(vendor_tier) BETWEEN 1 AND 128),
    commission_basis_points integer NOT NULL CHECK (commission_basis_points BETWEEN 0 AND 10000),
    wallet_basis_points integer NOT NULL CHECK (wallet_basis_points BETWEEN 0 AND 10000),
    CHECK (item_id IS NOT NULL OR variant_id IS NOT NULL)
);
CREATE UNIQUE INDEX commerce_commercial_product_item_idx
    ON commerce.commercial_product_rules (policy_id,vendor_id,item_id) WHERE item_id IS NOT NULL AND variant_id IS NULL;
CREATE UNIQUE INDEX commerce_commercial_product_variant_idx
    ON commerce.commercial_product_rules (policy_id,vendor_id,variant_id) WHERE variant_id IS NOT NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    commerce.pricing_policies,
    commerce.postal_zones,
    commerce.commercial_policies,
    commerce.commercial_vendor_rules,
    commerce.commercial_product_rules
TO planext4u_commerce_runtime;

RESET ROLE;
