BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.lock_deletion_governor(uuid,char,uuid,uuid,bigint);
REVOKE USAGE ON SCHEMA admin FROM planext4u_identity_owner;
RESET ROLE;
COMMIT;
