SET ROLE planext4u_local_verticals_owner;
REVOKE SELECT, UPDATE ON local_verticals.classified_listings FROM planext4u_admin_runtime;
REVOKE SELECT, INSERT, UPDATE ON local_verticals.policies FROM planext4u_admin_runtime;
REVOKE USAGE ON SCHEMA local_verticals FROM planext4u_admin_runtime;
RESET ROLE;
