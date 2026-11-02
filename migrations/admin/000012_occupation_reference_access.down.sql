BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.active_occupations(uuid,char,text,uuid);
DROP FUNCTION admin.lock_occupation_reference(uuid,char,uuid);
RESET ROLE;
COMMIT;
