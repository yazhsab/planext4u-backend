SET ROLE planext4u_notification_owner;
DROP INDEX IF EXISTS notification.notification_delivery_worker_idx;
ALTER TABLE notification.deliveries DROP CONSTRAINT IF EXISTS notification_delivery_attempts_bounded;
ALTER TABLE notification.deliveries DROP CONSTRAINT IF EXISTS notification_delivery_retry_fields_present;
ALTER TABLE notification.deliveries DROP COLUMN IF EXISTS claim_until;
ALTER TABLE notification.deliveries DROP COLUMN IF EXISTS next_attempt_at;
ALTER TABLE notification.deliveries DROP COLUMN IF EXISTS attempts;
RESET ROLE;
