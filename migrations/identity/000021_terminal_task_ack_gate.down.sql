SET ROLE planext4u_identity_owner;
DROP FUNCTION identity.deletion_terminal_task_ack_gate_version();
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
 IF NEW.country IS NULL OR clock_timestamp()<NEW.effective_at THEN
  RAISE EXCEPTION 'deletion country or recovery period is unavailable';
 END IF;
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
  LEFT JOIN identity.deletion_country_policies policy
    ON policy.tenant_id=tenant AND policy.country=NEW.country AND policy.owner=required.owner
  LEFT JOIN identity.deletion_owner_receipts receipt
    ON receipt.request_id=NEW.id AND receipt.owner=required.owner
  WHERE policy.owner IS NULL OR receipt.owner IS NULL
     OR receipt.policy_version<>policy.policy_version
     OR receipt.outcome NOT IN ('ERASED','NO_DATA')
     OR receipt.attested_at<NEW.effective_at
     OR receipt.attested_at>clock_timestamp()
     OR clock_timestamp()<NEW.effective_at+policy.retention_interval
 ) THEN
  RAISE EXCEPTION 'deletion lacks current country policy and owner erasure evidence';
 END IF;
 RETURN NEW;
END $$;
RESET ROLE;
