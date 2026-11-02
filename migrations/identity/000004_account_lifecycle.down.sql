BEGIN;
SET ROLE planext4u_identity_owner;
DROP TABLE identity.account_restrictions;
RESET ROLE;
COMMIT;
