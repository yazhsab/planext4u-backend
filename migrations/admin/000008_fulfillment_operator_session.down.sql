BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.lock_fulfillment_operator(uuid,char,uuid,uuid,bigint);
RESET ROLE;
COMMIT;
