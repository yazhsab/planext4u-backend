DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_admin_owner') THEN CREATE ROLE planext4u_admin_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_admin_runtime') THEN CREATE ROLE planext4u_admin_runtime NOLOGIN; END IF;
END $$;

GRANT planext4u_admin_owner TO CURRENT_USER;
