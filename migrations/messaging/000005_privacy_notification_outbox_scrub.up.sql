SET ROLE planext4u_messaging_owner;

-- Partial owner work: proven, unsent notification envelopes can leave the
-- outbox. Published rows, inbox history, broker copies and legacy events need
-- separate retention decisions, so the receipt is always RETAINED.
CREATE TABLE messaging.privacy_outbox_scrub_runs (
 request_id text PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 subject_id text NOT NULL,
 policy_version text NOT NULL,
 deleted_rows integer NOT NULL CHECK(deleted_rows BETWEEN 0 AND 1000),
 published_rows bigint NOT NULL CHECK(published_rows>=0),
 occurred_at timestamptz NOT NULL
);
CREATE INDEX messaging_privacy_outbox_scope_idx
 ON messaging.privacy_outbox_scrub_runs(tenant_id,country,subject_id);
REVOKE ALL ON messaging.privacy_outbox_scrub_runs FROM PUBLIC,planext4u_messaging_runtime;

-- The event ID tombstone prevents a deleted envelope from being reintroduced
-- under a different subject or country, even though outbox normally permits
-- runtime inserts. The tombstone contains no event payload.
CREATE TABLE messaging.privacy_outbox_scrubbed_events (
 event_id uuid PRIMARY KEY,
 request_id text NOT NULL REFERENCES messaging.privacy_outbox_scrub_runs(request_id),
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL,
 subject_id text NOT NULL,
 occurred_at timestamptz NOT NULL
);
CREATE INDEX messaging_privacy_outbox_events_request_idx
 ON messaging.privacy_outbox_scrubbed_events(request_id,event_id);
REVOKE ALL ON messaging.privacy_outbox_scrubbed_events FROM PUBLIC,planext4u_messaging_runtime;

CREATE FUNCTION messaging.reject_privacy_outbox_evidence_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,messaging AS $$
BEGIN
 RAISE EXCEPTION 'messaging privacy evidence is immutable';
END $$;
CREATE TRIGGER privacy_outbox_runs_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON messaging.privacy_outbox_scrub_runs FOR EACH STATEMENT
 EXECUTE FUNCTION messaging.reject_privacy_outbox_evidence_mutation();
CREATE TRIGGER privacy_outbox_events_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON messaging.privacy_outbox_scrubbed_events FOR EACH STATEMENT
 EXECUTE FUNCTION messaging.reject_privacy_outbox_evidence_mutation();
REVOKE ALL ON FUNCTION messaging.reject_privacy_outbox_evidence_mutation() FROM PUBLIC;

CREATE FUNCTION messaging.guard_privacy_outbox_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,messaging AS $$
DECLARE old_scrubbed boolean := false; new_scrubbed boolean := false;
BEGIN
 IF TG_OP='INSERT' AND EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrubbed_events tombstone
  WHERE tombstone.event_id=NEW.event_id) THEN
  RAISE EXCEPTION 'scrubbed outbox event ID cannot be reused';
 END IF;
 IF TG_OP='UPDATE' THEN
  SELECT EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrub_runs run
   WHERE run.tenant_id=OLD.tenant_id AND run.country=OLD.privacy_country
     AND lower(run.subject_id)=lower(OLD.privacy_subject_id)) INTO old_scrubbed;
  -- Legacy notification events cannot be requeued after any tenant scrub;
  -- their missing subject/country metadata cannot authorize a narrower read.
  IF OLD.tenant_id=NEW.tenant_id AND NEW.state IN ('PENDING','LEASED')
     AND (OLD.privacy_subject_id IS NULL OR OLD.privacy_country IS NULL)
     AND (OLD.aggregate_type='notification-delivery'
          OR OLD.envelope->>'producer'='notification-service'
          OR OLD.envelope->>'event_type'=('planext4u.'||'notification'||'.delivery.requested.v1'))
     AND EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrub_runs run
      WHERE run.tenant_id=OLD.tenant_id) THEN
   RAISE EXCEPTION 'unclassified legacy notification event cannot be requeued';
  END IF;
 END IF;
 SELECT EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrub_runs run
  WHERE run.tenant_id=NEW.tenant_id AND run.country=NEW.privacy_country
    AND lower(run.subject_id)=lower(NEW.privacy_subject_id)) INTO new_scrubbed;
 IF TG_OP='INSERT' AND new_scrubbed THEN
  RAISE EXCEPTION 'deleted messaging subject cannot be queued';
 END IF;
 IF TG_OP='UPDATE' AND (old_scrubbed OR new_scrubbed)
    AND NEW.state IN ('PENDING','LEASED') THEN
  RAISE EXCEPTION 'deleted messaging subject cannot be requeued';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_outbox_reintroduction BEFORE INSERT OR UPDATE
 ON messaging.outbox FOR EACH ROW
 EXECUTE FUNCTION messaging.guard_privacy_outbox_reintroduction();
