SET ROLE planext4u_notification_owner;

-- This is a local partial scrub. Delivery content, provider receipts and
-- countryless preferences/consents remain for their own retention decisions.
CREATE TABLE notification.privacy_push_scrub_runs (
 request_id text PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL,
 subject_id text NOT NULL,
 policy_version text NOT NULL,
 device_rows integer NOT NULL CHECK(device_rows BETWEEN 0 AND 1000),
 delivery_rows integer NOT NULL CHECK(delivery_rows BETWEEN 0 AND 1000),
 occurred_at timestamptz NOT NULL
);
REVOKE ALL ON notification.privacy_push_scrub_runs FROM PUBLIC,planext4u_notification_runtime;

CREATE FUNCTION notification.reject_privacy_push_scrub_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN
 RAISE EXCEPTION 'notification push scrub evidence is append only';
END $$;
CREATE TRIGGER privacy_push_scrub_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON notification.privacy_push_scrub_runs FOR EACH STATEMENT
 EXECUTE FUNCTION notification.reject_privacy_push_scrub_mutation();
REVOKE ALL ON FUNCTION notification.reject_privacy_push_scrub_mutation() FROM PUBLIC;

-- A committed run permanently fences token restoration by normal writers.
-- Check both scopes so an UPDATE cannot move an old row out of the fence.
CREATE FUNCTION notification.guard_privacy_push_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,notification AS $$
BEGIN
 IF TG_OP='UPDATE' AND EXISTS(
  SELECT 1 FROM notification.privacy_push_scrub_runs run
  WHERE run.tenant_id=OLD.tenant_id AND run.country=OLD.country
    AND run.subject_id=OLD.subject_id
 ) THEN
  RAISE EXCEPTION 'deleted push device token cannot be restored';
 END IF;
 IF EXISTS(
  SELECT 1 FROM notification.privacy_push_scrub_runs run
  WHERE run.tenant_id=NEW.tenant_id AND run.country=NEW.country
    AND run.subject_id=NEW.subject_id
 ) THEN
  RAISE EXCEPTION 'deleted push device token cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_push_reintroduction BEFORE INSERT OR UPDATE
 ON notification.push_device FOR EACH ROW
 EXECUTE FUNCTION notification.guard_privacy_push_reintroduction();
REVOKE ALL ON FUNCTION notification.guard_privacy_push_reintroduction() FROM PUBLIC;

-- The durable fence also blocks new queue work. Existing provider receipts
-- remain writable for previously sent deliveries, while a suppressed delivery
-- cannot be made sendable again.
CREATE FUNCTION notification.guard_privacy_delivery_requeue() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,notification AS $$
DECLARE old_scrubbed boolean := false; new_scrubbed boolean := false;
BEGIN
 IF TG_OP='UPDATE' THEN
  SELECT EXISTS(SELECT 1 FROM notification.privacy_push_scrub_runs run
   WHERE run.tenant_id=OLD.tenant_id AND run.country=OLD.country
     AND run.subject_id=OLD.subject_id) INTO old_scrubbed;
 END IF;
 SELECT EXISTS(SELECT 1 FROM notification.privacy_push_scrub_runs run
  WHERE run.tenant_id=NEW.tenant_id AND run.country=NEW.country
    AND run.subject_id=NEW.subject_id) INTO new_scrubbed;
 IF TG_OP='INSERT' AND new_scrubbed THEN
  RAISE EXCEPTION 'deleted notification subject cannot be queued';
 END IF;
 IF TG_OP='UPDATE' AND (old_scrubbed OR new_scrubbed) THEN
  IF OLD.tenant_id<>NEW.tenant_id OR OLD.country<>NEW.country
     OR OLD.subject_id<>NEW.subject_id OR NEW.status IN ('QUEUED','SENDING')
     OR (OLD.status='SUPPRESSED' AND NEW.status<>OLD.status) THEN
   RAISE EXCEPTION 'deleted notification subject cannot be requeued';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_delivery_requeue BEFORE INSERT OR UPDATE
 ON notification.deliveries FOR EACH ROW
 EXECUTE FUNCTION notification.guard_privacy_delivery_requeue();
REVOKE ALL ON FUNCTION notification.guard_privacy_delivery_requeue() FROM PUBLIC;

CREATE FUNCTION notification.privacy_scrub_push_tokens(r text)
RETURNS TABLE(outcome text,evidence_reference text,device_rows integer,delivery_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,notification AS $$
DECLARE tenant text; subject text; policy text; governing_country text;
DECLARE prior notification.privacy_push_scrub_runs%ROWTYPE;
DECLARE affected integer := 0; suppressed integer := 0; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_notification_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR length(subject)=0 OR length(governing_country)<>2 THEN
  RAISE EXCEPTION 'notification push erasure scope unavailable';
 END IF;
 evidence := 'notification-push-scrub:'||r;
 -- The table lock covers the zero-row case and serializes queued device
 -- registrations with the durable trigger fence before commit.
 LOCK TABLE notification.push_device,notification.deliveries IN SHARE ROW EXCLUSIVE MODE;
 SELECT * INTO prior FROM notification.privacy_push_scrub_runs run
 WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant::uuid OR prior.country<>governing_country::char(2)
     OR prior.subject_id<>subject OR prior.policy_version<>policy THEN
   RAISE EXCEPTION 'notification push erasure retry scope changed';
  END IF;
  IF EXISTS(SELECT 1 FROM notification.push_device d
   WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
     AND d.subject_id=subject) THEN
   RAISE EXCEPTION 'notification push erasure retry found restored token';
  END IF;
  IF EXISTS(SELECT 1 FROM notification.deliveries d
   WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
     AND d.subject_id=subject AND d.status IN ('QUEUED','SENDING')) THEN
   RAISE EXCEPTION 'notification push erasure retry found sendable delivery';
  END IF;
  PERFORM identity.privacy_notification_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.device_rows,prior.delivery_rows;
  RETURN;
 END IF;
 IF EXISTS(SELECT 1 FROM notification.deliveries d
  WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
    AND d.subject_id=subject AND d.status='SENDING') THEN
  RAISE EXCEPTION 'notification push scrub waits for an in-flight delivery';
 END IF;
 SELECT count(*)::integer INTO affected FROM notification.push_device d
 WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
   AND d.subject_id=subject;
 IF affected>1000 THEN RAISE EXCEPTION 'notification push scrub requires a smaller batch'; END IF;
 SELECT count(*)::integer INTO suppressed FROM notification.deliveries d
 WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
   AND d.subject_id=subject AND d.status='QUEUED';
 IF suppressed>1000 THEN RAISE EXCEPTION 'notification push scrub requires a smaller batch'; END IF;
 DELETE FROM notification.push_device d
 WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
   AND d.subject_id=subject;
 UPDATE notification.deliveries d
 SET status='SUPPRESSED',suppression_reason='PRIVACY_ERASURE',claim_until=NULL,
     updated_at=clock_timestamp()
 WHERE d.tenant_id=tenant::uuid AND d.country=governing_country::char(2)
   AND d.subject_id=subject AND d.status='QUEUED';
 INSERT INTO notification.privacy_push_scrub_runs
 (request_id,tenant_id,country,subject_id,policy_version,device_rows,delivery_rows,occurred_at)
 VALUES(r,tenant::uuid,governing_country::char(2),subject,policy,affected,suppressed,clock_timestamp());
 PERFORM identity.privacy_notification_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,affected,suppressed;
END $$;
REVOKE ALL ON FUNCTION notification.privacy_scrub_push_tokens(text) FROM PUBLIC;
RESET ROLE;
