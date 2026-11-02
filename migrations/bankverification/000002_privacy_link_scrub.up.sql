SET ROLE planext4u_bankverification_owner;

-- The owner still retains append-only audit and any provider-side records.
-- This table records a bounded local scrub, never terminal erasure.
CREATE TABLE bankverification.privacy_link_scrub_runs (
 request_id text PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL,
 owner_id uuid NOT NULL,
 policy_version text NOT NULL,
 attempt_rows integer NOT NULL CHECK(attempt_rows BETWEEN 0 AND 1000),
 occurred_at timestamptz NOT NULL
);
CREATE FUNCTION bankverification.reject_privacy_scrub_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'bank link privacy scrub is append only'; END $$;
CREATE TRIGGER privacy_link_scrub_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON bankverification.privacy_link_scrub_runs FOR EACH STATEMENT
 EXECUTE FUNCTION bankverification.reject_privacy_scrub_mutation();

-- A completed local scrub is a durable fence for bank proof reintroduction.
CREATE FUNCTION bankverification.guard_privacy_link_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,bankverification AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF EXISTS(SELECT 1 FROM bankverification.privacy_link_scrub_runs run
   WHERE run.tenant_id=OLD.tenant_id AND run.country=OLD.country
     AND run.owner_id=OLD.owner_id) THEN
   RAISE EXCEPTION 'deleted bank link proof cannot be restored';
  END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM bankverification.privacy_link_scrub_runs run
  WHERE run.tenant_id=NEW.tenant_id AND run.country=NEW.country
    AND run.owner_id=NEW.owner_id) THEN
  RAISE EXCEPTION 'deleted bank link proof cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_link_reintroduction BEFORE INSERT OR UPDATE
 ON bankverification.link_attempts FOR EACH ROW
 EXECUTE FUNCTION bankverification.guard_privacy_link_reintroduction();
REVOKE ALL ON FUNCTION bankverification.guard_privacy_link_reintroduction() FROM PUBLIC;

CREATE FUNCTION bankverification.privacy_scrub_link_attempts(r text)
RETURNS TABLE(outcome text,evidence_reference text,attempt_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,bankverification AS $$
DECLARE tenant text; subject text; policy text; governing_country text;
DECLARE prior bankverification.privacy_link_scrub_runs%ROWTYPE;
DECLARE targets uuid[] := ARRAY[]::uuid[]; affected integer := 0;
DECLARE evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_bankverification_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR NOT pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  RAISE EXCEPTION 'bank link erasure scope unavailable';
 END IF;
 evidence := 'bankverification-link-scrub:'||r;
 -- Serialize direct inserts and updates with the snapshot and durable fence.
 LOCK TABLE bankverification.link_attempts IN SHARE ROW EXCLUSIVE MODE;
 SELECT * INTO prior FROM bankverification.privacy_link_scrub_runs run
 WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant::uuid OR prior.country<>governing_country::char(2)
     OR prior.owner_id<>subject::uuid OR prior.policy_version<>policy THEN
   RAISE EXCEPTION 'bank link erasure retry scope changed';
  END IF;
  IF (SELECT count(*) FROM bankverification.link_attempts l
      WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
        AND l.owner_id=subject::uuid)<>prior.attempt_rows
   OR EXISTS(SELECT 1 FROM bankverification.link_attempts l
      WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
        AND l.owner_id=subject::uuid
        AND (l.status<>'EXPIRED' OR l.provider_session_id IS NOT NULL
         OR l.account IS NOT NULL OR l.verification_id IS NOT NULL
         OR l.verified_at IS NOT NULL
         OR l.idempotency_hash<>(md5(l.id::text)||md5(r||l.id::text)))) THEN
   RAISE EXCEPTION 'bank link erasure retry found reintroduced proof';
  END IF;
  PERFORM identity.privacy_bankverification_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.attempt_rows;
  RETURN;
 END IF;
 SELECT coalesce(array_agg(a.id),ARRAY[]::uuid[]) INTO targets
 FROM (SELECT l.id FROM bankverification.link_attempts l
       WHERE l.tenant_id=tenant::uuid AND l.country=governing_country::char(2)
         AND l.owner_id=subject::uuid
       ORDER BY l.id LIMIT 1001 FOR UPDATE) a;
 affected := cardinality(targets);
 IF affected>1000 THEN RAISE EXCEPTION 'bank link scrub requires a smaller batch'; END IF;
 -- Remove local hosted-link proof and idempotency correlation. The attempt
 -- identifiers and immutable audit remain for separately governed retention.
 UPDATE bankverification.link_attempts l
 SET status='EXPIRED',provider_session_id=NULL,account=NULL,
     verification_id=NULL,verified_at=NULL,
     idempotency_hash=md5(l.id::text)||md5(r||l.id::text),
     updated_at=clock_timestamp(),revision=l.revision+1
 WHERE l.id=ANY(targets);
 INSERT INTO bankverification.privacy_link_scrub_runs
 (request_id,tenant_id,country,owner_id,policy_version,attempt_rows,occurred_at)
 VALUES(r,tenant::uuid,governing_country::char(2),subject::uuid,policy,affected,clock_timestamp());
 PERFORM identity.privacy_bankverification_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,affected;
END $$;
REVOKE ALL ON FUNCTION bankverification.privacy_scrub_link_attempts(text) FROM PUBLIC;
RESET ROLE;
