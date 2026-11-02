SET ROLE planext4u_identity_owner;
-- Owner services may expose a subject's own data only while the exact source
-- session is live. No participant role is required: historical applications
-- remain portable after a role is removed.
CREATE FUNCTION identity.privacy_export_session(t text,c char(2),s text,sid text)
RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT EXISTS (
  SELECT 1 FROM identity.sessions source
  JOIN identity.identities account ON account.id=source.identity_id
  WHERE account.id=s AND account.tenant_id=t AND account.disabled_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM identity.account_restrictions r
                    WHERE r.identity_id=s AND r.source<>'ACTIVE')
    AND source.id=sid AND source.identity_id=s AND source.tenant_id=t AND source.country=c
    AND source.revoked_at IS NULL AND source.refresh_expires_at>clock_timestamp()
    AND source.authenticated_at<=clock_timestamp()+interval '1 minute'
    AND NOT EXISTS (
      SELECT 1 FROM identity.account_deletion_requests d
      WHERE d.identity_id=s AND d.status='SCHEDULED' AND d.effective_at<=clock_timestamp()
    )
 );
$$;
REVOKE ALL ON FUNCTION identity.privacy_export_session(text,char,text,text) FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO planext4u_supply_owner,planext4u_fulfillment_owner;
GRANT EXECUTE ON FUNCTION identity.privacy_export_session(text,char,text,text)
 TO planext4u_supply_owner,planext4u_fulfillment_owner;
RESET ROLE;
