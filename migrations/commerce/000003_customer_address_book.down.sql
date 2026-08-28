SET ROLE planext4u_commerce_owner;
DROP INDEX IF EXISTS commerce.commerce_customer_addresses_one_default_idx;
ALTER TABLE commerce.customer_addresses
    DROP CONSTRAINT IF EXISTS customer_addresses_longitude,
    DROP CONSTRAINT IF EXISTS customer_addresses_latitude,
    DROP COLUMN IF EXISTS revision,
    DROP COLUMN IF EXISTS is_default,
    DROP COLUMN IF EXISTS serviceable,
    DROP COLUMN IF EXISTS longitude,
    DROP COLUMN IF EXISTS latitude,
    DROP COLUMN IF EXISTS line2,
    DROP COLUMN IF EXISTS line1;
RESET ROLE;
