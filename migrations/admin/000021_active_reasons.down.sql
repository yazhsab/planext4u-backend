BEGIN;
SET ROLE planext4u_admin_owner;
REVOKE ALL ON FUNCTION admin.active_reasons(uuid,char,text,uuid) FROM planext4u_configuration_runtime;
DROP FUNCTION admin.active_reasons(uuid,char,text,uuid);
RESET ROLE;
COMMIT;
