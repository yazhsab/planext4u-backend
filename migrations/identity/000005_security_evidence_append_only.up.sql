BEGIN;
SET ROLE planext4u_identity_owner;
REVOKE UPDATE, DELETE, TRUNCATE ON identity.security_events FROM planext4u_identity_runtime;
GRANT SELECT, INSERT ON identity.security_events TO planext4u_identity_runtime;
RESET ROLE;
COMMIT;
