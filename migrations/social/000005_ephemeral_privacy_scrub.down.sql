SET ROLE planext4u_social_owner;
LOCK TABLE social.privacy_ephemeral_erasure_runs IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM social.privacy_ephemeral_erasure_runs) THEN
  RAISE EXCEPTION 'social ephemeral privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION social.privacy_scrub_ephemeral(text);
DROP TRIGGER privacy_call_signal_reintroduction ON social.call_signals;
DROP FUNCTION social.guard_privacy_call_signal_reintroduction();
DROP TRIGGER privacy_call_reintroduction ON social.call_sessions;
DROP FUNCTION social.guard_privacy_call_reintroduction();
DROP TRIGGER conversation_participants_immutable ON social.conversations;
DROP FUNCTION social.guard_conversation_participants_immutable();
DROP TRIGGER privacy_presence_reintroduction ON social.presence;
DROP FUNCTION social.guard_privacy_presence_reintroduction();
DROP TRIGGER privacy_ephemeral_run_immutable ON social.privacy_ephemeral_erasure_runs;
DROP FUNCTION social.reject_privacy_ephemeral_run_mutation();
DROP INDEX social.social_privacy_ephemeral_scope_idx;
DROP TABLE social.privacy_ephemeral_erasure_runs;
RESET ROLE;
