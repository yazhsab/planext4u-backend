SET ROLE planext4u_commerce_owner;
ALTER TABLE commerce.customer_addresses ADD COLUMN geography jsonb;
ALTER TABLE commerce.customer_addresses ADD CONSTRAINT address_geography_shape
 CHECK (geography IS NULL OR (jsonb_typeof(geography)='object' AND jsonb_typeof(geography->'path')='array' AND jsonb_array_length(geography->'path')=5 AND geography ? 'area_id'));
RESET ROLE;
