SET ROLE planext4u_identity_owner;
LOCK TABLE identity.deletion_owner_receipts IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF to_regprocedure('customer_web.privacy_scrub_sessions(text)') IS NOT NULL THEN
  RAISE EXCEPTION 'customer web privacy capability is still installed';
 END IF;
 IF EXISTS(SELECT 1 FROM identity.deletion_owner_receipts WHERE owner='customer_web') THEN
  RAISE EXCEPTION 'customer web privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION identity.privacy_customer_web_record_retained(text,text,text);
DROP FUNCTION identity.privacy_customer_web_erasure_country_scope(text);
REVOKE USAGE ON SCHEMA identity FROM planext4u_customer_web_owner;
RESET ROLE;
