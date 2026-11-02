SET ROLE planext4u_social_owner;

-- This is a partial social-owner scrub. Durable posts, messages, moderation,
-- reward and media evidence remain subject to separate retention decisions.
CREATE TABLE social.privacy_ephemeral_erasure_runs (
 request_id text PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL,
 subject_id uuid NOT NULL,
 policy_version text NOT NULL,
 presence_rows integer NOT NULL CHECK(presence_rows BETWEEN 0 AND 1000),
 call_rows integer NOT NULL CHECK(call_rows BETWEEN 0 AND 1000),
 signal_rows integer NOT NULL CHECK(signal_rows BETWEEN 0 AND 1000),
 occurred_at timestamptz NOT NULL
);
CREATE INDEX social_privacy_ephemeral_scope_idx
 ON social.privacy_ephemeral_erasure_runs(tenant_id,country,subject_id);
REVOKE ALL ON social.privacy_ephemeral_erasure_runs FROM PUBLIC,planext4u_social_runtime;

CREATE FUNCTION social.reject_privacy_ephemeral_run_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,social AS $$
BEGIN
 RAISE EXCEPTION 'social ephemeral privacy evidence is immutable';
END $$;
CREATE TRIGGER privacy_ephemeral_run_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON social.privacy_ephemeral_erasure_runs FOR EACH STATEMENT
 EXECUTE FUNCTION social.reject_privacy_ephemeral_run_mutation();
REVOKE ALL ON FUNCTION social.reject_privacy_ephemeral_run_mutation() FROM PUBLIC;

CREATE FUNCTION social.guard_privacy_presence_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,social AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM social.privacy_ephemeral_erasure_runs run
  WHERE run.tenant_id=NEW.tenant_id AND run.country=NEW.country
    AND run.subject_id=NEW.profile_identity_id)
 OR (TG_OP='UPDATE' AND EXISTS(
  SELECT 1 FROM social.privacy_ephemeral_erasure_runs run
  WHERE run.tenant_id=OLD.tenant_id AND run.country=OLD.country
    AND run.subject_id=OLD.profile_identity_id)) THEN
  RAISE EXCEPTION 'deleted social presence cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_presence_reintroduction BEFORE INSERT OR UPDATE ON social.presence
 FOR EACH ROW EXECUTE FUNCTION social.guard_privacy_presence_reintroduction();
REVOKE ALL ON FUNCTION social.guard_privacy_presence_reintroduction() FROM PUBLIC;

-- A conversation's participant set is an immutable privacy boundary for its
-- calls. Status changes remain possible; identity reassignment does not.
CREATE FUNCTION social.guard_conversation_participants_immutable() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,social AS $$
BEGIN
 IF NEW.participant_ids IS DISTINCT FROM OLD.participant_ids
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.country IS DISTINCT FROM OLD.country THEN
  RAISE EXCEPTION 'social conversation participants are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER conversation_participants_immutable BEFORE UPDATE ON social.conversations
 FOR EACH ROW EXECUTE FUNCTION social.guard_conversation_participants_immutable();
REVOKE ALL ON FUNCTION social.guard_conversation_participants_immutable() FROM PUBLIC;

CREATE FUNCTION social.guard_privacy_call_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,social AS $$
BEGIN
 IF EXISTS(
  SELECT 1 FROM social.conversations conversation
  JOIN social.privacy_ephemeral_erasure_runs run
   ON run.tenant_id=conversation.tenant_id AND run.country=conversation.country
   AND run.subject_id=ANY(conversation.participant_ids)
  WHERE conversation.id=NEW.conversation_id AND conversation.tenant_id=NEW.tenant_id
    AND conversation.country=NEW.country
 ) OR (TG_OP='UPDATE' AND EXISTS(
  SELECT 1 FROM social.conversations conversation
  JOIN social.privacy_ephemeral_erasure_runs run
   ON run.tenant_id=conversation.tenant_id AND run.country=conversation.country
   AND run.subject_id=ANY(conversation.participant_ids)
  WHERE conversation.id=OLD.conversation_id AND conversation.tenant_id=OLD.tenant_id
    AND conversation.country=OLD.country
 )) THEN
  RAISE EXCEPTION 'deleted social call cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_call_reintroduction BEFORE INSERT OR UPDATE ON social.call_sessions
 FOR EACH ROW EXECUTE FUNCTION social.guard_privacy_call_reintroduction();
