BEGIN;
SET ROLE planext4u_admin_owner;
REVOKE EXECUTE ON FUNCTION admin.lock_participant_reviewer(uuid,char,uuid,uuid,bigint) FROM planext4u_admin_runtime;
DROP FUNCTION admin.lock_participant_access(uuid,char,uuid,uuid,bigint,text,uuid,text);
DROP FUNCTION admin.participant_access_allowed(uuid,char,uuid,uuid,text,uuid,text);
DROP TABLE admin.participant_territory_events,admin.participant_territory_bindings;
RESET ROLE;
COMMIT;
