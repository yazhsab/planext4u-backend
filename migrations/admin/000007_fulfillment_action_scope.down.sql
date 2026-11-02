BEGIN;
SET ROLE planext4u_admin_owner;
REVOKE EXECUTE ON FUNCTION admin.fulfillment_territory_allowed(uuid,char,uuid,uuid,text) FROM planext4u_admin_runtime;
RESET ROLE;
COMMIT;
