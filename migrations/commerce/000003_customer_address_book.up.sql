SET ROLE planext4u_commerce_owner;

ALTER TABLE commerce.customer_addresses
    ADD COLUMN line1 text NOT NULL DEFAULT '',
    ADD COLUMN line2 text NOT NULL DEFAULT '',
    ADD COLUMN latitude double precision,
    ADD COLUMN longitude double precision,
    ADD COLUMN serviceable boolean NOT NULL DEFAULT false,
    ADD COLUMN is_default boolean NOT NULL DEFAULT false,
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);

ALTER TABLE commerce.customer_addresses
    ADD CONSTRAINT customer_addresses_latitude CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
    ADD CONSTRAINT customer_addresses_longitude CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180);

CREATE UNIQUE INDEX commerce_customer_addresses_one_default_idx
    ON commerce.customer_addresses (tenant_id, country, customer_identity_id)
    WHERE active AND is_default;

GRANT SELECT, INSERT, UPDATE, DELETE ON commerce.customer_addresses TO planext4u_commerce_runtime;
RESET ROLE;
