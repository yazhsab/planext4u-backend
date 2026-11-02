BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.lock_participant_reviewer(uuid,char,uuid,uuid,bigint);
RESET ROLE;
COMMIT;
