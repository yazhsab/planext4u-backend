REVOKE planext4u_emergency_owner FROM CURRENT_USER;
REVOKE planext4u_local_verticals_owner FROM CURRENT_USER;
REVOKE planext4u_social_owner FROM CURRENT_USER;
DROP ROLE IF EXISTS planext4u_emergency_runtime;
DROP ROLE IF EXISTS planext4u_emergency_owner;
DROP ROLE IF EXISTS planext4u_local_verticals_runtime;
DROP ROLE IF EXISTS planext4u_local_verticals_owner;
DROP ROLE IF EXISTS planext4u_social_runtime;
DROP ROLE IF EXISTS planext4u_social_owner;
