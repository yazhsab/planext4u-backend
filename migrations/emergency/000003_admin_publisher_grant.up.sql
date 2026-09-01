SET ROLE planext4u_emergency_owner;
GRANT USAGE ON SCHEMA emergency TO planext4u_admin_runtime;
GRANT SELECT, INSERT, UPDATE ON emergency.policies TO planext4u_admin_runtime;
RESET ROLE;
