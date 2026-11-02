SET ROLE planext4u_identity_owner;

-- Serialize bank-link writes against any due account deletion in
-- account/request/session order, including another governing country.
CREATE OR REPLACE FUNCTION identity.lock_bank_link_session(t text,c char(2),s text,sid text,require_fresh boolean)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE disabled timestamptz; session_row identity.sessions%ROWTYPE;
BEGIN
 SELECT disabled_at INTO disabled FROM identity.identities WHERE id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR disabled IS NOT NULL THEN RETURN false; END IF;
 PERFORM 1 FROM identity.account_deletion_requests
 WHERE identity_id=s AND status='SCHEDULED'
   AND effective_at<=clock_timestamp() FOR SHARE;
 IF FOUND THEN RETURN false; END IF;
 SELECT * INTO session_row FROM identity.sessions WHERE id=sid AND identity_id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR session_row.country<>c OR session_row.revoked_at IS NOT NULL OR session_row.refresh_expires_at<=clock_timestamp()
    OR session_row.authenticated_at>clock_timestamp()+interval '1 minute' THEN RETURN false; END IF;
 IF require_fresh AND session_row.authenticated_at<clock_timestamp()-interval '15 minutes' THEN RETURN false; END IF;
 IF EXISTS(SELECT 1 FROM identity.account_restrictions WHERE identity_id=s AND source<>'ACTIVE') THEN RETURN false; END IF;
 RETURN true;
END $$;

-- A bank-link scrub receives only the due request's locked jurisdiction and
-- policy. The bank owner cannot choose an account or attest completion.
CREATE FUNCTION identity.privacy_bankverification_erasure_country_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text,country text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,d.country::text
 FROM identity.privacy_owner_erasure_scope(r,'bankverification') scope
 JOIN identity.account_deletion_requests d ON d.id=r
 WHERE d.country IS NOT NULL
$$;
CREATE FUNCTION identity.privacy_bankverification_record_retained(r text,pv text,reference text)
RETURNS void LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT identity.privacy_record_retained(r,'bankverification',pv,reference)
$$;
REVOKE ALL ON FUNCTION identity.privacy_bankverification_erasure_country_scope(text),
 identity.privacy_bankverification_record_retained(text,text,text) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_bankverification_owner;
GRANT EXECUTE ON FUNCTION identity.privacy_bankverification_erasure_country_scope(text),
 identity.privacy_bankverification_record_retained(text,text,text)
 TO planext4u_bankverification_owner;
RESET ROLE;
