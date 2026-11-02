SET ROLE planext4u_commerce_owner;
ALTER TABLE commerce.customer_addresses DROP COLUMN geography;
RESET ROLE;
