SET ROLE planext4u_identity_owner;
DROP FUNCTION identity.release_deletion_hold(bigint,uuid,char,bigint,text,uuid,uuid,bigint);
DROP FUNCTION identity.place_deletion_hold(text,uuid,char,text,text,uuid,uuid,bigint);
DROP FUNCTION identity.publish_deletion_policy(uuid,uuid,char,uuid,uuid,bigint);
DROP FUNCTION identity.propose_deletion_policy(uuid,uuid,char,text,text,interval,bigint,text,uuid,uuid,bigint);
DROP FUNCTION identity.require_deletion_governor(uuid,char,uuid,uuid,bigint);
DROP INDEX identity.deletion_policy_version_unique;
REVOKE USAGE ON SCHEMA identity FROM planext4u_admin_runtime;
RESET ROLE;