REVOKE ALL ON FUNCTION messaging.guard_privacy_outbox_reintroduction() FROM PUBLIC;

-- A dead-lettered event may have reached the broker before the failed
-- acknowledgment. Keep an inbox consumer from processing either a deleted
-- event ID or a retained scoped/legacy notification after the scrub commits.
CREATE FUNCTION messaging.guard_privacy_inbox_reprocessing() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,messaging AS $$
BEGIN
 IF TG_OP='INSERT' OR NEW.state='PROCESSING' THEN
  IF EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrubbed_events tombstone
   WHERE tombstone.event_id=NEW.event_id)
     OR EXISTS(SELECT 1 FROM messaging.outbox event
      JOIN messaging.privacy_outbox_scrub_runs run ON run.tenant_id=event.tenant_id
      WHERE event.event_id=NEW.event_id
        AND ((event.privacy_country=run.country
              AND lower(event.privacy_subject_id)=lower(run.subject_id))
             OR ((event.privacy_subject_id IS NULL OR event.privacy_country IS NULL)
                 AND (event.aggregate_type='notification-delivery'
                      OR event.envelope->>'producer'='notification-service'
                      OR event.envelope->>'event_type'=('planext4u.'||'notification'||'.delivery.requested.v1'))))) THEN
   RAISE EXCEPTION 'scrubbed or unclassified notification cannot be reprocessed';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_inbox_reprocessing BEFORE INSERT OR UPDATE
 ON messaging.inbox FOR EACH ROW
 EXECUTE FUNCTION messaging.guard_privacy_inbox_reprocessing();
REVOKE ALL ON FUNCTION messaging.guard_privacy_inbox_reprocessing() FROM PUBLIC;

