BEGIN;
SET ROLE planext4u_identity_owner;
-- Only this bounded operation crosses the identity ownership boundary. Locks
-- are held by the caller through its bank-link commit, in account/session order.
CREATE FUNCTION identity.lock_bank_link_session(t text,c char(2),s text,sid text,require_fresh boolean)
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
REVOKE ALL ON FUNCTION identity.lock_bank_link_session(text,char,text,text,boolean) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_bankverification_runtime;
GRANT EXECUTE ON FUNCTION identity.lock_bank_link_session(text,char,text,text,boolean) TO planext4u_bankverification_runtime;
RESET ROLE;
COMMIT;
