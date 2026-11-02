SET ROLE planext4u_messaging_owner;
LOCK TABLE messaging.privacy_outbox_scrub_runs,
 messaging.privacy_outbox_scrubbed_events IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrub_runs)
    OR EXISTS(SELECT 1 FROM messaging.privacy_outbox_scrubbed_events) THEN
  RAISE EXCEPTION 'messaging privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION messaging.privacy_scrub_notification_outbox(text);
DROP TRIGGER privacy_inbox_reprocessing ON messaging.inbox;
DROP FUNCTION messaging.guard_privacy_inbox_reprocessing();
DROP TRIGGER privacy_outbox_reintroduction ON messaging.outbox;
DROP FUNCTION messaging.guard_privacy_outbox_reintroduction();
DROP TRIGGER privacy_outbox_events_immutable ON messaging.privacy_outbox_scrubbed_events;
DROP TRIGGER privacy_outbox_runs_immutable ON messaging.privacy_outbox_scrub_runs;
DROP FUNCTION messaging.reject_privacy_outbox_evidence_mutation();
DROP INDEX messaging.messaging_privacy_outbox_events_request_idx;
DROP TABLE messaging.privacy_outbox_scrubbed_events;
DROP INDEX messaging.messaging_privacy_outbox_scope_idx;
DROP TABLE messaging.privacy_outbox_scrub_runs;
RESET ROLE;