CREATE FUNCTION messaging.privacy_scrub_notification_outbox(r text)
RETURNS TABLE(outcome text,evidence_reference text,deleted_rows integer,published_rows bigint)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,messaging AS $$
DECLARE tenant text; subject text; policy text; governing_country text;
DECLARE prior messaging.privacy_outbox_scrub_runs%ROWTYPE;
DECLARE existing boolean := false;
DECLARE to_delete bigint := 0; published bigint := 0; changed bigint := 0;
DECLARE evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_messaging_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR NOT pg_catalog.pg_input_is_valid(subject,'uuid')
    OR governing_country !~ '^[A-Z]{2}$' THEN
  RAISE EXCEPTION 'messaging outbox erasure scope unavailable';
 END IF;
 evidence := 'messaging-notification-outbox-scrub:'||r;
 -- A table lock covers the zero-row case and serializes a concurrent writer
 -- or worker claim before the durable reintroduction fence commits.
 LOCK TABLE messaging.outbox,messaging.inbox IN SHARE ROW EXCLUSIVE MODE;
 SELECT * INTO prior FROM messaging.privacy_outbox_scrub_runs run
 WHERE run.request_id=r FOR UPDATE;
 existing := FOUND;
 IF existing AND (prior.tenant_id<>tenant::uuid OR prior.country<>governing_country::char(2)
   OR lower(prior.subject_id)<>lower(subject) OR prior.policy_version<>policy) THEN
  RAISE EXCEPTION 'messaging outbox scrub retry scope changed';
 END IF;
 -- Historical notification envelopes without independently owned subject
 -- metadata cannot be assigned to this account. Refuse while any such event
 -- may still be dispatched; published copies remain covered by RETAINED.
 IF EXISTS(SELECT 1 FROM messaging.outbox event
  WHERE event.tenant_id=tenant::uuid
    AND (event.privacy_subject_id IS NULL OR event.privacy_country IS NULL)
    AND (event.aggregate_type='notification-delivery'
         OR event.envelope->>'producer'='notification-service'
         OR event.envelope->>'event_type'=('planext4u.'||'notification'||'.delivery.requested.v1'))
    AND event.state IN ('PENDING','LEASED','DEAD_LETTER')) THEN
  RAISE EXCEPTION 'unclassified legacy notification events require quarantine';
 END IF;
 IF EXISTS(SELECT 1 FROM messaging.outbox event
  WHERE event.tenant_id=tenant::uuid AND event.privacy_country=governing_country::char(2)
    AND lower(event.privacy_subject_id)=lower(subject) AND event.state='LEASED') THEN
  RAISE EXCEPTION 'messaging outbox scrub waits for an in-flight publisher';
 END IF;
 IF EXISTS(SELECT 1 FROM messaging.inbox inbox
  JOIN messaging.outbox event ON event.event_id=inbox.event_id
  WHERE event.tenant_id=tenant::uuid AND inbox.state='PROCESSING'
    AND ((event.privacy_country=governing_country::char(2)
          AND lower(event.privacy_subject_id)=lower(subject))
         OR ((event.privacy_subject_id IS NULL OR event.privacy_country IS NULL)
             AND (event.aggregate_type='notification-delivery'
                  OR event.envelope->>'producer'='notification-service'
                  OR event.envelope->>'event_type'=('planext4u.'||'notification'||'.delivery.requested.v1'))))) THEN
  RAISE EXCEPTION 'messaging outbox scrub waits for an in-flight consumer';
 END IF;
 IF existing THEN
  IF EXISTS(SELECT 1 FROM messaging.outbox event
   WHERE event.tenant_id=tenant::uuid AND event.privacy_country=governing_country::char(2)
     AND lower(event.privacy_subject_id)=lower(subject)
     AND event.state IN ('PENDING','LEASED','DEAD_LETTER'))
     OR (SELECT count(*) FROM messaging.privacy_outbox_scrubbed_events tombstone
         WHERE tombstone.request_id=r)<>prior.deleted_rows THEN
   RAISE EXCEPTION 'messaging outbox scrub retry found restored work or lost evidence';
  END IF;
  PERFORM identity.privacy_messaging_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.deleted_rows,prior.published_rows;
  RETURN;
 END IF;
 SELECT count(*) INTO to_delete FROM messaging.outbox event
 WHERE event.tenant_id=tenant::uuid AND event.privacy_country=governing_country::char(2)
   AND lower(event.privacy_subject_id)=lower(subject)
   AND event.state IN ('PENDING','DEAD_LETTER');
 IF to_delete>1000 THEN RAISE EXCEPTION 'messaging outbox scrub requires a smaller batch'; END IF;
 SELECT count(*) INTO published FROM messaging.outbox event
 WHERE event.tenant_id=tenant::uuid AND event.privacy_country=governing_country::char(2)
   AND lower(event.privacy_subject_id)=lower(subject) AND event.state='PUBLISHED';
 INSERT INTO messaging.privacy_outbox_scrub_runs
 (request_id,tenant_id,country,subject_id,policy_version,deleted_rows,published_rows,occurred_at)
 VALUES(r,tenant::uuid,governing_country::char(2),subject,policy,to_delete::integer,published,clock_timestamp());
 INSERT INTO messaging.privacy_outbox_scrubbed_events
 (event_id,request_id,tenant_id,country,subject_id,occurred_at)
 SELECT event.event_id,r,tenant::uuid,governing_country::char(2),subject,clock_timestamp()
 FROM messaging.outbox event
 WHERE event.tenant_id=tenant::uuid AND event.privacy_country=governing_country::char(2)
   AND lower(event.privacy_subject_id)=lower(subject)
   AND event.state IN ('PENDING','DEAD_LETTER');
 GET DIAGNOSTICS changed=ROW_COUNT;
 IF changed<>to_delete THEN RAISE EXCEPTION 'messaging outbox tombstones were incomplete'; END IF;
 DELETE FROM messaging.outbox event
 WHERE event.tenant_id=tenant::uuid AND event.privacy_country=governing_country::char(2)
   AND lower(event.privacy_subject_id)=lower(subject)
   AND event.state IN ('PENDING','DEAD_LETTER');
 GET DIAGNOSTICS changed=ROW_COUNT;
 IF changed<>to_delete THEN RAISE EXCEPTION 'messaging outbox deletion was incomplete'; END IF;
 PERFORM identity.privacy_messaging_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,to_delete::integer,published;
END $$;
REVOKE ALL ON FUNCTION messaging.privacy_scrub_notification_outbox(text) FROM PUBLIC;
RESET ROLE;
