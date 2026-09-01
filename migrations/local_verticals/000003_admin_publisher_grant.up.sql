SET ROLE planext4u_local_verticals_owner;
GRANT USAGE ON SCHEMA local_verticals TO planext4u_admin_runtime;
GRANT SELECT, INSERT, UPDATE ON local_verticals.policies TO planext4u_admin_runtime;
GRANT SELECT, UPDATE ON local_verticals.classified_listings TO planext4u_admin_runtime;
RESET ROLE;
