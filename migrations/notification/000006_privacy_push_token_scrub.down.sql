SET ROLE planext4u_notification_owner;
LOCK TABLE notification.privacy_push_scrub_runs IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM notification.privacy_push_scrub_runs) THEN
  RAISE EXCEPTION 'notification push privacy evidence blocks rollback';
 END IF;
END $$;
DROP FUNCTION notification.privacy_scrub_push_tokens(text);
DROP TRIGGER privacy_delivery_requeue ON notification.deliveries;
DROP FUNCTION notification.guard_privacy_delivery_requeue();
DROP TRIGGER privacy_push_reintroduction ON notification.push_device;
DROP FUNCTION notification.guard_privacy_push_reintroduction();
DROP TRIGGER privacy_push_scrub_immutable ON notification.privacy_push_scrub_runs;
DROP TABLE notification.privacy_push_scrub_runs;
DROP FUNCTION notification.reject_privacy_push_scrub_mutation();
RESET ROLE;
