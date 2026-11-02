SET ROLE planext4u_identity_owner;
LOCK TABLE identity.deletion_owner_receipts IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF to_regprocedure('bankverification.privacy_scrub_link_attempts(text)') IS NOT NULL THEN
  RAISE EXCEPTION 'bank privacy capability is still installed';
 END IF;
 IF EXISTS(SELECT 1 FROM identity.deletion_owner_receipts WHERE owner='bankverification') THEN
  RAISE EXCEPTION 'bank privacy evidence blocks rollback';
 END IF;
END $$;
CREATE OR REPLACE FUNCTION identity.lock_bank_link_session(t text,c char(2),s text,sid text,require_fresh boolean)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE disabled timestamptz; session_row identity.sessions%ROWTYPE;
BEGIN
 SELECT disabled_at INTO disabled FROM identity.identities WHERE id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR disabled IS NOT NULL THEN RETURN false; END IF;
 SELECT * INTO session_row FROM identity.sessions WHERE id=sid AND identity_id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR session_row.country<>c OR session_row.revoked_at IS NOT NULL OR session_row.refresh_expires_at<=clock_timestamp()
    OR session_row.authenticated_at>clock_timestamp()+interval '1 minute' THEN RETURN false; END IF;
 IF require_fresh AND session_row.authenticated_at<clock_timestamp()-interval '15 minutes' THEN RETURN false; END IF;
 IF EXISTS(SELECT 1 FROM identity.account_restrictions WHERE identity_id=s AND source<>'ACTIVE') THEN RETURN false; END IF;
 RETURN true;
END $$;
DROP FUNCTION identity.privacy_bankverification_record_retained(text,text,text);
DROP FUNCTION identity.privacy_bankverification_erasure_country_scope(text);
REVOKE USAGE ON SCHEMA identity FROM planext4u_bankverification_owner;
RESET ROLE;
