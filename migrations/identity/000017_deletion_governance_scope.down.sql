SET ROLE planext4u_identity_owner;
-- Restore the previous fail-closed tenant policy gate before removing v2 state.
CREATE OR REPLACE FUNCTION identity.guard_deletion_completed() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE tenant text;
BEGIN
 IF TG_OP='UPDATE' AND OLD.status IN ('CANCELLED','COMPLETED') AND NEW.status<>OLD.status THEN
  RAISE EXCEPTION 'deletion terminal state cannot change';
 END IF;
 IF NEW.status<>'COMPLETED' THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND OLD.status<>'SCHEDULED' THEN
  RAISE EXCEPTION 'deletion must be scheduled before completion';
 END IF;
 IF clock_timestamp()<NEW.effective_at THEN RAISE EXCEPTION 'deletion recovery period has not elapsed'; END IF;
 SELECT i.tenant_id INTO tenant FROM identity.identities i WHERE i.id=NEW.identity_id;
 IF tenant IS NULL OR NOT EXISTS(SELECT 1 FROM identity.deletion_owner_requirements) THEN
  RAISE EXCEPTION 'deletion owner registry is incomplete';
 END IF;
 IF EXISTS(SELECT 1 FROM identity.deletion_legal_holds h
           WHERE h.request_id=NEW.id AND h.released_at IS NULL) THEN
  RAISE EXCEPTION 'deletion has an unresolved legal hold';
 END IF;
 IF EXISTS (
  SELECT 1 FROM identity.deletion_owner_requirements required
  LEFT JOIN identity.deletion_retention_policies policy
    ON policy.tenant_id=tenant AND policy.owner=required.owner
  LEFT JOIN identity.deletion_owner_receipts receipt
    ON receipt.request_id=NEW.id AND receipt.owner=required.owner
  WHERE policy.owner IS NULL OR receipt.owner IS NULL
     OR receipt.policy_version<>policy.policy_version
     OR receipt.outcome NOT IN ('ERASED','NO_DATA')
     OR receipt.attested_at<NEW.effective_at
     OR receipt.attested_at>clock_timestamp()
     OR clock_timestamp()<NEW.effective_at+policy.retention_interval
 ) THEN RAISE EXCEPTION 'deletion lacks current retention policy and owner erasure evidence'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION identity.privacy_owner_erasure_scope(r text,o text)
RETURNS TABLE(tenant_id text,subject_id text,policy_version text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
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
REVOKE SELECT ON identity.deletion_country_policies FROM planext4u_identity_runtime;
DROP TRIGGER deletion_hold_placement_guard ON identity.deletion_legal_holds;
DROP FUNCTION identity.guard_deletion_hold_placement();
DROP TABLE identity.deletion_governance_events;
ALTER TABLE identity.deletion_legal_holds
 DROP COLUMN recorded_by, DROP COLUMN released_by,
 DROP COLUMN release_reason_reference, DROP COLUMN revision;
DROP TABLE identity.deletion_policy_proposals;
DROP TABLE identity.deletion_country_policies;
DROP TRIGGER account_deletion_country_capture ON identity.account_deletion_requests;
DROP FUNCTION identity.capture_deletion_country();
DROP TRIGGER account_deletion_country_immutable ON identity.account_deletion_requests;
DROP FUNCTION identity.guard_deletion_country();
ALTER TABLE identity.account_deletion_requests DROP COLUMN country;
RESET ROLE;
