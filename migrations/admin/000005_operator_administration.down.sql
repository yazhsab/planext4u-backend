BEGIN;
SET ROLE planext4u_admin_owner;
REVOKE INSERT,UPDATE ON admin.operator_accounts FROM planext4u_admin_runtime;
DROP TABLE admin.operator_events;
ALTER TABLE admin.sessions DROP COLUMN operator_revision;
RESET ROLE;
COMMIT;
