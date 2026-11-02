SET ROLE planext4u_customer_web_owner;

CREATE INDEX customer_web_sessions_subject_idx
 ON customer_web.sessions(tenant_id,country,identity_id) WHERE NOT is_guest;

-- This proves only local browser-session credential removal. Identity,
-- financial, audit and backup retention require separate owner evidence.
CREATE TABLE customer_web.privacy_session_scrub_runs (
 request_id text PRIMARY KEY,
 tenant_id text NOT NULL,
 country char(2) NOT NULL,
 subject_id text NOT NULL,
 policy_version text NOT NULL,
 session_rows bigint NOT NULL CHECK(session_rows>=0),
 occurred_at timestamptz NOT NULL
);
CREATE INDEX customer_web_privacy_session_scope_idx
 ON customer_web.privacy_session_scrub_runs(tenant_id,country,subject_id);
REVOKE ALL ON customer_web.privacy_session_scrub_runs FROM PUBLIC,planext4u_customer_web_runtime;

CREATE FUNCTION customer_web.reject_privacy_session_scrub_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN
 RAISE EXCEPTION 'customer web session scrub evidence is append only';
END $$;
CREATE TRIGGER privacy_session_scrub_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON customer_web.privacy_session_scrub_runs FOR EACH STATEMENT
 EXECUTE FUNCTION customer_web.reject_privacy_session_scrub_mutation();
REVOKE ALL ON FUNCTION customer_web.reject_privacy_session_scrub_mutation() FROM PUBLIC;

CREATE FUNCTION customer_web.guard_privacy_session_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,customer_web AS $$
BEGIN
 IF TG_OP='UPDATE' AND EXISTS(
  SELECT 1 FROM customer_web.privacy_session_scrub_runs run
  WHERE run.tenant_id=OLD.tenant_id AND run.country=OLD.country
    AND run.subject_id=OLD.identity_id
 ) THEN
  RAISE EXCEPTION 'deleted customer web session cannot be restored';
 END IF;
 IF EXISTS(
  SELECT 1 FROM customer_web.privacy_session_scrub_runs run
  WHERE run.tenant_id=NEW.tenant_id AND run.country=NEW.country
    AND run.subject_id=NEW.identity_id
 ) THEN
  RAISE EXCEPTION 'deleted customer web session cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_session_reintroduction BEFORE INSERT OR UPDATE
 ON customer_web.sessions FOR EACH ROW
 EXECUTE FUNCTION customer_web.guard_privacy_session_reintroduction();
REVOKE ALL ON FUNCTION customer_web.guard_privacy_session_reintroduction() FROM PUBLIC;

CREATE FUNCTION customer_web.privacy_scrub_sessions(r text)
RETURNS TABLE(outcome text,evidence_reference text,session_rows bigint)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,customer_web AS $$
DECLARE tenant text; subject text; policy text; governing_country text;
DECLARE prior customer_web.privacy_session_scrub_runs%ROWTYPE;
DECLARE affected bigint := 0; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_customer_web_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR length(tenant) NOT BETWEEN 1 AND 128 OR length(subject) NOT BETWEEN 1 AND 128
    OR length(governing_country)<>2 THEN
  RAISE EXCEPTION 'customer web session erasure scope unavailable';
 END IF;
 evidence := 'customer-web-session-scrub:'||r;
 -- Includes the zero-row case and serializes registration/replacement with
 -- the durable reintroduction trigger before the receipt can commit.
 LOCK TABLE customer_web.sessions IN SHARE ROW EXCLUSIVE MODE;
 SELECT * INTO prior FROM customer_web.privacy_session_scrub_runs run
 WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant OR prior.country<>governing_country::char(2)
     OR prior.subject_id<>subject OR prior.policy_version<>policy THEN
   RAISE EXCEPTION 'customer web session scrub retry scope changed';
  END IF;
  IF EXISTS(SELECT 1 FROM customer_web.sessions s
   WHERE s.tenant_id=tenant AND s.country=governing_country::char(2)
     AND s.identity_id=subject AND NOT s.is_guest) THEN
   RAISE EXCEPTION 'customer web session scrub retry found restored credentials';
  END IF;
  PERFORM identity.privacy_customer_web_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.session_rows;
  RETURN;
 END IF;
 SELECT count(*) INTO affected FROM customer_web.sessions s
 WHERE s.tenant_id=tenant AND s.country=governing_country::char(2)
   AND s.identity_id=subject AND NOT s.is_guest;
 DELETE FROM customer_web.sessions s
 WHERE s.tenant_id=tenant AND s.country=governing_country::char(2)
   AND s.identity_id=subject AND NOT s.is_guest;
 INSERT INTO customer_web.privacy_session_scrub_runs
 (request_id,tenant_id,country,subject_id,policy_version,session_rows,occurred_at)
 VALUES(r,tenant,governing_country::char(2),subject,policy,affected,clock_timestamp());
 PERFORM identity.privacy_customer_web_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,affected;
END $$;
REVOKE ALL ON FUNCTION customer_web.privacy_scrub_sessions(text) FROM PUBLIC;
RESET ROLE;
