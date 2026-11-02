SET ROLE planext4u_customer_web_owner;
LOCK TABLE customer_web.privacy_session_scrub_runs IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM customer_web.privacy_session_scrub_runs) THEN
  RAISE EXCEPTION 'customer web session privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION customer_web.privacy_scrub_sessions(text);
DROP TRIGGER privacy_session_reintroduction ON customer_web.sessions;
DROP FUNCTION customer_web.guard_privacy_session_reintroduction();
DROP TRIGGER privacy_session_scrub_immutable ON customer_web.privacy_session_scrub_runs;
DROP INDEX customer_web.customer_web_privacy_session_scope_idx;
DROP TABLE customer_web.privacy_session_scrub_runs;
DROP FUNCTION customer_web.reject_privacy_session_scrub_mutation();
DROP INDEX customer_web.customer_web_sessions_subject_idx;
RESET ROLE;