REVOKE ALL ON FUNCTION social.guard_privacy_call_reintroduction() FROM PUBLIC;

CREATE FUNCTION social.guard_privacy_call_signal_reintroduction() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,social AS $$
BEGIN
 IF EXISTS(
  SELECT 1 FROM social.call_sessions call
  JOIN social.conversations conversation ON conversation.id=call.conversation_id
  JOIN social.privacy_ephemeral_erasure_runs run
   ON run.tenant_id=call.tenant_id AND run.country=call.country
   AND (run.subject_id=NEW.sender_identity_id OR run.subject_id=ANY(conversation.participant_ids))
  WHERE call.id=NEW.call_id AND call.tenant_id=NEW.tenant_id AND call.country=NEW.country
 ) OR (TG_OP='UPDATE' AND EXISTS(
  SELECT 1 FROM social.call_sessions call
  JOIN social.conversations conversation ON conversation.id=call.conversation_id
  JOIN social.privacy_ephemeral_erasure_runs run
   ON run.tenant_id=call.tenant_id AND run.country=call.country
   AND (run.subject_id=OLD.sender_identity_id OR run.subject_id=ANY(conversation.participant_ids))
  WHERE call.id=OLD.call_id AND call.tenant_id=OLD.tenant_id AND call.country=OLD.country
 )) THEN
  RAISE EXCEPTION 'deleted social call signal cannot be restored';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER privacy_call_signal_reintroduction BEFORE INSERT OR UPDATE ON social.call_signals
 FOR EACH ROW EXECUTE FUNCTION social.guard_privacy_call_signal_reintroduction();
REVOKE ALL ON FUNCTION social.guard_privacy_call_signal_reintroduction() FROM PUBLIC;

