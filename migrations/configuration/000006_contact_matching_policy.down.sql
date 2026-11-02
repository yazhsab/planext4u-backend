BEGIN;
SET ROLE planext4u_configuration_owner;
DROP FUNCTION configuration.contact_matching_policy(uuid,char);
RESET ROLE;
COMMIT;
