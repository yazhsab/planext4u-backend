SET ROLE planext4u_identity_owner;

CREATE FUNCTION identity.list_deletion_policy_proposals(t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS TABLE(id uuid,owner text,expected_revision bigint,policy_version text,
 retention_microseconds bigint,reason_reference text,status text,requested_by uuid,
 requested_at timestamptz,approved_by uuid,published_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 RETURN QUERY SELECT p.id,p.owner,p.expected_revision,p.policy_version,
  (extract(epoch from p.retention_interval)*1000000)::bigint,p.reason_reference,
  p.status,p.requested_by,p.requested_at,p.approved_by,p.published_at
 FROM identity.deletion_policy_proposals p
 WHERE p.tenant_id=t::text AND p.country=c
 ORDER BY (p.status='PENDING') DESC,p.requested_at DESC,p.id DESC LIMIT 100;
END $$;
CREATE FUNCTION identity.get_deletion_policy_proposal(pid uuid,t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS TABLE(id uuid,owner text,expected_revision bigint,policy_version text,
 retention_microseconds bigint,reason_reference text,status text,requested_by uuid,
 requested_at timestamptz,approved_by uuid,published_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 RETURN QUERY SELECT p.id,p.owner,p.expected_revision,p.policy_version,
  (extract(epoch from p.retention_interval)*1000000)::bigint,p.reason_reference,
  p.status,p.requested_by,p.requested_at,p.approved_by,p.published_at
 FROM identity.deletion_policy_proposals p
 WHERE p.id=pid AND p.tenant_id=t::text AND p.country=c;
END $$;
CREATE FUNCTION identity.list_deletion_holds(t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS TABLE(id bigint,request_id text,owner text,reason_reference text,
 recorded_by uuid,recorded_at timestamptz,revision bigint)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 RETURN QUERY SELECT h.id,h.request_id,h.owner,h.reason_reference,
  h.recorded_by,h.recorded_at,h.revision
 FROM identity.deletion_legal_holds h
 JOIN identity.account_deletion_requests d ON d.id=h.request_id
 JOIN identity.identities i ON i.id=d.identity_id
 WHERE i.tenant_id=t::text AND d.country=c AND h.released_at IS NULL
 ORDER BY h.id DESC LIMIT 100;
END $$;
CREATE FUNCTION identity.list_deletion_requests(t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS TABLE(id text,identity_id text,status text,requested_at timestamptz,
 effective_at timestamptz,ready_owners bigint,blocked_owners bigint,active_holds bigint)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 RETURN QUERY SELECT d.id,d.identity_id,d.status,d.requested_at,d.effective_at,
  (SELECT count(*) FROM identity.deletion_owner_tasks task WHERE task.request_id=d.id AND task.status='READY'),
  (SELECT count(*) FROM identity.deletion_owner_tasks task WHERE task.request_id=d.id AND task.status<>'READY' AND task.status<>'ACKNOWLEDGED'),
  (SELECT count(*) FROM identity.deletion_legal_holds h WHERE h.request_id=d.id AND h.released_at IS NULL)
 FROM identity.account_deletion_requests d
 JOIN identity.identities i ON i.id=d.identity_id
 WHERE i.tenant_id=t::text AND d.country=c AND d.status='SCHEDULED'
 ORDER BY d.effective_at,d.id LIMIT 100;
END $$;
CREATE FUNCTION identity.get_deletion_request(r text,t uuid,c char(2),a uuid,s uuid,rev bigint)
RETURNS TABLE(id text,identity_id text,status text,requested_at timestamptz,
 effective_at timestamptz,ready_owners bigint,blocked_owners bigint,active_holds bigint)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 PERFORM identity.require_deletion_governor(t,c,a,s,rev);
 RETURN QUERY SELECT d.id,d.identity_id,d.status,d.requested_at,d.effective_at,
  (SELECT count(*) FROM identity.deletion_owner_tasks task WHERE task.request_id=d.id AND task.status='READY'),
  (SELECT count(*) FROM identity.deletion_owner_tasks task WHERE task.request_id=d.id AND task.status<>'READY' AND task.status<>'ACKNOWLEDGED'),
  (SELECT count(*) FROM identity.deletion_legal_holds h WHERE h.request_id=d.id AND h.released_at IS NULL)
 FROM identity.account_deletion_requests d
 JOIN identity.identities i ON i.id=d.identity_id
 WHERE d.id=r AND i.tenant_id=t::text AND d.country=c;
END $$;
REVOKE ALL ON FUNCTION
 identity.list_deletion_policy_proposals(uuid,char,uuid,uuid,bigint),
 identity.get_deletion_policy_proposal(uuid,uuid,char,uuid,uuid,bigint),
 identity.list_deletion_holds(uuid,char,uuid,uuid,bigint),
 identity.list_deletion_requests(uuid,char,uuid,uuid,bigint),
 identity.get_deletion_request(text,uuid,char,uuid,uuid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
 identity.list_deletion_policy_proposals(uuid,char,uuid,uuid,bigint),
 identity.get_deletion_policy_proposal(uuid,uuid,char,uuid,uuid,bigint),
 identity.list_deletion_holds(uuid,char,uuid,uuid,bigint),
 identity.list_deletion_requests(uuid,char,uuid,uuid,bigint),
 identity.get_deletion_request(text,uuid,char,uuid,uuid,bigint) TO planext4u_admin_runtime;
RESET ROLE;
