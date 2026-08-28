DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_social_owner') THEN CREATE ROLE planext4u_social_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_social_runtime') THEN CREATE ROLE planext4u_social_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_local_verticals_owner') THEN CREATE ROLE planext4u_local_verticals_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_local_verticals_runtime') THEN CREATE ROLE planext4u_local_verticals_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_emergency_owner') THEN CREATE ROLE planext4u_emergency_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_emergency_runtime') THEN CREATE ROLE planext4u_emergency_runtime NOLOGIN; END IF;
END $$;

GRANT planext4u_social_owner TO CURRENT_USER;
GRANT planext4u_local_verticals_owner TO CURRENT_USER;
GRANT planext4u_emergency_owner TO CURRENT_USER;
