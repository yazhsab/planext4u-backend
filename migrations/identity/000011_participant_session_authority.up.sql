SET ROLE planext4u_identity_owner;
CREATE FUNCTION identity.lock_participant_session(t text,c char(2),s text,sid text,participant_role text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE account identity.identities%ROWTYPE; source identity.sessions%ROWTYPE;
BEGIN
 IF participant_role NOT IN ('VENDOR','RIDER') THEN RETURN false; END IF;
 SELECT * INTO account FROM identity.identities WHERE id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR account.disabled_at IS NOT NULL THEN RETURN false; END IF;
 PERFORM 1 FROM identity.identity_roles WHERE identity_id=s AND role=participant_role FOR SHARE;
 IF NOT FOUND THEN RETURN false; END IF;
 SELECT * INTO source FROM identity.sessions WHERE id=sid AND identity_id=s AND tenant_id=t FOR SHARE;
 IF NOT FOUND OR source.country<>c OR source.revoked_at IS NOT NULL OR source.refresh_expires_at<=clock_timestamp()
   OR source.authenticated_at>clock_timestamp()+interval '1 minute' THEN RETURN false; END IF;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION identity.lock_participant_session(text,char,text,text,text) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_supply_runtime,planext4u_fulfillment_runtime;
GRANT EXECUTE ON FUNCTION identity.lock_participant_session(text,char,text,text,text) TO planext4u_supply_runtime,planext4u_fulfillment_runtime;
RESET ROLE;
