SET ROLE planext4u_notification_owner;
ALTER TABLE notification.deliveries DROP COLUMN IF EXISTS data;
RESET ROLE;
