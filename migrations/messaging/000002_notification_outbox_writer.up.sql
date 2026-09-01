SET ROLE planext4u_messaging_owner;
GRANT INSERT ON messaging.outbox TO planext4u_notification_runtime;
RESET ROLE;
