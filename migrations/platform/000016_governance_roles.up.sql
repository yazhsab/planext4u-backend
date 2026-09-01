DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_governance_owner') THEN CREATE ROLE planext4u_governance_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_governance_runtime') THEN CREATE ROLE planext4u_governance_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_governance_publisher') THEN CREATE ROLE planext4u_governance_publisher NOLOGIN; END IF;
END $$;

GRANT planext4u_governance_owner TO CURRENT_USER;
GRANT planext4u_governance_runtime, planext4u_governance_publisher TO planext4u_admin_runtime;
