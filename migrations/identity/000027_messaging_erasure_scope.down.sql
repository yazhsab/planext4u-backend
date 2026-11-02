SET ROLE planext4u_identity_owner;
LOCK TABLE identity.deletion_owner_receipts IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname='messaging' AND p.proname='privacy_scrub_notification_outbox' AND p.pronargs=1) THEN
  RAISE EXCEPTION 'messaging privacy capability is still installed';
 END IF;
 IF EXISTS(SELECT 1 FROM identity.deletion_owner_receipts WHERE owner='messaging') THEN
  RAISE EXCEPTION 'messaging privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION identity.privacy_messaging_record_retained(text,text,text);
DROP FUNCTION identity.privacy_messaging_erasure_country_scope(text);
REVOKE USAGE ON SCHEMA identity FROM planext4u_messaging_owner;
RESET ROLE;
