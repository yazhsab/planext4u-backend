BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.lock_fulfillment_finance_operator(uuid,char,uuid,uuid,bigint,text,text);
RESET ROLE;
COMMIT;
