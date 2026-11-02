BEGIN;
SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.lock_participant_field_access(uuid,char,uuid,uuid,bigint,uuid);
DROP FUNCTION admin.participant_field_assignment_allowed(uuid,char,uuid,uuid);
RESET ROLE;
COMMIT;
