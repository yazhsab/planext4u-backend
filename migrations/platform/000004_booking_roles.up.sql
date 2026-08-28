DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_booking_owner') THEN CREATE ROLE planext4u_booking_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_booking_runtime') THEN CREATE ROLE planext4u_booking_runtime NOLOGIN; END IF;
END $$;

GRANT planext4u_booking_owner TO CURRENT_USER;
