SET ROLE planext4u_identity_owner;
LOCK TABLE identity.deletion_owner_receipts IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname='social' AND p.proname='privacy_scrub_ephemeral' AND p.pronargs=1) THEN
  RAISE EXCEPTION 'social privacy capability is still installed';
 END IF;
 IF EXISTS(SELECT 1 FROM identity.deletion_owner_receipts WHERE owner='social') THEN
  RAISE EXCEPTION 'social privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION identity.privacy_social_record_retained(text,text,text);
DROP FUNCTION identity.privacy_social_erasure_country_scope(text);
REVOKE USAGE ON SCHEMA identity FROM planext4u_social_owner;
RESET ROLE;
