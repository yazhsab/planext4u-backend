BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.fulfillment_read_scope(uuid,char,uuid,uuid,bigint);
RESET ROLE;
COMMIT;
