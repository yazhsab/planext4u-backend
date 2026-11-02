BEGIN;
SET ROLE planext4u_identity_owner;
GRANT UPDATE, DELETE ON identity.security_events TO planext4u_identity_runtime;
RESET ROLE;
COMMIT;
