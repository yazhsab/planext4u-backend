SET ROLE planext4u_identity_owner;
CREATE OR REPLACE FUNCTION identity.lock_participant_session(t text,c char(2),s text,sid text,participant_role text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE account identity.identities%ROWTYPE; source identity.sessions%ROWTYPE;
BEGIN
 IF participant_role NOT IN ('VENDOR','RIDER') THEN RETURN false; END IF;
 SELECT * INTO account FROM identity.identities WHERE id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR account.disabled_at IS NOT NULL THEN RETURN false; END IF;
 PERFORM 1 FROM identity.account_deletion_requests
   WHERE identity_id=s AND status='SCHEDULED' AND effective_at<=clock_timestamp() FOR SHARE;
 IF FOUND THEN RETURN false; END IF;
 PERFORM 1 FROM identity.identity_roles WHERE identity_id=s AND role=participant_role FOR SHARE;
 IF NOT FOUND THEN RETURN false; END IF;
 SELECT * INTO source FROM identity.sessions WHERE id=sid AND identity_id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR source.country<>c OR source.revoked_at IS NOT NULL OR source.refresh_expires_at<=clock_timestamp()
   OR source.authenticated_at>clock_timestamp()+interval '1 minute' THEN RETURN false; END IF;
 RETURN true;
END $$;
RESET ROLE;
