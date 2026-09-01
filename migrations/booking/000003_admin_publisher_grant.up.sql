SET ROLE planext4u_booking_owner;
GRANT USAGE ON SCHEMA booking TO planext4u_admin_runtime;
GRANT SELECT, INSERT, UPDATE ON booking.policies TO planext4u_admin_runtime;
RESET ROLE;
