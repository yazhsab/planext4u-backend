SET ROLE planext4u_identity_owner;
DROP FUNCTION identity.get_deletion_request(text,uuid,char,uuid,uuid,bigint);
DROP FUNCTION identity.list_deletion_requests(uuid,char,uuid,uuid,bigint);
DROP FUNCTION identity.list_deletion_holds(uuid,char,uuid,uuid,bigint);
DROP FUNCTION identity.get_deletion_policy_proposal(uuid,uuid,char,uuid,uuid,bigint);
DROP FUNCTION identity.list_deletion_policy_proposals(uuid,char,uuid,uuid,bigint);
RESET ROLE;
