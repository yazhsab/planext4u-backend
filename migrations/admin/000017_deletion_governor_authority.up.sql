BEGIN;
SET ROLE planext4u_admin_owner;
-- A narrow live operator capability for the identity-owned deletion commands.
-- The referenced reviewer function locks both the current directory row and
-- the active session and requires country administration plus fresh MFA.
CREATE FUNCTION admin.lock_deletion_governor(t uuid,c char(2),s uuid,source_session uuid,expected_revision bigint)
RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,admin AS $$
 SELECT admin.lock_participant_reviewer(t,c,s,source_session,expected_revision)
$$;
REVOKE ALL ON FUNCTION admin.lock_deletion_governor(uuid,char,uuid,uuid,bigint) FROM PUBLIC;
GRANT USAGE ON SCHEMA admin TO planext4u_identity_owner;
GRANT EXECUTE ON FUNCTION admin.lock_deletion_governor(uuid,char,uuid,uuid,bigint) TO planext4u_identity_owner;
RESET ROLE;
COMMIT;
