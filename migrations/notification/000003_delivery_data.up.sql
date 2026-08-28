SET ROLE planext4u_notification_owner;
ALTER TABLE notification.deliveries
    ADD COLUMN data jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(data) = 'object');
RESET ROLE;
