SET ROLE planext4u_messaging_owner;
REVOKE INSERT ON messaging.outbox FROM planext4u_notification_runtime;
RESET ROLE;