CREATE FUNCTION social.privacy_scrub_ephemeral(r text)
RETURNS TABLE(outcome text,evidence_reference text,presence_rows integer,call_rows integer,signal_rows integer)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,social AS $$
DECLARE tenant text; subject text; policy text; governing_country text;
DECLARE prior social.privacy_ephemeral_erasure_runs%ROWTYPE;
DECLARE p_count integer; c_count integer; s_count integer; evidence text;
BEGIN
 SELECT scope.tenant_id,scope.subject_id,scope.policy_version,scope.country
 INTO tenant,subject,policy,governing_country
 FROM identity.privacy_social_erasure_country_scope(r) scope;
 IF tenant IS NULL OR subject IS NULL OR policy IS NULL OR governing_country IS NULL
    OR NOT pg_catalog.pg_input_is_valid(tenant,'uuid')
    OR NOT pg_catalog.pg_input_is_valid(subject,'uuid') THEN
  RAISE EXCEPTION 'social ephemeral erasure scope unavailable';
 END IF;
 evidence := 'social-ephemeral-scrub:'||r;
 -- Lock the same tables that insertion/update takes RowExclusive on. A
 -- concurrent writer completes before deletion or waits until the fence
 -- commits. The order follows call signal writers to avoid a lock cycle.
 LOCK TABLE social.call_sessions,social.call_signals,social.presence
  IN SHARE ROW EXCLUSIVE MODE;
 SELECT * INTO prior FROM social.privacy_ephemeral_erasure_runs run
 WHERE run.request_id=r FOR UPDATE;
 IF FOUND THEN
  IF prior.tenant_id<>tenant::uuid OR prior.country<>governing_country::char(2)
     OR prior.subject_id<>subject::uuid OR prior.policy_version<>policy THEN
   RAISE EXCEPTION 'social ephemeral scrub retry scope changed';
  END IF;
  IF EXISTS(SELECT 1 FROM social.presence p
    WHERE p.tenant_id=tenant::uuid AND p.country=governing_country::char(2)
      AND p.profile_identity_id=subject::uuid)
   OR EXISTS(SELECT 1 FROM social.call_sessions call
     JOIN social.conversations conversation ON conversation.id=call.conversation_id
     WHERE call.tenant_id=tenant::uuid AND call.country=governing_country::char(2)
       AND subject::uuid=ANY(conversation.participant_ids) AND call.status<>'ENDED')
   OR EXISTS(SELECT 1 FROM social.call_signals signal
     JOIN social.call_sessions call ON call.id=signal.call_id
     JOIN social.conversations conversation ON conversation.id=call.conversation_id
     WHERE signal.tenant_id=tenant::uuid AND signal.country=governing_country::char(2)
       AND (signal.sender_identity_id=subject::uuid
            OR subject::uuid=ANY(conversation.participant_ids))) THEN
   RAISE EXCEPTION 'social ephemeral scrub retry found restored activity';
  END IF;
  PERFORM identity.privacy_social_record_retained(r,policy,evidence);
  RETURN QUERY SELECT 'RETAINED'::text,evidence,prior.presence_rows,prior.call_rows,prior.signal_rows;
  RETURN;
 END IF;
 SELECT count(*) INTO p_count FROM social.presence p
 WHERE p.tenant_id=tenant::uuid AND p.country=governing_country::char(2)
   AND p.profile_identity_id=subject::uuid;
 SELECT count(*) INTO c_count FROM social.call_sessions call
 JOIN social.conversations conversation ON conversation.id=call.conversation_id
 WHERE call.tenant_id=tenant::uuid AND call.country=governing_country::char(2)
   AND subject::uuid=ANY(conversation.participant_ids) AND call.status<>'ENDED';
 SELECT count(*) INTO s_count FROM social.call_signals signal
 JOIN social.call_sessions call ON call.id=signal.call_id
 JOIN social.conversations conversation ON conversation.id=call.conversation_id
 WHERE signal.tenant_id=tenant::uuid AND signal.country=governing_country::char(2)
   AND (signal.sender_identity_id=subject::uuid
        OR subject::uuid=ANY(conversation.participant_ids));
 IF p_count>1000 OR c_count>1000 OR s_count>1000 THEN
  RAISE EXCEPTION 'social ephemeral erasure requires a smaller batch';
 END IF;
 DELETE FROM social.call_signals signal USING social.call_sessions call,social.conversations conversation
 WHERE signal.call_id=call.id AND call.conversation_id=conversation.id
   AND signal.tenant_id=tenant::uuid AND signal.country=governing_country::char(2)
   AND (signal.sender_identity_id=subject::uuid
        OR subject::uuid=ANY(conversation.participant_ids));
 UPDATE social.call_sessions call SET status='ENDED',expires_at=LEAST(call.expires_at,clock_timestamp())
 FROM social.conversations conversation
 WHERE call.conversation_id=conversation.id AND call.tenant_id=tenant::uuid
   AND call.country=governing_country::char(2)
   AND subject::uuid=ANY(conversation.participant_ids) AND call.status<>'ENDED';
 DELETE FROM social.presence p WHERE p.tenant_id=tenant::uuid
   AND p.country=governing_country::char(2) AND p.profile_identity_id=subject::uuid;
 INSERT INTO social.privacy_ephemeral_erasure_runs
 (request_id,tenant_id,country,subject_id,policy_version,presence_rows,call_rows,signal_rows,occurred_at)
 VALUES(r,tenant::uuid,governing_country::char(2),subject::uuid,policy,p_count,c_count,s_count,clock_timestamp());
 PERFORM identity.privacy_social_record_retained(r,policy,evidence);
 RETURN QUERY SELECT 'RETAINED'::text,evidence,p_count,c_count,s_count;
END $$;
REVOKE ALL ON FUNCTION social.privacy_scrub_ephemeral(text) FROM PUBLIC,planext4u_social_runtime;
RESET ROLE;
