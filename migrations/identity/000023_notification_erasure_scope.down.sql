SET ROLE planext4u_identity_owner;
LOCK TABLE identity.deletion_owner_receipts IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF to_regprocedure('notification.privacy_scrub_push_tokens(text)') IS NOT NULL THEN
  RAISE EXCEPTION 'notification privacy capability is still installed';
 END IF;
 IF EXISTS(SELECT 1 FROM identity.deletion_owner_receipts WHERE owner='notification') THEN
  RAISE EXCEPTION 'notification privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION identity.privacy_notification_record_retained(text,text,text);
DROP FUNCTION identity.privacy_notification_erasure_country_scope(text);
REVOKE USAGE ON SCHEMA identity FROM planext4u_notification_owner;
RESET ROLE;
