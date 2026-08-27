SET ROLE planext4u_commerce_owner;
DROP TABLE IF EXISTS commerce.checkout_quotes;
DROP TABLE IF EXISTS commerce.promotions;
DROP TABLE IF EXISTS commerce.delivery_slots;
DROP TABLE IF EXISTS commerce.customer_addresses;
ALTER TABLE commerce.cart_items DROP COLUMN IF EXISTS vendor_id;
RESET ROLE;
