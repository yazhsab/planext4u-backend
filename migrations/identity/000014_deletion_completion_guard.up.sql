SET ROLE planext4u_identity_owner;

-- Every current data owner must make a recorded decision. A new service must
-- add its owner here before it can participate in terminal erasure.
CREATE TABLE identity.deletion_owner_requirements (
 owner text PRIMARY KEY CHECK(owner ~ '^[a-z_]+$')
);
INSERT INTO identity.deletion_owner_requirements(owner) VALUES
 ('identity'),('audit'),('catalog'),('commerce'),('inventory'),('payment'),
 ('ordering'),('booking'),('supply'),('food'),('fulfillment'),('social'),
 ('governance'),('local_verticals'),('emergency'),('wallet'),('support'),
 ('configuration'),('admin'),('customer_web'),('bankverification'),('media'),
 ('messaging'),('notification');

-- No retention period is seeded. Each tenant/owner must configure a reviewed
-- policy before a deletion can finish; zero is allowed only when explicit.
CREATE TABLE identity.deletion_retention_policies (
 tenant_id text NOT NULL,
 owner text NOT NULL REFERENCES identity.deletion_owner_requirements(owner),
 policy_version text NOT NULL CHECK(length(policy_version) BETWEEN 1 AND 128),
 retention_interval interval NOT NULL CHECK(retention_interval >= interval '0'),
 configured_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,owner)
);
CREATE TABLE identity.deletion_owner_receipts (
 request_id text NOT NULL REFERENCES identity.account_deletion_requests(id),
 owner text NOT NULL REFERENCES identity.deletion_owner_requirements(owner),
 outcome text NOT NULL CHECK(outcome IN ('ERASED','NO_DATA','RETAINED')),
 policy_version text NOT NULL CHECK(length(policy_version) BETWEEN 1 AND 128),
 evidence_reference text NOT NULL CHECK(length(evidence_reference) BETWEEN 1 AND 256),
 attested_at timestamptz NOT NULL,
 PRIMARY KEY(request_id,owner)
);
CREATE TABLE identity.deletion_legal_holds (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 request_id text NOT NULL REFERENCES identity.account_deletion_requests(id),
 owner text NOT NULL REFERENCES identity.deletion_owner_requirements(owner),
 reason_reference text NOT NULL CHECK(length(reason_reference) BETWEEN 1 AND 256),
 recorded_at timestamptz NOT NULL,
 released_at timestamptz,
 CHECK(released_at IS NULL OR released_at>=recorded_at)
);
CREATE INDEX deletion_legal_holds_active_idx ON identity.deletion_legal_holds(request_id)
 WHERE released_at IS NULL;

CREATE FUNCTION identity.guard_deletion_completed() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE tenant text;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF OLD.status IN ('CANCELLED','COMPLETED') AND NEW.status<>OLD.status THEN
   RAISE EXCEPTION 'deletion terminal state cannot change';
  END IF;
 END IF;
 IF NEW.status<>'COMPLETED' THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' THEN
  IF OLD.status<>'SCHEDULED' THEN
   RAISE EXCEPTION 'deletion must be scheduled before completion';
  END IF;
 END IF;
 IF clock_timestamp()<NEW.effective_at THEN
  RAISE EXCEPTION 'deletion recovery period has not elapsed';
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
 ) THEN
  RAISE EXCEPTION 'deletion lacks current retention policy and owner erasure evidence';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION identity.guard_deletion_completed() FROM PUBLIC;
CREATE TRIGGER account_deletion_completion_guard
 BEFORE INSERT OR UPDATE OF status ON identity.account_deletion_requests
 FOR EACH ROW EXECUTE FUNCTION identity.guard_deletion_completed();

-- The application runtime receives no write grants to the policy, hold or
-- receipt tables. Their eventual writer must be separately authenticated and
-- audited by the erasure orchestration service.
RESET ROLE;
