SET ROLE planext4u_identity_owner;

-- Only the exact due request can authorize an owner scrub. The request row
-- lock serializes legal-hold insertion through its foreign key until commit.
CREATE FUNCTION identity.privacy_owner_erasure_scope(r text,o text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 -- Lock in a separate statement so a hold committed while waiting becomes
 -- visible to the following READ COMMITTED readiness query.
 PERFORM 1 FROM identity.account_deletion_requests d WHERE d.id=r FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'owner erasure request not found'; END IF;
 RETURN QUERY SELECT i.tenant_id,d.identity_id,p.policy_version
 FROM identity.account_deletion_requests d
 JOIN identity.identities i ON i.id=d.identity_id
 JOIN identity.deletion_owner_tasks task ON task.request_id=d.id AND task.owner=o AND task.status='READY'
 JOIN identity.deletion_retention_policies p ON p.tenant_id=i.tenant_id AND p.owner=o
 WHERE d.id=r AND d.status='SCHEDULED' AND d.effective_at<=clock_timestamp()
   AND d.effective_at+p.retention_interval<=clock_timestamp()
   AND NOT EXISTS(SELECT 1 FROM identity.deletion_legal_holds h
                  WHERE h.request_id=d.id AND h.released_at IS NULL)
 FOR UPDATE OF d,task,p;
 IF NOT FOUND THEN RAISE EXCEPTION 'owner erasure is not ready'; END IF;
END $$;
REVOKE ALL ON FUNCTION identity.privacy_owner_erasure_scope(text,text) FROM PUBLIC;

CREATE FUNCTION identity.privacy_record_retained(r text,o text,pv text,reference text)
RETURNS void LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE active_policy text; recorded identity.deletion_owner_receipts%ROWTYPE;
BEGIN
 SELECT scope.policy_version INTO active_policy
 FROM identity.privacy_owner_erasure_scope(r,o) scope;
 IF active_policy IS NULL OR active_policy<>pv OR reference IS NULL OR length(reference) NOT BETWEEN 1 AND 256 THEN
  RAISE EXCEPTION 'owner erasure policy or evidence changed';
 END IF;
 INSERT INTO identity.deletion_owner_receipts(request_id,owner,outcome,policy_version,evidence_reference,attested_at)
 VALUES(r,o,'RETAINED',pv,reference,clock_timestamp()) ON CONFLICT(request_id,owner) DO NOTHING;
 SELECT * INTO recorded FROM identity.deletion_owner_receipts WHERE request_id=r AND owner=o;
 IF NOT FOUND OR recorded.outcome<>'RETAINED' OR recorded.policy_version<>pv
    OR recorded.evidence_reference<>reference THEN
  RAISE EXCEPTION 'owner erasure receipt conflicts with existing evidence';
 END IF;
END $$;
REVOKE ALL ON FUNCTION identity.privacy_record_retained(text,text,text,text) FROM PUBLIC;

CREATE FUNCTION identity.privacy_supply_erasure_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT * FROM identity.privacy_owner_erasure_scope(r,'supply')
$$;
CREATE FUNCTION identity.privacy_fulfillment_erasure_scope(r text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT * FROM identity.privacy_owner_erasure_scope(r,'fulfillment')
$$;
CREATE FUNCTION identity.privacy_supply_record_retained(r text,pv text,reference text)
RETURNS void LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT identity.privacy_record_retained(r,'supply',pv,reference)
$$;
CREATE FUNCTION identity.privacy_fulfillment_record_retained(r text,pv text,reference text)
RETURNS void LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
 SELECT identity.privacy_record_retained(r,'fulfillment',pv,reference)
$$;
REVOKE ALL ON FUNCTION identity.privacy_supply_erasure_scope(text),
 identity.privacy_fulfillment_erasure_scope(text),
 identity.privacy_supply_record_retained(text,text,text),
 identity.privacy_fulfillment_record_retained(text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.privacy_supply_erasure_scope(text),
 identity.privacy_supply_record_retained(text,text,text) TO planext4u_supply_owner;
GRANT EXECUTE ON FUNCTION identity.privacy_fulfillment_erasure_scope(text),
 identity.privacy_fulfillment_record_retained(text,text,text) TO planext4u_fulfillment_owner;
RESET ROLE;
