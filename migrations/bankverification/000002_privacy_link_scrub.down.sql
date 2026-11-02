SET ROLE planext4u_bankverification_owner;
LOCK TABLE bankverification.privacy_link_scrub_runs IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM bankverification.privacy_link_scrub_runs) THEN
  RAISE EXCEPTION 'bank privacy scrub evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION bankverification.privacy_scrub_link_attempts(text);
DROP TRIGGER privacy_link_reintroduction ON bankverification.link_attempts;
DROP FUNCTION bankverification.guard_privacy_link_reintroduction();
DROP TABLE bankverification.privacy_link_scrub_runs;
DROP FUNCTION bankverification.reject_privacy_scrub_mutation();
RESET ROLE;
