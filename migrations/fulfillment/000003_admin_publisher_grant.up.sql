SET ROLE planext4u_fulfillment_owner;
GRANT USAGE ON SCHEMA fulfillment TO planext4u_admin_runtime;
GRANT SELECT, INSERT, UPDATE ON fulfillment.policies TO planext4u_admin_runtime;
RESET ROLE;
