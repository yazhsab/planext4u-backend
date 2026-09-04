DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_customer_web_owner') THEN CREATE ROLE planext4u_customer_web_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_customer_web_runtime') THEN CREATE ROLE planext4u_customer_web_runtime NOLOGIN; END IF;
END $$;

GRANT planext4u_customer_web_owner TO CURRENT_USER;
GRANT planext4u_customer_web_runtime TO CURRENT_USER;
