SET ROLE planext4u_identity_owner;

-- New requests record their governing country. Historical requests without a
-- trustworthy country stay NULL and cannot advance through the new gate.
ALTER TABLE identity.account_deletion_requests ADD COLUMN country char(2)
 CHECK(country ~ '^[A-Z]{2}$');
CREATE FUNCTION identity.capture_deletion_country() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE source_session text; source_row record;
BEGIN
 IF NEW.country IS NOT NULL THEN RETURN NEW; END IF;
 source_session:=nullif(current_setting('planext4u.deletion_session',true),'');
 IF source_session IS NULL THEN RETURN NEW; END IF;
 SELECT s.identity_id,s.tenant_id,s.country,s.revoked_at,s.refresh_expires_at,
        i.tenant_id AS account_tenant INTO source_row
 FROM identity.sessions s JOIN identity.identities i ON i.id=s.identity_id
 WHERE s.id=source_session FOR SHARE OF s,i;
 IF NOT FOUND OR source_row.identity_id<>NEW.identity_id
    OR source_row.tenant_id<>source_row.account_tenant
    OR source_row.revoked_at IS NOT NULL OR source_row.refresh_expires_at<=clock_timestamp() THEN
  RAISE EXCEPTION 'deletion source session is invalid';
 END IF;
 NEW.country:=source_row.country;
 RETURN NEW;
END $$;
CREATE TRIGGER account_deletion_country_capture
 BEFORE INSERT ON identity.account_deletion_requests
 FOR EACH ROW EXECUTE FUNCTION identity.capture_deletion_country();
REVOKE ALL ON FUNCTION identity.capture_deletion_country() FROM PUBLIC;
CREATE FUNCTION identity.guard_deletion_country() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
BEGIN
 IF NEW.country IS DISTINCT FROM OLD.country THEN
  RAISE EXCEPTION 'deletion governing country is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER account_deletion_country_immutable
 BEFORE UPDATE OF country ON identity.account_deletion_requests
 FOR EACH ROW EXECUTE FUNCTION identity.guard_deletion_country();
REVOKE ALL ON FUNCTION identity.guard_deletion_country() FROM PUBLIC;

-- The older tenant-only rows are preserved as historical data, never treated
-- as an active country policy. No jurisdiction or retention value is seeded.
CREATE TABLE identity.deletion_country_policies (
 tenant_id text NOT NULL,
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 owner text NOT NULL REFERENCES identity.deletion_owner_requirements(owner),
 revision bigint NOT NULL CHECK(revision>0),
 policy_version text NOT NULL CHECK(length(policy_version) BETWEEN 1 AND 128),
 retention_interval interval NOT NULL CHECK(retention_interval>=interval '0'),
 published_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,country,owner)
);
CREATE TABLE identity.deletion_policy_proposals (
 id uuid PRIMARY KEY,
 tenant_id text NOT NULL,
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 owner text NOT NULL REFERENCES identity.deletion_owner_requirements(owner),
 expected_revision bigint NOT NULL CHECK(expected_revision>=0),
 policy_version text NOT NULL CHECK(length(policy_version) BETWEEN 1 AND 128),
 retention_interval interval NOT NULL CHECK(retention_interval>=interval '0'),
 reason_reference text NOT NULL CHECK(length(reason_reference) BETWEEN 8 AND 256),
 status text NOT NULL CHECK(status IN ('PENDING','PUBLISHED')),
 requested_by uuid NOT NULL,
 requested_at timestamptz NOT NULL,
 approved_by uuid,
 published_at timestamptz,
 CHECK((status='PENDING' AND approved_by IS NULL AND published_at IS NULL)
    OR (status='PUBLISHED' AND approved_by IS NOT NULL AND published_at IS NOT NULL))
);
CREATE INDEX deletion_policy_proposals_scope_idx
 ON identity.deletion_policy_proposals(tenant_id,country,owner,requested_at DESC);
ALTER TABLE identity.deletion_legal_holds
 ADD COLUMN recorded_by uuid,
 ADD COLUMN released_by uuid,
 ADD COLUMN release_reason_reference text CHECK(length(release_reason_reference) BETWEEN 8 AND 256),
 ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK(revision>0);
CREATE TABLE identity.deletion_governance_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id text NOT NULL,
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 actor_id uuid NOT NULL,
 actor_session_id uuid NOT NULL,
 action text NOT NULL CHECK(action IN ('POLICY_PROPOSED','POLICY_PUBLISHED','HOLD_PLACED','HOLD_RELEASED')),
 target_reference text NOT NULL,
 reason_reference text NOT NULL CHECK(length(reason_reference) BETWEEN 8 AND 256),
 revision bigint NOT NULL CHECK(revision>0),
 recorded_at timestamptz NOT NULL
);
CREATE INDEX deletion_governance_events_scope_idx
 ON identity.deletion_governance_events(tenant_id,country,id);

-- A hold takes the request lock before becoming visible. This serializes hold
-- placement with completion and owner scrub, including a due-time race.
CREATE FUNCTION identity.guard_deletion_hold_placement() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,identity AS $$
DECLARE current_status text;
BEGIN
 SELECT status INTO current_status FROM identity.account_deletion_requests
 WHERE id=NEW.request_id FOR UPDATE;
 IF current_status IS DISTINCT FROM 'SCHEDULED' THEN
  RAISE EXCEPTION 'legal hold requires a scheduled deletion';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER deletion_hold_placement_guard
 BEFORE INSERT ON identity.deletion_legal_holds
 FOR EACH ROW EXECUTE FUNCTION identity.guard_deletion_hold_placement();
REVOKE ALL ON FUNCTION identity.guard_deletion_hold_placement() FROM PUBLIC;

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
 JOIN identity.deletion_country_policies p ON p.tenant_id=i.tenant_id AND p.country=d.country AND p.owner=o
 WHERE d.id=r AND d.status='SCHEDULED' AND d.country IS NOT NULL
   AND d.effective_at<=clock_timestamp()
   AND d.effective_at+p.retention_interval<=clock_timestamp()
   AND NOT EXISTS(SELECT 1 FROM identity.deletion_legal_holds h
                  WHERE h.request_id=d.id AND h.released_at IS NULL)
 FOR UPDATE OF d,task,p;
 IF NOT FOUND THEN RAISE EXCEPTION 'owner erasure is not ready'; END IF;
END $$;

GRANT SELECT ON identity.deletion_country_policies TO planext4u_identity_runtime;
RESET ROLE;
