REVOKE planext4u_supply_owner FROM CURRENT_USER;
REVOKE planext4u_food_owner FROM CURRENT_USER;
REVOKE planext4u_fulfillment_owner FROM CURRENT_USER;
DROP ROLE IF EXISTS planext4u_fulfillment_runtime;
DROP ROLE IF EXISTS planext4u_fulfillment_owner;
DROP ROLE IF EXISTS planext4u_food_runtime;
DROP ROLE IF EXISTS planext4u_food_owner;
DROP ROLE IF EXISTS planext4u_supply_runtime;
DROP ROLE IF EXISTS planext4u_supply_owner;
