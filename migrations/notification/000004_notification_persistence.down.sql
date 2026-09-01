SET ROLE planext4u_notification_owner;
DROP INDEX IF EXISTS notification.notification_push_device_subject_idx;
DROP TABLE IF EXISTS notification.consents;
RESET ROLE;
