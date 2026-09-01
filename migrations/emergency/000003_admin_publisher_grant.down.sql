SET ROLE planext4u_emergency_owner;
REVOKE SELECT, INSERT, UPDATE ON emergency.policies FROM planext4u_admin_runtime;
REVOKE USAGE ON SCHEMA emergency FROM planext4u_admin_runtime;
RESET ROLE;
