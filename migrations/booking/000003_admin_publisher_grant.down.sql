SET ROLE planext4u_booking_owner;
REVOKE SELECT, INSERT, UPDATE ON booking.policies FROM planext4u_admin_runtime;
REVOKE USAGE ON SCHEMA booking FROM planext4u_admin_runtime;
RESET ROLE;
