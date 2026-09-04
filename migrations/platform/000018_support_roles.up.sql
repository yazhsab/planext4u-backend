DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_support_owner') THEN
        CREATE ROLE planext4u_support_owner NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_support_runtime') THEN
        CREATE ROLE planext4u_support_runtime NOLOGIN;
    END IF;
END $$;

GRANT planext4u_support_owner TO CURRENT_USER;
GRANT planext4u_support_runtime TO CURRENT_USER;
