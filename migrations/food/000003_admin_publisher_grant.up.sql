SET ROLE planext4u_food_owner;
GRANT USAGE ON SCHEMA food TO planext4u_admin_runtime;
GRANT SELECT, INSERT, UPDATE ON food.policies TO planext4u_admin_runtime;
RESET ROLE;
