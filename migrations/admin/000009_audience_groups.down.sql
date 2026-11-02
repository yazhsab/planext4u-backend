BEGIN;
SET ROLE planext4u_admin_owner;
REVOKE EXECUTE ON FUNCTION admin.lock_fulfillment_operator(uuid,char,uuid,uuid,bigint) FROM planext4u_admin_runtime;
DROP FUNCTION admin.audience_member(uuid,char,uuid,uuid);
DROP TABLE admin.audience_group_versions;
DROP TABLE admin.audience_groups;
RESET ROLE;
COMMIT;
