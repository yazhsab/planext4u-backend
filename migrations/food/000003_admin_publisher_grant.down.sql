SET ROLE planext4u_food_owner;
REVOKE SELECT, INSERT, UPDATE ON food.policies FROM planext4u_admin_runtime;
REVOKE USAGE ON SCHEMA food FROM planext4u_admin_runtime;
RESET ROLE;
