SET ROLE planext4u_fulfillment_owner;
REVOKE SELECT, INSERT, UPDATE ON fulfillment.policies FROM planext4u_admin_runtime;
REVOKE USAGE ON SCHEMA fulfillment FROM planext4u_admin_runtime;
RESET ROLE;
